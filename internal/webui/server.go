// Package webui implements Stage 7's minimal usable interface: the strategy
// configuration wizard, the state board, strategy detail views.
//
// This layer only displays capability the backend already has -- it never
// re-implements business rules itself: translation is internal/agent's job,
// state transitions are internal/strategy's job, data read/write is
// internal/storage's job. The interface also has to hold the compliance red
// line: no copy anywhere may contain investment-advice language like
// "suggest" or "recommend" -- factual description only.
package webui

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"regexp"
	"sync"

	"tradeforge/internal/agent"
	"tradeforge/internal/i18n"
	"tradeforge/internal/marketdata/okx"
	"tradeforge/internal/modules"
	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// uuidPattern roughly validates that a path param looks like a UUID.
//
// strategies.id is a UUID column in Postgres: querying with an
// obviously-malformed string (say, a typo'd path in the browser) makes the
// driver return "invalid input syntax for type uuid" directly -- that error
// isn't pgx.ErrNoRows, so Store won't map it to ErrNotFound. Without
// filtering it out here, a malformed ID would come back as a 500 internal
// server error when it should just be an ordinary 404.
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func looksLikeUUID(s string) bool { return uuidPattern.MatchString(s) }

// Store is the minimal persistence interface the interface layer needs.
//
// It's an interface rather than a direct dependency on *storage.Store so the
// handlers' core logic can be unit tested without a real Postgres -- the same
// pattern already proven by cmd/executor's promotionStore. *storage.Store
// satisfies this interface structurally, no extra code needed.
//
// Multi-tenant SaaS rework: aside from SaveStrategy/RecordTransition/
// SaveAgentProfile/SaveBrokerProfile (the data being written already carries
// its own ownership, no extra param needed), almost every method gained a
// userID parameter -- data isolation relies on row-level filtering at the
// database, not on the interface layer deciding for itself "is this row
// mine" before showing it; one missed check there would be a real
// authorization bypass.
type Store interface {
	ListStrategies(ctx context.Context, userID string) ([]types.StrategyConfig, error)
	GetStrategy(ctx context.Context, userID, id string) (types.StrategyConfig, error)
	SaveStrategy(ctx context.Context, cfg types.StrategyConfig) error
	DeleteStrategy(ctx context.Context, userID, id string) error
	RecordTransition(ctx context.Context, t storage.Transition) error
	UpdateStrategyState(ctx context.Context, userID string, t storage.Transition) error
	ListTransitions(ctx context.Context, userID, strategyID string) ([]storage.Transition, error)
	ListDecisions(ctx context.Context, userID, strategyID string, limit int) ([]types.Decision, error)
	ListOrders(ctx context.Context, userID, strategyID string, limit int) ([]types.Order, error)
	PaperStats(ctx context.Context, userID, strategyID string) (strategy.PaperStats, error)
	LatestBacktestResult(ctx context.Context, userID, strategyID string) (types.BacktestResult, error)

	// SaveAgentProfile/ListAgentProfiles/ActivateAgentProfile/DeleteAgentProfile/
	// ActiveAgentProfile support the settings page persisting several saved
	// LLM provider profiles (see handlers_settings.go). The API key is
	// already encrypted by the caller before it reaches here -- this layer
	// only stores/retrieves ciphertext, never touches the plaintext key or
	// the encryption logic itself.
	SaveAgentProfile(ctx context.Context, p storage.AgentProfile, activate bool) error
	GetAgentProfile(ctx context.Context, userID, id string) (storage.AgentProfile, error)
	ListAgentProfiles(ctx context.Context, userID string) ([]storage.AgentProfile, error)
	ActivateAgentProfile(ctx context.Context, userID, id string) error
	DeleteAgentProfile(ctx context.Context, userID, id string) error
	ActiveAgentProfile(ctx context.Context, userID string) (storage.AgentProfile, error)

	// SaveBrokerProfile/GetBrokerProfile/ListBrokerProfiles/ActivateBrokerProfile/
	// DeleteBrokerProfile are the same persistence pattern for BrokerProfile,
	// letting the settings page save exchange order-routing credentials (see
	// handlers_settings_brokers.go). Unlike AgentProfile, "currently active"
	// is grouped by "user + broker," not globally unique per user -- see
	// storage.BrokerProfile's doc comment.
	SaveBrokerProfile(ctx context.Context, p storage.BrokerProfile, activate bool) error
	GetBrokerProfile(ctx context.Context, userID, id string) (storage.BrokerProfile, error)
	ListBrokerProfiles(ctx context.Context, userID string) ([]storage.BrokerProfile, error)
	ActivateBrokerProfile(ctx context.Context, userID, id string) error
	DeleteBrokerProfile(ctx context.Context, userID, id string) error

	// CreateUser/GetUserByEmail/GetUser support account signup/login (see
	// handlers_auth.go). SetUserPreferredLang persists a logged-in user's
	// language-toggle choice to their account (see lang.go's handleSetLang),
	// so it follows them across devices and so cmd/notifier can later send
	// their alerts in their own language.
	CreateUser(ctx context.Context, u storage.User) error
	GetUserByEmail(ctx context.Context, email string) (storage.User, error)
	GetUser(ctx context.Context, id string) (storage.User, error)
	SetUserPreferredLang(ctx context.Context, userID, lang string) error

	// SaveNotificationChannel/ListNotificationChannels/GetNotificationChannel/
	// SetNotificationChannelEnabled/DeleteNotificationChannel support the
	// settings page persisting notification-channel configs (see
	// handlers_settings_notifications.go). The key difference from
	// AgentProfile/BrokerProfile: there's no "exactly one currently active"
	// semantics -- any number of channels can be is_enabled=true at once, see
	// storage.NotificationChannel's doc comment. cmd/notifier accesses the
	// same table through its own separate notifierStore interface, not this
	// one -- a method of the kind tagged "SECURITY: cross-user query" in
	// postgres.go must never be added here; security_guard_test.go will
	// block it.
	SaveNotificationChannel(ctx context.Context, c storage.NotificationChannel) error
	ListNotificationChannels(ctx context.Context, userID string) ([]storage.NotificationChannel, error)
	GetNotificationChannel(ctx context.Context, userID, id string) (storage.NotificationChannel, error)
	SetNotificationChannelEnabled(ctx context.Context, userID, id string, enabled bool) error
	DeleteNotificationChannel(ctx context.Context, userID, id string) error
}

// agentStatus is the currently-effective Agent configuration, used only for
// settings-page display -- it plays no part in translation logic itself.
type agentStatus struct {
	// Provider is empty when the Agent was configured via an environment
	// variable (a startup flag) rather than through the settings page -- in
	// that case we don't know the specific provider/model and can only show
	// "configured."
	Provider agent.Provider
	Model    string
	// KeyHint is the masked key (e.g. "sk-...ab12"), never the full key.
	KeyHint string
	// EnvConfigured is true only for the one shared agentStatus built at
	// startup from an environment variable (see New()) -- the settings page
	// renders a translated "configured via environment variable" label for
	// this case instead of a literal KeyHint, since there's no real key
	// value to mask and show.
	EnvConfigured bool
	// LastErr is the reason the most recent attempt to update the
	// configuration failed; it doesn't affect the Agent that's still
	// currently in effect.
	LastErr string
	// ProfileID, when non-empty, means the currently-effective configuration
	// was loaded/activated from this row in the agent_profiles table (see
	// handlers_settings.go). Empty means the current configuration is only
	// in effect in memory -- either started via an environment variable, or
	// the degraded result of a missing login password/failed encryption --
	// used when deleting a saved configuration from the database to decide
	// "is the one being deleted the one currently in use."
	ProfileID string
}

func (s agentStatus) Configured() bool { return s.KeyHint != "" || s.EnvConfigured }

// agentCacheEntry caches a given user's already-decrypted/constructed Agent,
// avoiding redoing the scrypt key derivation on every request (see
// internal/secretcrypto's doc comment: tens of milliseconds per call, and
// paying that cost on every page load in a multi-tenant setting would be a
// real performance regression). agent == nil means "already confirmed this
// user has no usable configuration" (either never configured, or the last
// configuration attempt failed) -- distinguished from "never looked up yet"
// by whether the key exists in the map at all, so that even a
// never-configured user only triggers one real database query, not one per
// request.
type agentCacheEntry struct {
	agent  *agent.Agent
	status agentStatus
}

// Server holds every dependency the interface layer needs to run.
type Server struct {
	store Store

	// agentCache caches each user's already-decrypted/constructed Agent (see
	// agentCacheEntry). envAgent/envAgentStatus is the one configured at
	// startup via an environment variable (cmd/webui/main.go's
	// ANTHROPIC_API_KEY path), not belonging to any specific user -- when a
	// given user hasn't configured any model of their own under /settings,
	// it falls back to this one as a team-shared default, preserving the
	// original single-operator behavior of "start with an environment
	// variable and it just works."
	agentCacheMu   sync.RWMutex
	agentCache     map[string]agentCacheEntry
	envAgent       *agent.Agent
	envAgentStatus agentStatus

	gate strategy.Gate
	// tmplByLang holds one fully-bound *template.Template per supported
	// i18n.Lang -- see render.go's parseTemplatesByLang. Look up via
	// s.tmplFor(lang), never index this map directly (it falls back to
	// i18n.DefaultLang for a lang not present).
	tmplByLang map[i18n.Lang]*template.Template
	logger     *slog.Logger

	// registry exists independently of agent: validating a StrategyConfig
	// (strategy.Validate) only needs the module registry, not an LLM. Both
	// the visual builder (handlers_builder.go) and handleWizardConfirm rely
	// on it to validate and save a strategy even with no LLM key configured
	// -- handleWizardConfirm used to reach the registry indirectly through
	// ag.Confirm, which meant this step (which never actually needed an LLM)
	// got blocked on whether the agent was ready.
	registry *modules.Registry
	// okxClient provides read-only candle data (GET /api/candles) to the
	// visual builder's chart page. OKX's market-data API is public,
	// read-only, and needs no API key, so construction needs no
	// configuration -- the same usage as cmd/signal-engine's.
	okxClient *okx.Client
	// symbolCache supplies candidates for the "enter a symbol" autocomplete
	// (GET /api/symbols), lazily loading and caching the full set of OKX spot
	// symbols -- see symbols.go.
	symbolCache *symbolCache

	// masterKey is the server-side master key (TF_MASTER_KEY) that
	// internal/secretcrypto uses to encrypt/decrypt every user's LLM/
	// exchange credentials -- it is not any user's login password; login
	// authentication relies entirely on sessions/bcrypt, see auth.go.
	// cmd/webui/main.go's production startup path requires it to be set;
	// only tests leave it empty.
	masterKey string
	sessions  *sessionStore

	// vapidPublicKey is the public key (RFC 8292) the browser needs for a web
	// push subscription -- it isn't a secret, and in fact has to reach
	// browser JS before PushManager.subscribe() can even be called. The
	// private key only ever lives in the cmd/notifier process
	// (internal/notify.VAPIDWebPushSender); webui never touches it.
	vapidPublicKey string

	// backtestCfg/runPython support the "run backtest" button
	// (handlers_backtest.go): pull real historical data, replay signals in
	// memory, then spawn a Python subprocess to handle matching and
	// persistence. runPython spawns a real subprocess by default; tests swap
	// in a fake implementation so the orchestration logic (pulling data,
	// writing temp files, assembling args, handling errors) can be tested
	// without actually having Python installed.
	backtestCfg BacktestRunnerConfig
	runPython   func(ctx context.Context, args []string, dir string) (stdout, stderr []byte, err error)
}

// SetMasterKey sets the server-side master key used to encrypt/decrypt every
// user's credentials. cmd/webui/main.go's production startup path always
// calls it (it refuses to start if TF_MASTER_KEY isn't set, see that file);
// only tests skip this step.
func (s *Server) SetMasterKey(key string) {
	s.masterKey = key
}

// SetVAPIDPublicKey sets the VAPID public key used for web push subscriptions
// (see the vapidPublicKey field's doc comment). cmd/webui/main.go calls it at
// startup with cfg.Notification.WebPush.VAPIDPublicKey; leaving it empty just
// makes the settings page's "enable push" button fail when clicked -- it
// doesn't affect anything else and doesn't need to block startup.
func (s *Server) SetVAPIDPublicKey(key string) {
	s.vapidPublicKey = key
}

// New constructs a Server and parses every template. A template-parsing
// failure is a startup-time error, not a runtime one.
func New(store Store, ag *agent.Agent, gate strategy.Gate, logger *slog.Logger) (*Server, error) {
	tmplByLang, err := parseTemplatesByLang()
	if err != nil {
		return nil, err
	}
	okxClient := okx.NewClient()
	s := &Server{
		store: store, gate: gate, tmplByLang: tmplByLang, logger: logger, sessions: newSessionStore(),
		agentCache: make(map[string]agentCacheEntry),
		registry:   modules.NewDefaultRegistry(), okxClient: okxClient,
		symbolCache: newSymbolCache(okxClient),
		backtestCfg: DefaultBacktestRunnerConfig(), runPython: runPythonSubprocess,
	}
	if ag != nil {
		// Already configured at startup via an environment variable: the
		// specific provider/model isn't visible to the interface layer, so
		// only a generic notice can be shown. It doesn't belong to any
		// specific user -- it's the default shared by every user (see the
		// agentCache field's doc comment).
		s.envAgent = ag
		s.envAgentStatus = agentStatus{EnvConfigured: true}
	}
	return s, nil
}

// userAgent returns a given user's currently-effective Agent (possibly from
// the cache, from a database-saved configuration, or the team default
// configured via an environment variable); nil means the translation layer
// isn't ready for this user.
func (s *Server) userAgent(ctx context.Context, userID string) (*agent.Agent, agentStatus) {
	s.agentCacheMu.RLock()
	entry, cached := s.agentCache[userID]
	s.agentCacheMu.RUnlock()
	if cached {
		return entry.agent, entry.status
	}

	ag, status := s.loadUserAgentFromDB(ctx, userID)
	if ag == nil && s.envAgent != nil {
		ag, status = s.envAgent, s.envAgentStatus
	}
	s.agentCacheMu.Lock()
	s.agentCache[userID] = agentCacheEntry{agent: ag, status: status}
	s.agentCacheMu.Unlock()
	return ag, status
}

// setUserAgent atomically replaces a given user's currently-effective Agent
// and its display status, called by the settings page.
func (s *Server) setUserAgent(userID string, ag *agent.Agent, status agentStatus) {
	s.agentCacheMu.Lock()
	defer s.agentCacheMu.Unlock()
	s.agentCache[userID] = agentCacheEntry{agent: ag, status: status}
}

// setUserAgentError only records the reason this user's attempt failed; it
// leaves the currently-effective Agent untouched -- one attempt with a wrong
// key shouldn't knock out a translation layer that was already working.
func (s *Server) setUserAgentError(userID, errMsg string) {
	s.agentCacheMu.Lock()
	defer s.agentCacheMu.Unlock()
	entry := s.agentCache[userID]
	entry.status.LastErr = errMsg
	s.agentCache[userID] = entry
}

// invalidateUserAgent clears a given user's cache entry, so the next
// userAgent call re-resolves from the database -- called after saving/
// activating/deleting a model configuration, so the cache never holds a
// stale credential.
func (s *Server) invalidateUserAgent(userID string) {
	s.agentCacheMu.Lock()
	defer s.agentCacheMu.Unlock()
	delete(s.agentCache, userID)
}

// Routes builds the route table.
//
// Every route other than login itself is wrapped in requireAuth: this
// interface can see strategy details and change which LLM API key is bound,
// so a single unprotected endpoint would make the entire login requirement
// meaningless -- wrapping it centrally here means no individual handler has
// to remember to check. The exemption list is currently /login, /signup,
// /logout, /lang/{en,zh} (the language switch has to work before login too,
// see lang.go), plus the four static asset routes PWA needs (/manifest.json,
// /sw.js, two icons) -- the browser fetches these with no login state yet,
// see static.go.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", s.handleLoginShow)
	mux.HandleFunc("POST /login", s.handleLoginSubmit)
	mux.HandleFunc("GET /signup", s.handleSignupShow)
	mux.HandleFunc("POST /signup", s.handleSignupSubmit)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /lang/en", s.handleSetLang(i18n.LangEN))
	mux.HandleFunc("GET /lang/zh", s.handleSetLang(i18n.LangZH))
	mux.HandleFunc("GET /manifest.json", s.handleManifest)
	mux.HandleFunc("GET /sw.js", s.handleServiceWorker)
	mux.HandleFunc("GET /static/icon-192.png", s.handleIcon192)
	mux.HandleFunc("GET /static/icon-512.png", s.handleIcon512)
	mux.HandleFunc("GET /", s.requireAuth(s.handleDashboard))
	mux.HandleFunc("POST /strategies/bulk-delete", s.requireAuth(s.handleBulkDeleteStrategies))
	mux.HandleFunc("GET /wizard", s.requireAuth(s.handleWizardStart))
	mux.HandleFunc("POST /wizard/translate", s.requireAuth(s.handleWizardTranslate))
	mux.HandleFunc("POST /wizard/clarify", s.requireAuth(s.handleWizardClarify))
	mux.HandleFunc("POST /wizard/confirm", s.requireAuth(s.handleWizardConfirm))
	mux.HandleFunc("GET /wizard/batch", s.requireAuth(s.handleBatchScanStart))
	mux.HandleFunc("POST /wizard/batch/scan", s.requireAuth(s.handleBatchScanTranslate))
	mux.HandleFunc("POST /wizard/batch/clarify", s.requireAuth(s.handleBatchScanClarify))
	mux.HandleFunc("POST /wizard/batch/confirm", s.requireAuth(s.handleBatchScanConfirm))
	mux.HandleFunc("GET /strategies/{id}", s.requireAuth(s.handleStrategyDetail))
	mux.HandleFunc("POST /strategies/{id}/delete", s.requireAuth(s.handleDeleteStrategy))
	mux.HandleFunc("POST /strategies/{id}/run-backtest", s.requireAuth(s.handleRunBacktest))
	mux.HandleFunc("POST /strategies/{id}/confirm-backtest", s.requireAuth(s.handleConfirmBacktest))
	mux.HandleFunc("POST /strategies/{id}/start-paper-trading", s.requireAuth(s.handleStartPaperTrading))
	mux.HandleFunc("POST /strategies/{id}/unlock-live", s.requireAuth(s.handleUnlockLive))
	mux.HandleFunc("GET /settings", s.requireAuth(s.handleSettingsShow))
	mux.HandleFunc("POST /settings", s.requireAuth(s.handleSettingsSave))
	mux.HandleFunc("POST /settings/profiles/{id}/activate", s.requireAuth(s.handleSettingsActivateProfile))
	mux.HandleFunc("POST /settings/profiles/{id}/delete", s.requireAuth(s.handleSettingsDeleteProfile))
	mux.HandleFunc("POST /settings/brokers", s.requireAuth(s.handleSettingsSaveBroker))
	mux.HandleFunc("POST /settings/brokers/{id}/activate", s.requireAuth(s.handleSettingsActivateBrokerProfile))
	mux.HandleFunc("POST /settings/brokers/{id}/delete", s.requireAuth(s.handleSettingsDeleteBrokerProfile))
	mux.HandleFunc("POST /settings/notifications", s.requireAuth(s.handleSettingsSaveNotificationChannel))
	mux.HandleFunc("POST /settings/notifications/{id}/toggle", s.requireAuth(s.handleSettingsToggleNotificationChannel))
	mux.HandleFunc("POST /settings/notifications/{id}/delete", s.requireAuth(s.handleSettingsDeleteNotificationChannel))
	mux.HandleFunc("POST /settings/notifications/webpush-subscribe", s.requireAuth(s.handleSettingsWebPushSubscribe))
	mux.HandleFunc("GET /builder", s.requireAuth(s.handleBuilderList))
	mux.HandleFunc("GET /builder/{symbol}", s.requireAuth(s.handleBuilderView))
	mux.HandleFunc("POST /builder/describe", s.requireAuth(s.handleBuilderDescribe))
	mux.HandleFunc("POST /builder/{symbol}/translate", s.requireAuth(s.handleBuilderTranslate))
	mux.HandleFunc("GET /api/candles/{symbol}", s.requireAuth(s.handleAPICandles))
	mux.HandleFunc("GET /api/symbols", s.requireAuth(s.handleAPISymbolSearch))
	mux.HandleFunc("GET /api/modules", s.requireAuth(s.handleAPIModules))
	mux.HandleFunc("GET /api/preview/support-resistance/{symbol}", s.requireAuth(s.handleAPIPreviewSupportResistance))
	mux.HandleFunc("GET /api/preview/fakeout/{symbol}", s.requireAuth(s.handleAPIPreviewFakeout))
	mux.HandleFunc("GET /api/preview/poc/{symbol}", s.requireAuth(s.handleAPIPreviewPOC))
	mux.HandleFunc("GET /api/preview/trendline-pullback/{symbol}", s.requireAuth(s.handleAPIPreviewTrendlinePullback))
	return mux
}
