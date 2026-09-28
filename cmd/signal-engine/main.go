// Command signal-engine is the long-running service entry point for the
// composition engine.
//
// It continuously fetches market data for each strategy's symbol, calls the
// composition engine to compute a decision, persists it for audit, and
// publishes it to Kafka for the execution layer and backtester to consume.
//
// Usage:
//
//	go run ./cmd/signal-engine -candles testdata/btcusdt_1h.csv       # replay a local CSV
//	go run ./cmd/signal-engine -source okx -state PAPER_TRADING       # multi-tenant mode: live OKX feed, serves all users
//	go run ./cmd/signal-engine -source okx -owner-email you@example.com  # single-tenant mode
//
// -source csv (default) replays a local CSV bar by bar, for running the
// whole pipeline offline. -source okx backfills a historical window over
// REST at startup, then subscribes to a WebSocket to keep feeding closed
// candles in real time — this is what actually makes it "live". OKX was
// chosen over the Binance mentioned by default in CLAUDE.md because
// Binance rejects requests (451) from the egress IPs of common cloud dev
// environments on regional restrictions, and OKX doesn't have that problem;
// see the internal/marketdata/okx package comments for details.
//
// Multi-tenant concurrent execution (phase 2): -owner-email is now optional,
// with the same semantics as cmd/executor's -owner-email — empty = multi-tenant
// mode, one process computes decisions for all users concurrently; set explicitly
// = single-tenant mode, behavior identical to before the rework. Multi-tenant
// mode naturally avoids fetching the same market data twice: when different
// users trade the same symbol and timeframe, live.go's candleCache dedupes by
// (symbol, timeframe) — it never had a per-user dimension to begin with — so
// two users' strategies naturally share the same REST backfill + WebSocket
// subscription.
//
// On the -source okx path, the strategy list is rescanned every
// -rescan-interval to open a new subscription for any newly-seen
// (symbol, timeframe) combination — this only covers the case of "a new user
// creates a strategy on a symbol/timeframe nobody is subscribed to yet".
// The case of "a new user's strategy happens to land on a (symbol, timeframe)
// someone is already subscribed to" is not handled this round and still
// requires a process restart to take effect (each subscription's
// triggerStrategies in live.go is a closure argument fixed at startup, not a
// shared structure that can be appended to later; supporting that properly
// needs a bigger change). -source csv is an offline one-shot replay, so the
// concept of rescanning doesn't naturally apply there.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"tradeforge/internal/config"
	"tradeforge/internal/engine"
	"tradeforge/internal/marketdata"
	"tradeforge/internal/marketdata/okx"
	"tradeforge/internal/messaging"
	"tradeforge/internal/modules"
	"tradeforge/internal/storage"
	"tradeforge/pkg/types"
)

func main() {
	ownerEmail := flag.String("owner-email", "",
		"single-tenant mode: serve only this one user (optional, that user's email); "+
			"empty means multi-tenant mode, serving all users — same boundary as cmd/executor's "+
			"-owner-email, see the comment at the top of that main.go")
	source := flag.String("source", "csv", "market data source: csv (replay a local file) or okx (live feed)")
	candlesPath := flag.String("candles", "", "market data CSV path (required when -source csv)")
	stateFlag := flag.String("state", string(types.StatePaperTrading),
		"compute decisions for strategies in this state")
	interval := flag.Duration("interval", 200*time.Millisecond, "delay between replayed candles (-source csv only)")
	window := flag.Int("window", 1200, "max number of historical candles fed to modules")
	backfill := flag.Int("backfill", 300, "number of historical candles to backfill over REST at startup "+
		"(-source okx only; OKX's per-request limit is exactly 300)")
	rescanInterval := flag.Duration("rescan-interval", 5*time.Minute,
		"how often to rescan the strategy list and open subscriptions for newly-seen "+
			"(symbol, timeframe) combinations (-source okx only; a new user's strategy on an "+
			"already-subscribed combination still needs a restart to take effect, see the top comment)")
	flag.Parse()

	if *source == "csv" && *candlesPath == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *source != "csv" && *source != "okx" {
		fatal("-source only supports csv or okx, got %q", *source)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		fatal("failed to connect to the database: %v", err)
	}
	defer store.Close()

	if err := messaging.EnsureTopics(ctx, cfg.Kafka); err != nil {
		fatal("failed to create Kafka topics: %v", err)
	}
	pub := messaging.NewKafkaPublisher(cfg.Kafka)
	defer pub.Close()

	// Empty -owner-email = multi-tenant mode: ownerUserID stays an empty
	// string, and loadStrategies branches on that empty string into a
	// "query across all users" path.
	ownerUserID := ""
	if trimmed := strings.TrimSpace(*ownerEmail); trimmed != "" {
		owner, err := store.GetUserByEmail(ctx, trimmed)
		if err != nil {
			fatal("could not find the user specified by -owner-email %q: %v", trimmed, err)
		}
		ownerUserID = owner.ID
	}
	multiTenant := ownerUserID == ""

	state := types.StrategyState(*stateFlag)
	strategies, err := loadStrategies(ctx, store, ownerUserID, state)
	if err != nil {
		fatal("failed to load strategies: %v", err)
	}
	if len(strategies) == 0 {
		// No longer fatal: in multi-tenant mode, "no user currently has a
		// strategy in this state" is a normal transient condition (paired
		// with the periodic rescan below, one may show up any time), not an
		// error needing manual intervention. In single-tenant mode there's
		// also no reason to exit just because the user hasn't created their
		// first strategy yet.
		logger.Warn("no matching strategies found, idling for now", "multi_tenant", multiTenant, "state", state)
	}

	e := engine.New(modules.NewDefaultRegistry(),
		engine.WithAuditor(store),
		engine.WithPublisher(pub),
		engine.WithTimeout(cfg.Engine.ModuleTimeout),
		engine.WithLogger(logger),
	)

	for _, s := range strategies {
		logger.Info("strategy loaded",
			"strategy_id", s.ID, "symbol", s.Symbol, "name", s.Name, "combine", s.Combine)
	}

	if *source == "okx" {
		client := okx.NewClient()
		logger.Info("using live OKX market data", "backfill", *backfill, "window", *window, "multi_tenant", multiTenant)
		reload := func(ctx context.Context) ([]types.StrategyConfig, error) {
			return loadStrategies(ctx, store, ownerUserID, state)
		}
		runLive(ctx, e, strategies, client, client, *backfill, *window, logger, reload, *rescanInterval)
		logger.Info("stopped")
		return
	}

	feeds := loadCSVFeeds(*candlesPath, strategies, logger)
	replay(ctx, e, strategies, feeds, *window, *interval, logger)
	logger.Info("replay finished")
}

// strategyLister is the minimal persistence interface loadStrategies needs,
// just so callers can unit test without a real Postgres. *storage.Store
// satisfies this interface structurally.
type strategyLister interface {
	ListStrategiesByState(ctx context.Context, userID string, state types.StrategyState) ([]types.StrategyConfig, error)
	ListStrategiesByStateAllUsers(ctx context.Context, state types.StrategyState) ([]types.StrategyConfig, error)
}

// loadStrategies branches on whether ownerUserID is empty: non-empty =
// single-tenant mode, query only that one user's strategies; empty =
// multi-tenant mode, query across all users — same branching pattern used by
// cmd/executor.
func loadStrategies(ctx context.Context, store strategyLister, ownerUserID string, state types.StrategyState) ([]types.StrategyConfig, error) {
	if ownerUserID != "" {
		return store.ListStrategiesByState(ctx, ownerUserID, state)
	}
	return store.ListStrategiesByStateAllUsers(ctx, state)
}

// loadCSVFeeds loads CSV market data deduplicated by (symbol, timeframe).
//
// Dedup by the pair rather than by symbol alone: if two strategies share the
// same symbol but different timeframes (e.g. one BTCUSDT strategy on 1h and
// another on 4h), deduping by symbol alone would silently make the second
// one read the first one's data — mismatched timeframes with no error. That's
// a more dangerous failure mode than simply missing data.
//
// Every timeframe a multi-timeframe strategy needs (the trigger timeframe
// plus each module's declared background timeframes, see
// StrategyConfig.RequiredTimeframes) must be expanded into its own key,
// rather than loading only the strategy's trigger timeframe — otherwise
// modules that need a background timeframe never get data on this CSV
// replay path and stay degraded for the whole run.
func loadCSVFeeds(path string, strategies []types.StrategyConfig, logger *slog.Logger) map[feedKey]types.MarketData {
	feeds := make(map[feedKey]types.MarketData, len(strategies))
	for _, s := range strategies {
		for _, tf := range s.RequiredTimeframes() {
			k := feedKey{s.Symbol, tf}
			if _, ok := feeds[k]; ok {
				continue
			}
			md, err := marketdata.LoadCSV(path, s.Symbol, tf)
			if err != nil {
				logger.Error("failed to load market data, skipping this timeframe", "symbol", s.Symbol, "timeframe", tf, "err", err)
				continue
			}
			feeds[k] = md
		}
	}
	return feeds
}

func replay(
	ctx context.Context, e *engine.Engine,
	strategies []types.StrategyConfig, feeds map[feedKey]types.MarketData,
	window int, interval time.Duration, logger *slog.Logger,
) {
	maxBars := 0
	for _, md := range feeds {
		if len(md.Candles) > maxBars {
			maxBars = len(md.Candles)
		}
	}

	for i := 0; i < maxBars; i++ {
		select {
		case <-ctx.Done():
			return
		default:
		}

		for _, s := range strategies {
			trigger, ok := feeds[feedKey{s.Symbol, s.Timeframe}]
			if !ok || i >= len(trigger.Candles) {
				continue
			}
			start := 0
			if window > 0 && i+1 > window {
				start = i + 1 - window
			}
			cutoff := trigger.Candles[i].CloseTime
			strategyFeeds := map[types.Timeframe]types.MarketData{
				s.Timeframe: {
					Symbol: trigger.Symbol, Timeframe: trigger.Timeframe,
					Candles: trigger.Candles[start : i+1],
				},
			}
			for _, tf := range s.RequiredTimeframes() {
				if tf == s.Timeframe {
					continue
				}
				ctxMD, ok := feeds[feedKey{s.Symbol, tf}]
				if !ok {
					continue // A missing background timeframe is left for the engine to degrade per-module; not special-cased here
				}
				aligned := types.AlignAsOf(ctxMD.Candles, cutoff)
				if window > 0 && len(aligned) > window {
					aligned = aligned[len(aligned)-window:]
				}
				strategyFeeds[tf] = types.MarketData{Symbol: ctxMD.Symbol, Timeframe: tf, Candles: aligned}
			}

			d, err := e.Process(ctx, s, strategyFeeds)
			if err != nil {
				// One strategy's error doesn't stop the others from being computed.
				logger.Error("failed to compute decision", "strategy_id", s.ID, "symbol", s.Symbol, "err", err)
				continue
			}
			if d.Triggered {
				logger.Info("decision triggered",
					"strategy_id", s.ID, "symbol", s.Symbol,
					"direction", d.Direction, "score", d.Score,
					"price", d.Price.String(), "reason", d.Reason)
			}
		}

		if interval > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
