// Command executor is the entry point for the multi-symbol execution layer.
//
// It consumes decisions produced by the combination engine from Kafka and
// routes them by strategy ID to their own execution instances. One worker
// and one queue per symbol, isolated from each other.
//
// Usage:
//
//	go run ./cmd/executor                                                  # multi-tenant mode: run PAPER_TRADING strategies for all users on this broker channel
//	go run ./cmd/executor -state LIVE                                      # multi-tenant mode: run all LIVE strategies
//	go run ./cmd/executor -broker okx-demo                                 # multi-tenant mode: run all users with credentials configured for OKX demo
//	go run ./cmd/executor -owner-email you@example.com                     # single-tenant mode: serve only this one user (separate process, backward-compatible behavior)
//
// Multi-tenant concurrent execution (phase 2): -owner-email is now optional.
// Left empty = multi-tenant mode — this process serves all users on the
// exchange channel given by -broker, and each user's strategies execute
// with their own credentials saved in the database (in-memory cache with
// periodic refresh, see brokerCache), without affecting each other; when
// different users trade the same symbol, the underlying market data/risk
// management is already isolated by strategy ID, so this is inherently
// safe. Explicitly setting -owner-email = single-tenant mode, which fully
// preserves pre-refactor behavior (including the env-var credential
// fallback mentioned below) — reserved for large customers who need a
// dedicated process with exclusive resources, not just leftover
// compatibility baggage.
//
// In multi-tenant mode, the env-var credential fallback is completely
// disabled (see the multiTenant parameter on loadBrokerCredentials): if it
// weren't, variables like TF_OKX_API_KEY set in the deployment environment
// would be shared by every user on that channel, meaning everyone's real
// funds would hit the same exchange account — that's a fund cross-contamination
// issue, not an ordinary bug, so env vars are only allowed in single-tenant
// mode (explicit -owner-email).
//
// Every -promotion-interval cycle, this process rescans the strategy list:
// newly-appeared strategies are auto-registered, and strategies that left
// the target state are auto-removed — so new users/strategies take effect
// without restarting the process. A restart would interrupt every other
// user currently running on this process, which should be avoided as much
// as possible now that multiple tenants share one process. The broker
// credential cache's TTL reuses this same interval: a newly-registered
// strategy waits at most one cycle to pick up newly-configured credentials;
// but an already-running strategy whose credentials changed does not hot-reload
// — it still needs to be restarted (or wait to be removed and re-registered)
// for the change to take effect.
//
// Safety convention: by default, only the pure in-memory paper-trading
// channel (paper) is used. Connecting to a real exchange requires
// explicitly setting -broker, and the current version only allows hitting
// each exchange's testnet/demo account. In single-tenant mode, credentials
// can come from environment variables (binance-testnet needs
// TF_BINANCE_API_KEY / TF_BINANCE_API_SECRET; okx-demo needs
// TF_OKX_API_KEY / TF_OKX_API_SECRET / TF_OKX_PASSPHRASE; bybit-testnet
// needs TF_BYBIT_API_KEY / TF_BYBIT_API_SECRET; bitget-demo needs
// TF_BITGET_API_KEY / TF_BITGET_API_SECRET / TF_BITGET_PASSPHRASE); when
// the env vars aren't fully set, it falls back to reading the credentials
// that user saved on the web UI's settings page (needs the same
// TF_MASTER_KEY to decrypt — see
// internal/webui/handlers_settings_brokers.go). In multi-tenant mode, only
// the database path is used.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"tradeforge/internal/config"
	"tradeforge/internal/execution"
	"tradeforge/internal/messaging"
	"tradeforge/internal/secretcrypto"
	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

func main() {
	ownerEmail := flag.String("owner-email", "",
		"single-tenant mode: serve only this one user (optional, their email); leave empty for multi-tenant mode, serving all users on the -broker channel")
	stateFlag := flag.String("state", string(types.StatePaperTrading),
		"load strategies in this state (PAPER_TRADING / LIVE_ELIGIBLE / LIVE)")
	brokerFlag := flag.String("broker", "paper",
		"order channel: paper (pure in-memory paper trading, default) / binance-testnet / okx-demo / bybit-testnet / bitget-demo")
	group := flag.String("group", "tradeforge-executor", "Kafka consumer group ID")
	promotionInterval := flag.Duration("promotion-interval", 5*time.Minute,
		"shared interval for checking whether paper-trading strategies qualify for promotion, rescanning the strategy list (additions/removals), and refreshing the broker credential cache")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	state := types.StrategyState(*stateFlag)
	if !state.Valid() {
		fatal("invalid state %q", *stateFlag)
	}
	if state == types.StateDraft || state == types.StateBacktested {
		fatal("strategies in state %s are not allowed to produce any trades", state)
	}

	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		fatal("failed to connect to database: %v", err)
	}
	defer store.Close()

	// -owner-email left empty = multi-tenant mode: ownerUserID stays an empty
	// string, and every downstream branch (loading strategies, resolving
	// credentials) treats "" as "across all users."
	ownerUserID := ""
	if trimmed := strings.TrimSpace(*ownerEmail); trimmed != "" {
		owner, err := store.GetUserByEmail(ctx, trimmed)
		if err != nil {
			fatal("could not find the user specified by -owner-email %q: %v", trimmed, err)
		}
		ownerUserID = owner.ID
	}
	multiTenant := ownerUserID == ""

	brokers := newBrokerCache(store, cfg.Security.MasterKey, *brokerFlag, multiTenant, *promotionInterval)

	sup := execution.NewSupervisor(logger)
	defer sup.Shutdown()

	reconcileRegistrations(ctx, store, sup, brokers, ownerUserID, state, logger)
	if len(sup.StrategyIDs()) == 0 {
		logger.Warn("no matching strategies found, execution layer is idling", "multi_tenant", multiTenant, "state", state)
	}

	if err := messaging.EnsureTopics(ctx, cfg.Kafka); err != nil {
		logger.Warn("failed to create Kafka topics, relying on auto-creation", "err", err)
	}
	reader := messaging.NewDecisionReader(cfg.Kafka, *group)
	defer reader.Close()

	go runPromotionLoop(ctx, store, ownerUserID, strategy.DefaultGate(), logger, *promotionInterval)
	go runReconcileLoop(ctx, store, sup, brokers, ownerUserID, state, logger, *promotionInterval)

	logger.Info("starting to consume decisions",
		"topic", cfg.Kafka.DecisionTopic, "group", *group, "multi_tenant", multiTenant, "broker", *brokerFlag)
	consume(ctx, reader, sup, logger)

	logger.Info("received stop signal, shutting down execution layer")
	printSummary(sup, logger)
}

// executorStore is the full set of persistence capabilities cmd/executor
// depends on at runtime — using an interface instead of depending directly
// on *storage.Store lets the registerOne/reconcileRegistrations/brokerCache
// chain be unit-tested without a real Postgres, the same pattern as
// brokerCredentialStore/promotionStore. *storage.Store satisfies this
// interface structurally, with no extra code needed.
type executorStore interface {
	brokerCredentialStore
	execution.OrderRecorder
	execution.RiskEventRecorder
	ListStrategiesByState(ctx context.Context, userID string, state types.StrategyState) ([]types.StrategyConfig, error)
	ListStrategiesByStateAllUsers(ctx context.Context, state types.StrategyState) ([]types.StrategyConfig, error)
}

// loadStrategies branches its query on whether ownerUserID is empty:
// non-empty = single-tenant mode, query just that one user; empty =
// multi-tenant mode, query across all users — the same branching pattern
// used by checkPromotions (promote.go).
func loadStrategies(ctx context.Context, store executorStore, ownerUserID string, state types.StrategyState) ([]types.StrategyConfig, error) {
	if ownerUserID != "" {
		return store.ListStrategiesByState(ctx, ownerUserID, state)
	}
	return store.ListStrategiesByStateAllUsers(ctx, state)
}

// reconcileRegistrations diffs the set of strategies currently registered
// in sup against the latest database state: newly-appeared strategies get
// registered, and ones not in the new list (deleted, or their state has
// moved outside the range this process cares about) get removed.
//
// Now that multiple tenants share one process, this isn't a nice-to-have:
// without this step, "a user's new strategy taking effect" would require
// restarting the entire process, and a restart interrupts every other
// user's strategies currently running on this process (all in-memory
// RiskManager/Stats state is lost) — exactly the opposite of what
// "merging into one process serving everyone" is meant to achieve.
func reconcileRegistrations(
	ctx context.Context, store executorStore, sup *execution.Supervisor, brokers *brokerCache,
	ownerUserID string, state types.StrategyState, logger *slog.Logger,
) {
	strategies, err := loadStrategies(ctx, store, ownerUserID, state)
	if err != nil {
		logger.Error("failed to rescan strategy list, keeping existing registrations unchanged", "err", err)
		return
	}

	fresh := make(map[string]struct{}, len(strategies))
	for _, s := range strategies {
		fresh[s.ID] = struct{}{}
		registerOne(ctx, store, sup, brokers, s, logger)
	}

	for _, id := range sup.StrategyIDs() {
		if _, ok := fresh[id]; ok {
			continue
		}
		if err := sup.Unregister(id); err != nil {
			logger.Error("failed to remove a strategy that left the target state", "strategy_id", id, "err", err)
			continue
		}
		logger.Info("stopped execution instance (strategy left the target state)", "strategy_id", id)
	}
}

// registerOne resolves the broker for a single strategy's owning user and
// registers it with the Supervisor. Whether credential resolution or
// registration fails, only that one strategy is skipped — it doesn't affect
// other users/strategies in the same batch, which is where the isolation
// principle starts taking effect. errors.Is(err,
// execution.ErrAlreadyRegistered) is a normal case (already registered in
// the previous scan pass) and is silently skipped, not treated as an error.
func registerOne(ctx context.Context, store executorStore, sup *execution.Supervisor, brokers *brokerCache, s types.StrategyConfig, logger *slog.Logger) {
	br, err := brokers.get(ctx, s.UserID)
	if err != nil {
		logger.Error("failed to resolve the user's exchange credentials, skipping this strategy",
			"strategy_id", s.ID, "user_id", s.UserID, "err", err)
		return
	}
	if err := sup.Register(ctx, s, br,
		execution.WithOrderRecorder(store),
		execution.WithRiskEventRecorder(store),
	); err != nil {
		if errors.Is(err, execution.ErrAlreadyRegistered) {
			return
		}
		logger.Error("strategy registration failed, skipping",
			"strategy_id", s.ID, "symbol", s.Symbol, "err", err)
		return
	}
	logger.Info("started execution instance",
		"strategy_id", s.ID, "user_id", s.UserID, "symbol", s.Symbol, "name", s.Name,
		"modules", len(s.Modules), "combine", s.Combine)
}

// runReconcileLoop calls reconcileRegistrations repeatedly at a fixed
// interval until ctx is cancelled. It reuses the same -promotion-interval
// value as runPromotionLoop, but with its own independent ticker — there's
// no need for the two to be precisely synced to the same moment, this just
// avoids introducing a second "how often does this refresh" concept to keep
// track of.
func runReconcileLoop(
	ctx context.Context, store executorStore, sup *execution.Supervisor, brokers *brokerCache,
	ownerUserID string, state types.StrategyState, logger *slog.Logger, interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileRegistrations(ctx, store, sup, brokers, ownerUserID, state, logger)
		}
	}
}

func consume(
	ctx context.Context, reader *messaging.DecisionReader,
	sup *execution.Supervisor, logger *slog.Logger,
) {
	for {
		d, err := reader.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Error("failed to read decision, will retry shortly", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		if err := sup.Dispatch(d); err != nil {
			// An unregistered strategy is normal: the same topic carries
			// decisions for strategies in other states too.
			if !errors.Is(err, execution.ErrUnknownStrategy) {
				logger.Warn("failed to dispatch decision", "strategy_id", d.StrategyID, "err", err)
			}
		}
	}
}

// brokerCredentialStore is the minimal persistence interface buildBroker
// needs, purely so it can be unit-tested without a real Postgres — the same
// pattern as promote.go's promotionStore. *storage.Store satisfies this
// interface structurally, with no extra code needed.
type brokerCredentialStore interface {
	ActiveBrokerProfile(ctx context.Context, userID, broker string) (storage.BrokerProfile, error)
}

// brokerCacheEntry is one cached entry in brokerCache.
type brokerCacheEntry struct {
	broker    execution.Broker
	fetchedAt time.Time
}

// brokerCache caches constructed order-channel instances per user — a given
// user's credential decryption/construction has a real cost and shouldn't
// be redone every time a strategy is registered. The structure copies the
// agentCache pattern from internal/webui/server.go: an RWMutex-protected
// map, reading from cache on hit, building fresh only on miss/expiry.
//
// The key is just userID, with no broker kind: -broker is constant for the
// lifetime of a process, so it doesn't need to be a dimension of the cache
// key too.
//
// The key difference from agentCache is deliberate: agentCache caches
// "confirmed not configured" empty results, because it sits on the hot path
// of every HTTP request where repeated DB lookups have a real cost;
// brokerCache is only accessed during strategy registration/periodic
// rescans (minute-level frequency, not per-second), so caching failed
// results would only break the core multi-tenant SaaS experience of "a user
// just configured exchange credentials and is waiting for their existing
// PAPER_TRADING strategy to automatically start on the next scan cycle" —
// not worth it. Only successes are cached here; failures always retry.
//
// Successful results get a TTL; expiry only affects the next fresh get call
// (i.e. a newly-registered strategy) and does not hot-swap credentials into
// an already-running Worker — a Worker's broker is a private field fixed at
// construction time, with no setter. Getting an already-running strategy to
// use new credentials still requires it to be removed and re-registered
// first (see the comment at the top of main.go).
type brokerCache struct {
	mu      sync.RWMutex
	entries map[string]brokerCacheEntry // key: userID

	store       brokerCredentialStore
	masterKey   string
	kind        string
	multiTenant bool
	ttl         time.Duration
}

func newBrokerCache(store brokerCredentialStore, masterKey, kind string, multiTenant bool, ttl time.Duration) *brokerCache {
	return &brokerCache{
		entries:     make(map[string]brokerCacheEntry),
		store:       store,
		masterKey:   masterKey,
		kind:        kind,
		multiTenant: multiTenant,
		ttl:         ttl,
	}
}

func (c *brokerCache) get(ctx context.Context, userID string) (execution.Broker, error) {
	c.mu.RLock()
	entry, ok := c.entries[userID]
	c.mu.RUnlock()
	if ok && time.Since(entry.fetchedAt) < c.ttl {
		return entry.broker, nil
	}

	br, err := buildBroker(ctx, c.store, c.masterKey, userID, c.kind, c.multiTenant)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.entries[userID] = brokerCacheEntry{broker: br, fetchedAt: time.Now()}
	c.mu.Unlock()
	return br, nil
}

// buildBroker constructs an order instance for the channel chosen by
// -broker. In single-tenant mode (multiTenant=false), credentials are read
// from environment variables first (legacy behavior, so scripted
// deployments don't need to touch the database); when the env vars aren't
// fully set, it falls back to reading the currently-active profile that
// user saved on the web UI's settings page. In multi-tenant mode
// (multiTenant=true), the env-var fallback is completely disabled and only
// the database is used — see the comment at the top of
// loadBrokerCredentials; this isn't a simplification, it's a necessary
// restriction to prevent multiple tenants sharing one set of env-var
// credentials, which would cross-contaminate funds.
func buildBroker(ctx context.Context, store brokerCredentialStore, masterKey, ownerUserID, name string, multiTenant bool) (execution.Broker, error) {
	kind := execution.BrokerKind(name)
	if kind == "" {
		kind = execution.BrokerKindPaper
	}
	if !kind.Valid() {
		return nil, fmt.Errorf("unknown order channel %q (options: paper / binance-testnet / okx-demo / bybit-testnet / bitget-demo)", name)
	}
	if kind == execution.BrokerKindPaper {
		return execution.NewBroker(kind, "", "", "")
	}

	apiKey, apiSecret, passphrase, err := loadBrokerCredentials(ctx, store, masterKey, ownerUserID, kind, multiTenant)
	if err != nil {
		return nil, err
	}
	return execution.NewBroker(kind, apiKey, apiSecret, passphrase)
}

// envVarsForBroker is the environment variable names for each order
// channel — kept identical to the previous version's variable names, since
// adding a database storage path shouldn't break existing scripted
// deployments.
func envVarsForBroker(kind execution.BrokerKind) (apiKeyVar, apiSecretVar, passphraseVar string) {
	switch kind {
	case execution.BrokerKindBinanceTestnet:
		return "TF_BINANCE_API_KEY", "TF_BINANCE_API_SECRET", ""
	case execution.BrokerKindOKXDemo:
		return "TF_OKX_API_KEY", "TF_OKX_API_SECRET", "TF_OKX_PASSPHRASE"
	case execution.BrokerKindBybitTestnet:
		return "TF_BYBIT_API_KEY", "TF_BYBIT_API_SECRET", ""
	case execution.BrokerKindBitgetDemo:
		return "TF_BITGET_API_KEY", "TF_BITGET_API_SECRET", "TF_BITGET_PASSPHRASE"
	default:
		return "", "", ""
	}
}

// loadBrokerCredentials, in single-tenant mode (multiTenant=false), prefers
// environment variables and falls back to the user's currently-active
// profile in the database when they're missing (needs TF_MASTER_KEY to
// decrypt — the server-side master key that encrypts every user's
// credentials, not anyone's login password — see
// internal/webui/handlers_settings_brokers.go).
//
// In multi-tenant mode (multiTenant=true), the env-var check is skipped
// entirely and it goes straight to this user's own credentials in the
// database — environment variables are shared across the whole process, so
// using them in multi-tenant mode would make every user's strategies on
// this broker channel place orders with the same credentials, which is a
// fund cross-contamination issue, not backward-compatible behavior that can
// be allowed through.
func loadBrokerCredentials(ctx context.Context, store brokerCredentialStore, masterKey, ownerUserID string, kind execution.BrokerKind, multiTenant bool) (apiKey, apiSecret, passphrase string, err error) {
	keyVar, secretVar, passVar := envVarsForBroker(kind)
	if !multiTenant {
		apiKey, apiSecret = os.Getenv(keyVar), os.Getenv(secretVar)
		if passVar != "" {
			passphrase = os.Getenv(passVar)
		}
		if apiKey != "" && apiSecret != "" {
			return apiKey, apiSecret, passphrase, nil
		}
	}

	if masterKey == "" {
		if multiTenant {
			return "", "", "", fmt.Errorf("%s is missing credentials: no TF_MASTER_KEY, cannot read the user's saved profile", kind)
		}
		return "", "", "", fmt.Errorf(
			"%s is missing credentials: environment variables %s/%s are not set, and no TF_MASTER_KEY to read a saved profile",
			kind, keyVar, secretVar)
	}
	profile, err := store.ActiveBrokerProfile(ctx, ownerUserID, string(kind))
	if err != nil {
		if multiTenant {
			return "", "", "", fmt.Errorf(
				"%s is missing credentials: this user has no currently-active profile in the database (save one on the settings page first): %w", kind, err)
		}
		return "", "", "", fmt.Errorf(
			"%s is missing credentials: environment variables are not set, and this user has no currently-active profile in the database either (save one on the settings page first): %w",
			kind, err)
	}
	plaintext, err := secretcrypto.Decrypt(masterKey, profile.EncryptedCredentials, profile.KeySalt, profile.KeyNonce)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to decrypt %s's saved profile (does TF_MASTER_KEY match the one used when it was saved?): %w", kind, err)
	}
	var creds struct {
		APIKey     string `json:"api_key"`
		APISecret  string `json:"api_secret"`
		Passphrase string `json:"passphrase"`
	}
	if err := json.Unmarshal([]byte(plaintext), &creds); err != nil {
		return "", "", "", fmt.Errorf("failed to parse %s's saved profile: %w", kind, err)
	}
	return creds.APIKey, creds.APISecret, creds.Passphrase, nil
}

func printSummary(sup *execution.Supervisor, logger *slog.Logger) {
	for _, ss := range sup.StatsByStrategy() {
		st := ss.Stats
		logger.Info("execution instance stats",
			"strategy_id", ss.StrategyID,
			"symbol", ss.Symbol,
			"decisions", st.DecisionsSeen,
			"orders", st.OrdersPlaced,
			"rejected", st.OrdersRejected,
			"risk_events", st.RiskEvents,
			"errors", st.Errors,
			"realized_pnl", st.RealizedPnL.String(),
			"suspended", st.Suspended,
		)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
