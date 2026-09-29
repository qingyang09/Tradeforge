// Package webui 实现阶段 7 的最小可用界面：策略配置向导、状态看板、策略详情。
//
// 这一层只负责展示已有的后端能力，不重新实现任何业务规则——翻译交给
// internal/agent，状态流转交给 internal/strategy，数据读写交给 internal/storage。
// 界面本身也要守住合规红线：任何文案都不得包含"建议""推荐"等投资建议措辞，
// 只做事实性描述。
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

// uuidPattern 粗略校验路径参数是不是 UUID 形状。
//
// strategies.id 在 Postgres 里是 UUID 列：查询一个格式明显不对的字符串（比如浏览器
// 里手滑打错的路径）会让驱动直接报"invalid input syntax for type uuid"，这个错误
// 不是 pgx.ErrNoRows，Store 也就不会把它映射成 ErrNotFound——不在这里提前拦掉，
// 一个格式错误的 ID 会被当成服务器内部错误返回 500，而它本该是最普通的 404。
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func looksLikeUUID(s string) bool { return uuidPattern.MatchString(s) }

// Store 是界面所需的最小持久化接口。
//
// 用接口而不是直接依赖 *storage.Store，是为了让 handler 的核心逻辑不需要真实
// Postgres 就能单元测试——这是 cmd/executor 的 promotionStore 已经验证过的模式。
// *storage.Store 结构性满足这个接口，不需要任何额外代码。
//
// 多用户 SaaS 改造：除了 SaveStrategy/RecordTransition/SaveAgentProfile/
// SaveBrokerProfile（写入的数据自带归属，不需要额外传）之外，几乎每个方法都加了
// userID 参数——数据隔离靠数据库行级过滤，不是靠界面自己判断"这条是不是你的"再决定
// 要不要显示，那样一旦某处漏判就是真实的越权读取。
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
	// ActiveAgentProfile 支持设置页面持久化保存多份 LLM 供应商配置（见
	// handlers_settings.go）。API key 在存进来之前已经由调用方加密——这一层只管
	// 存取密文，不接触明文 key 或加密逻辑。
	SaveAgentProfile(ctx context.Context, p storage.AgentProfile, activate bool) error
	GetAgentProfile(ctx context.Context, userID, id string) (storage.AgentProfile, error)
	ListAgentProfiles(ctx context.Context, userID string) ([]storage.AgentProfile, error)
	ActivateAgentProfile(ctx context.Context, userID, id string) error
	DeleteAgentProfile(ctx context.Context, userID, id string) error
	ActiveAgentProfile(ctx context.Context, userID string) (storage.AgentProfile, error)

	// SaveBrokerProfile/GetBrokerProfile/ListBrokerProfiles/ActivateBrokerProfile/
	// DeleteBrokerProfile 是 BrokerProfile 版本的同一套持久化模式，供设置页面保存
	// 交易所下单通道的凭据（见 handlers_settings_brokers.go）。跟 AgentProfile 的区别是
	// "当前生效"按"用户+broker"分组，不是按用户全局唯一——见 storage.BrokerProfile
	// 的注释。
	SaveBrokerProfile(ctx context.Context, p storage.BrokerProfile, activate bool) error
	GetBrokerProfile(ctx context.Context, userID, id string) (storage.BrokerProfile, error)
	ListBrokerProfiles(ctx context.Context, userID string) ([]storage.BrokerProfile, error)
	ActivateBrokerProfile(ctx context.Context, userID, id string) error
	DeleteBrokerProfile(ctx context.Context, userID, id string) error

	// CreateUser/GetUserByEmail/GetUser 支持账户注册登录（见 handlers_auth.go）。
	CreateUser(ctx context.Context, u storage.User) error
	GetUserByEmail(ctx context.Context, email string) (storage.User, error)
	GetUser(ctx context.Context, id string) (storage.User, error)

	// SaveNotificationChannel/ListNotificationChannels/GetNotificationChannel/
	// SetNotificationChannelEnabled/DeleteNotificationChannel 支持设置页面持久化保存
	// 提醒渠道配置（见 handlers_settings_notifications.go）。跟 AgentProfile/BrokerProfile
	// 的关键差异：没有"当前生效唯一一份"的语义，任意数量的渠道可以同时 is_enabled=true，
	// 见 storage.NotificationChannel 的注释。cmd/notifier 单独通过自己的 notifierStore
	// 接口访问同一张表，不复用这个 Store 接口——绝不能把 postgres.go 里带
	// "SECURITY: cross-user query" 注释的那类跨用户方法加进这里，security_guard_test.go
	// 会挡下来。
	SaveNotificationChannel(ctx context.Context, c storage.NotificationChannel) error
	ListNotificationChannels(ctx context.Context, userID string) ([]storage.NotificationChannel, error)
	GetNotificationChannel(ctx context.Context, userID, id string) (storage.NotificationChannel, error)
	SetNotificationChannelEnabled(ctx context.Context, userID, id string, enabled bool) error
	DeleteNotificationChannel(ctx context.Context, userID, id string) error
}

// agentStatus 是当前生效的 Agent 配置，只用于设置页面展示，不参与翻译逻辑。
type agentStatus struct {
	// Provider 为空表示 Agent 是通过环境变量（启动参数）配置的，不是通过设置页面——
	// 这种情况下我们不知道具体供应商/模型，只能显示"已配置"。
	Provider agent.Provider
	Model    string
	// KeyHint 是掩码后的 key（如 "sk-…ab12"），从不展示完整 key。
	KeyHint string
	// LastErr 是最近一次尝试更新配置失败的原因；不影响当前仍在生效的 Agent。
	LastErr string
	// ProfileID 非空时表示当前生效的配置是从 agent_profiles 表里的这一份加载/激活的
	// （见 handlers_settings.go）。为空表示当前配置只在内存里生效——要么是通过环境变量
	// 启动的，要么是没有登录密码/加密失败时的降级结果，删除数据库里的某份配置时用它
	// 判断"删掉的是不是正在用的这份"。
	ProfileID string
}

func (s agentStatus) Configured() bool { return s.KeyHint != "" }

// agentCacheEntry 缓存某个用户当前解密/构造好的 Agent，避免每次请求都重新做一次
// scrypt 密钥派生（见 internal/secretcrypto 的文档注释：单次几十毫秒，多用户环境下
// 每次页面加载都付一次这个代价是真实的性能回退）。agent 为 nil 表示"已经确认过这个
// 用户没有可用配置"（可能是没配、也可能是上次配置失败），跟"还没查过"用是否存在于
// map 里区分——这样即使没配置的用户也只会真正查一次数据库，不会每次请求都打一次库。
type agentCacheEntry struct {
	agent  *agent.Agent
	status agentStatus
}

// Server 持有界面运行所需的全部依赖。
type Server struct {
	store Store

	// agentCache 按用户缓存已经解密/构造好的 Agent（见 agentCacheEntry）。
	// envAgent/envAgentStatus 是启动时通过环境变量配置的一份（cmd/webui/main.go 的
	// ANTHROPIC_API_KEY 路径），不属于任何具体用户——当某个用户自己在 /settings
	// 没配置任何模型时，退回这一份当团队共享的默认值，兼容原来单操作者场景下
	// "用环境变量启动就能用"的行为。
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

	// registry 独立于 agent 存在：校验一份 StrategyConfig（strategy.Validate）只需要
	// 模块注册表，不需要 LLM。可视化建策（handlers_builder.go）和 handleWizardConfirm
	// 都靠它在没有配置 LLM key 时依然能校验并保存策略——之前 handleWizardConfirm 是
	// 通过 ag.Confirm 间接拿到 registry 的，导致这个本不需要 LLM 的步骤被 agent 是否
	// 就绪卡住。
	registry *modules.Registry
	// okxClient 用于给可视化建策的画板页提供只读的 K 线数据（GET /api/candles）。
	// OKX 的行情接口公开只读、不需要 API key，构造不依赖任何配置，
	// 跟 cmd/signal-engine 里的用法一致。
	okxClient *okx.Client
	// symbolCache 给"输入标的"的自动补全提供候选（GET /api/symbols），懒加载 OKX
	// 现货标的全集并缓存，见 symbols.go。
	symbolCache *symbolCache

	// masterKey 是给 internal/secretcrypto 加解密所有用户的 LLM/交易所凭据用的
	// 服务端主密钥（TF_MASTER_KEY）——不是任何用户的登录密码，登录鉴权完全靠
	// sessions/bcrypt，见 auth.go。cmd/webui/main.go 的生产启动路径要求它必须设置，
	// 只有测试会留空。
	masterKey string
	sessions  *sessionStore

	// vapidPublicKey 是 web push 订阅时浏览器端需要的公钥（RFC 8292），不是秘密——
	// 必须能到达浏览器 JS 才能调用 PushManager.subscribe()。私钥只存在于
	// cmd/notifier 的进程里（internal/notify.VAPIDWebPushSender），webui 从不接触它。
	vapidPublicKey string

	// backtestCfg/runPython 支持"运行回测"按钮（handlers_backtest.go）：
	// 拉真实历史数据、在内存里跑一遍信号重放、再拉起 Python 子进程做撮合与落库。
	// runPython 默认是真的起子进程，测试替换成假实现，不需要真的装 Python
	// 就能测拉数据/写临时文件/组装参数/处理错误这些编排逻辑。
	backtestCfg BacktestRunnerConfig
	runPython   func(ctx context.Context, args []string, dir string) (stdout, stderr []byte, err error)
}

// SetMasterKey 设置加解密全部用户凭据用的服务端主密钥。cmd/webui/main.go 的生产
// 启动路径总会调用它（TF_MASTER_KEY 未设置时直接拒绝启动，见该文件），只有测试会
// 跳过这一步。
func (s *Server) SetMasterKey(key string) {
	s.masterKey = key
}

// SetVAPIDPublicKey 设置 web push 订阅用的 VAPID 公钥（见 vapidPublicKey 字段注释）。
// cmd/webui/main.go 会在启动时用 cfg.Notification.WebPush.VAPIDPublicKey 调用它；
// 留空只是让设置页面的"启用推送"按钮点击后失败，不影响其它功能，不需要拒绝启动。
func (s *Server) SetVAPIDPublicKey(key string) {
	s.vapidPublicKey = key
}

// New 构造 Server 并解析全部模板。模板解析失败是启动期错误，不是运行期错误。
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
		// 启动时已经通过环境变量配置好了：具体供应商/模型对界面不可见，
		// 只能给一个通用提示。不属于任何具体用户，是所有用户共享的默认值
		// （见 agentCache 字段的注释）。
		s.envAgent = ag
		s.envAgentStatus = agentStatus{KeyHint: "（通过环境变量配置）"}
	}
	return s, nil
}

// userAgent 返回某个用户当前生效的 Agent（可能来自缓存、数据库里保存的配置，或者
// 环境变量配置的团队默认值），nil 表示翻译层对这个用户未就绪。
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

// setUserAgent 原子地替换某个用户当前生效的 Agent 与展示状态，供设置页面调用。
func (s *Server) setUserAgent(userID string, ag *agent.Agent, status agentStatus) {
	s.agentCacheMu.Lock()
	defer s.agentCacheMu.Unlock()
	s.agentCache[userID] = agentCacheEntry{agent: ag, status: status}
}

// setUserAgentError 只记录该用户一次失败尝试的原因，不动当前仍在生效的 Agent——
// 一次填错 key 的尝试不该把已经工作正常的翻译层顶掉。
func (s *Server) setUserAgentError(userID, errMsg string) {
	s.agentCacheMu.Lock()
	defer s.agentCacheMu.Unlock()
	entry := s.agentCache[userID]
	entry.status.LastErr = errMsg
	s.agentCache[userID] = entry
}

// invalidateUserAgent 清掉某个用户的缓存条目，下次 userAgent 会重新从数据库解析——
// 保存/激活/删除模型配置后调用，避免缓存里存着一份已经过期的凭据。
func (s *Server) invalidateUserAgent(userID string) {
	s.agentCacheMu.Lock()
	defer s.agentCacheMu.Unlock()
	delete(s.agentCache, userID)
}

// Routes 构造路由表。
//
// 除登录本身外的所有路由都套一层 requireAuth：这个界面能看到策略细节、能改绑
// LLM API key，任何一个端点漏保护都等于整个登录形同虚设，所以在这里统一包裹，
// 不指望每个 handler 自己记得检查。豁免名单目前是 /login、/signup、/logout、
// /lang/{en,zh}（语言切换必须在登录前也能用，见 lang.go），加上 PWA 需要的四个
// 静态资源路由（/manifest.json、/sw.js、两个图标）——浏览器拉取这些文件时还没有
// 登录态，见 static.go。
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
	return mux
}
