// Command executor 是多标的执行层的入口。
//
// 它从 Kafka 消费组合引擎产出的决策，按策略 ID 路由到各自的执行实例。
// 每个标的一个 worker、一条队列，互相隔离。
//
// 用法：
//
//	go run ./cmd/executor                                                  # 多用户模式：跑这个 broker 通道下所有用户的 PAPER_TRADING 策略
//	go run ./cmd/executor -state LIVE                                      # 多用户模式：跑所有 LIVE 状态的策略
//	go run ./cmd/executor -broker okx-demo                                 # 多用户模式：跑所有在 OKX demo 上配了凭据的用户
//	go run ./cmd/executor -owner-email you@example.com                     # 单用户模式：只跑这一个用户（独立进程，兼容旧行为）
//
// 多用户并发执行（第二阶段）：-owner-email 现在是可选的。留空 = 多用户模式——这个进程
// 服务 -broker 指定的这个交易所通道下所有用户，每个用户的策略用他自己在数据库里保存的
// 凭据执行（内存缓存 + 定时刷新，见 brokerCache），互不影响；不同用户交易同一个标的时，
// 底层行情/风控本来就是按策略 ID 隔离的，天然安全。显式指定 -owner-email = 单用户模式，
// 完全保留改造前的行为（含下面提到的环境变量凭据回退）——留给需要独立进程、独享资源的
// 大客户，不是纯粹的兼容包袱。
//
// 多用户模式下环境变量凭据回退被完全禁用（见 loadBrokerCredentials 的 multiTenant
// 参数）：如果不禁用，部署环境里设的 TF_OKX_API_KEY 等变量会被这个通道下所有用户共用，
// 等于所有人的真实资金都打到同一个交易所账户——这是资金串号级别的问题，不是普通 bug，
// 只有单用户模式（-owner-email 显式指定）才允许用环境变量。
//
// 每 -promotion-interval 一个周期，这个进程会重新扫描一次策略列表：新出现的策略自动
// 注册、离开目标状态的策略自动摘除——不需要重启进程就能让新用户/新策略生效，重启会打断
// 这个进程上所有其它正在跑的用户，多用户共享一个进程后应该尽量避免。broker 凭据缓存的
// 有效期也复用这同一个间隔：新注册的策略最多等一个周期就能用上新配置的凭据；但已经在跑
// 的策略换了凭据不会热更新，仍然需要重启（或等它被摘除重新注册）才能生效。
//
// 安全约定：默认只使用纯内存模拟盘通道（paper）。接交易所需要显式指定 -broker，
// 且当前版本每一家都只允许打到测试网/模拟盘。单用户模式下凭据可以来自环境变量
// （binance-testnet 需要 TF_BINANCE_API_KEY / TF_BINANCE_API_SECRET；okx-demo 需要
// TF_OKX_API_KEY / TF_OKX_API_SECRET / TF_OKX_PASSPHRASE；bybit-testnet 需要
// TF_BYBIT_API_KEY / TF_BYBIT_API_SECRET；bitget-demo 需要 TF_BITGET_API_KEY /
// TF_BITGET_API_SECRET / TF_BITGET_PASSPHRASE），环境变量没配全时会退回读取该用户
// 在 Web 界面设置页面里保存的凭据（需要同一个 TF_MASTER_KEY 才能解密——见
// internal/webui/handlers_settings_brokers.go）。多用户模式下只走数据库这一条路径。
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
		"单用户模式：只服务这一个用户（可选，该用户的邮箱）；留空则是多用户模式，服务 -broker 指定通道下的所有用户")
	stateFlag := flag.String("state", string(types.StatePaperTrading),
		"加载处于该状态的策略（PAPER_TRADING / LIVE_ELIGIBLE / LIVE）")
	brokerFlag := flag.String("broker", "paper",
		"下单通道：paper（纯内存模拟盘，默认）/ binance-testnet / okx-demo / bybit-testnet / bitget-demo")
	group := flag.String("group", "tradeforge-executor", "Kafka 消费组 ID")
	promotionInterval := flag.Duration("promotion-interval", 5*time.Minute,
		"检查模拟盘策略是否达标、重新扫描策略列表（新增/摘除）、刷新 broker 凭据缓存共用的间隔")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	state := types.StrategyState(*stateFlag)
	if !state.Valid() {
		fatal("状态 %q 不合法", *stateFlag)
	}
	if state == types.StateDraft || state == types.StateBacktested {
		fatal("状态 %s 的策略不允许产生任何交易", state)
	}

	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		fatal("连接数据库失败：%v", err)
	}
	defer store.Close()

	// 留空 -owner-email = 多用户模式：ownerUserID 保持空字符串，下游所有分支
	// （加载策略、解析凭据）都按"空字符串=跨全部用户"来处理。
	ownerUserID := ""
	if trimmed := strings.TrimSpace(*ownerEmail); trimmed != "" {
		owner, err := store.GetUserByEmail(ctx, trimmed)
		if err != nil {
			fatal("找不到 -owner-email 指定的用户 %q：%v", trimmed, err)
		}
		ownerUserID = owner.ID
	}
	multiTenant := ownerUserID == ""

	brokers := newBrokerCache(store, cfg.Security.MasterKey, *brokerFlag, multiTenant, *promotionInterval)

	sup := execution.NewSupervisor(logger)
	defer sup.Shutdown()

	reconcileRegistrations(ctx, store, sup, brokers, ownerUserID, state, logger)
	if len(sup.StrategyIDs()) == 0 {
		logger.Warn("没有找到匹配的策略，执行层空转", "multi_tenant", multiTenant, "state", state)
	}

	if err := messaging.EnsureTopics(ctx, cfg.Kafka); err != nil {
		logger.Warn("创建 Kafka topic 失败，将依赖自动创建", "err", err)
	}
	reader := messaging.NewDecisionReader(cfg.Kafka, *group)
	defer reader.Close()

	go runPromotionLoop(ctx, store, ownerUserID, strategy.DefaultGate(), logger, *promotionInterval)
	go runReconcileLoop(ctx, store, sup, brokers, ownerUserID, state, logger, *promotionInterval)

	logger.Info("开始消费决策",
		"topic", cfg.Kafka.DecisionTopic, "group", *group, "multi_tenant", multiTenant, "broker", *brokerFlag)
	consume(ctx, reader, sup, logger)

	logger.Info("收到停止信号，正在关闭执行层")
	printSummary(sup, logger)
}

// executorStore 是 cmd/executor 运行时依赖的全部持久化能力——用接口而不是直接依赖
// *storage.Store，是为了让 registerOne/reconcileRegistrations/brokerCache 这条链路
// 不需要真实 Postgres 就能单元测试，跟 brokerCredentialStore/promotionStore 是同一个
// 模式。*storage.Store 结构性满足这个接口，不需要任何额外代码。
type executorStore interface {
	brokerCredentialStore
	execution.OrderRecorder
	execution.RiskEventRecorder
	ListStrategiesByState(ctx context.Context, userID string, state types.StrategyState) ([]types.StrategyConfig, error)
	ListStrategiesByStateAllUsers(ctx context.Context, state types.StrategyState) ([]types.StrategyConfig, error)
}

// loadStrategies 按 ownerUserID 是否为空分支查询：非空 = 单用户模式，只查这一个用户的；
// 空 = 多用户模式，跨全部用户查——跟 checkPromotions（promote.go）用的是同一个分支模式。
func loadStrategies(ctx context.Context, store executorStore, ownerUserID string, state types.StrategyState) ([]types.StrategyConfig, error) {
	if ownerUserID != "" {
		return store.ListStrategiesByState(ctx, ownerUserID, state)
	}
	return store.ListStrategiesByStateAllUsers(ctx, state)
}

// reconcileRegistrations 把 sup 当前注册的策略集合与数据库最新状态做一次差集：新出现
// 的策略注册进去，不在新列表里的（被删除、或状态已经流转出这个进程关心的范围）摘除。
//
// 多用户共享一个进程之后，这不是锦上添花：如果没有这一步，"一个用户的新策略要生效，
// 得重启整个进程"，而重启会打断这个进程上所有其它用户正在跑的策略（内存里的
// RiskManager/Stats 状态全部丢失）——这跟"合并成一个进程服务所有人"想要的效果正好相反。
func reconcileRegistrations(
	ctx context.Context, store executorStore, sup *execution.Supervisor, brokers *brokerCache,
	ownerUserID string, state types.StrategyState, logger *slog.Logger,
) {
	strategies, err := loadStrategies(ctx, store, ownerUserID, state)
	if err != nil {
		logger.Error("重新扫描策略列表失败，保留现有注册不变", "err", err)
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
			logger.Error("摘除已离开目标状态的策略失败", "strategy_id", id, "err", err)
			continue
		}
		logger.Info("已停止执行实例（策略已离开目标状态）", "strategy_id", id)
	}
}

// registerOne 给单个策略解析它自己归属用户的 broker、注册进 Supervisor。凭据解析失败
// 或注册失败都只跳过这一条策略，不影响同一批次里其它用户/其它策略——隔离原则从这里
// 就开始生效。errors.Is(err, execution.ErrAlreadyRegistered) 是正常情况（上一轮扫描
// 已经注册过了），静默跳过，不当错误处理。
func registerOne(ctx context.Context, store executorStore, sup *execution.Supervisor, brokers *brokerCache, s types.StrategyConfig, logger *slog.Logger) {
	br, err := brokers.get(ctx, s.UserID)
	if err != nil {
		logger.Error("解析用户交易所凭据失败，已跳过该策略",
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
		logger.Error("策略注册失败，已跳过",
			"strategy_id", s.ID, "symbol", s.Symbol, "err", err)
		return
	}
	logger.Info("已启动执行实例",
		"strategy_id", s.ID, "user_id", s.UserID, "symbol", s.Symbol, "name", s.Name,
		"modules", len(s.Modules), "combine", s.Combine)
}

// runReconcileLoop 按固定间隔重复调用 reconcileRegistrations，直到 ctx 被取消。
// 跟 runPromotionLoop 复用同一个 -promotion-interval 值，但各自独立的 ticker——不需要
// 精确同步到同一个时刻，只是不新增第二个"多久刷新一次"的心智负担。
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
			logger.Error("读取决策失败，稍后重试", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}

		if err := sup.Dispatch(d); err != nil {
			// 未注册的策略是常态：同一个 topic 上会有其它状态的策略的决策。
			if !errors.Is(err, execution.ErrUnknownStrategy) {
				logger.Warn("投递决策失败", "strategy_id", d.StrategyID, "err", err)
			}
		}
	}
}

// brokerCredentialStore 是 buildBroker 所需的最小持久化接口，只为了让它不需要真实
// Postgres 就能单元测试——跟 promote.go 的 promotionStore 是同一个模式。
// *storage.Store 结构性满足这个接口，不需要任何额外代码。
type brokerCredentialStore interface {
	ActiveBrokerProfile(ctx context.Context, userID, broker string) (storage.BrokerProfile, error)
}

// brokerCacheEntry 是 brokerCache 里的一条缓存。
type brokerCacheEntry struct {
	broker    execution.Broker
	fetchedAt time.Time
}

// brokerCache 按用户缓存已构造好的下单通道实例——同一个用户的凭据解密/构造有实打实的
// 代价，不该每次注册策略都重新做一遍。结构照抄 internal/webui/server.go 的 agentCache
// 模式：RWMutex 保护的 map，缓存命中优先读，未命中/过期才现建。
//
// key 只用 userID，不带 broker 种类：-broker 在一个进程的生命周期里是常量，不需要再
// 拿来当缓存 key 的一个维度。
//
// 跟 agentCache 的关键差异，是刻意的：agentCache 会缓存"确认没配置"的空结果，因为它在
// 每个 HTTP 请求的热路径上，重复查库代价是真实的；brokerCache 只在策略注册/定期重新
// 扫描时才会被访问（分钟级频率，不是每秒级），缓存失败结果的唯一效果是让"用户刚配好
// 交易所凭据，等着已有的 PAPER_TRADING 策略在下个扫描周期自动跑起来"这个多用户 SaaS
// 的核心体验失效——不值得。这里只缓存成功，失败永远重试。
//
// 成功结果给一个 TTL，过期只影响下一次全新的 get 调用（也就是新注册的策略），不会给
// 已经在跑的 Worker 热替换凭据——Worker 的 broker 是构造时固定的私有字段，没有 setter。
// 要让一个已经在跑的策略换用新凭据，仍然需要它先被摘除再重新注册（见 main.go 顶部注释）。
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

// buildBroker 按 -broker 选中的通道构造下单实例。单用户模式（multiTenant=false）下
// 凭据优先从环境变量读取（历史行为，脚本化部署不用碰数据库），环境变量没配全时退回
// 读取该用户在 Web 界面设置页面里保存的、当前生效的一份；多用户模式（multiTenant=true）
// 下环境变量回退被完全禁用，只走数据库——见 loadBrokerCredentials 顶部注释，这不是
// 简化，是防止多用户共享一份环境变量凭据导致资金串号的必要限制。
func buildBroker(ctx context.Context, store brokerCredentialStore, masterKey, ownerUserID, name string, multiTenant bool) (execution.Broker, error) {
	kind := execution.BrokerKind(name)
	if kind == "" {
		kind = execution.BrokerKindPaper
	}
	if !kind.Valid() {
		return nil, fmt.Errorf("未知的下单通道 %q（可选：paper / binance-testnet / okx-demo / bybit-testnet / bitget-demo）", name)
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

// envVarsForBroker 是各下单通道对应的环境变量名——保留跟此前版本相同的变量名，
// 不因为新增了数据库存储路径就破坏已有的脚本化部署方式。
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

// loadBrokerCredentials 单用户模式（multiTenant=false）下优先用环境变量，缺失时退回
// 数据库里该用户当前生效的一份配置（需要 TF_MASTER_KEY 才能解密，是加密全部用户凭据的
// 服务端主密钥，不是任何人的登录密码——见 internal/webui/handlers_settings_brokers.go）。
//
// 多用户模式（multiTenant=true）下完全跳过环境变量检查，直接走这个用户自己在数据库里
// 的凭据——环境变量是整个进程共享的一份，多用户模式下用它会让这个 broker 通道下所有
// 用户的策略全部用同一份凭据下单，是资金串号级别的问题，不是可以放行的历史兼容行为。
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
			return "", "", "", fmt.Errorf("%s 缺少凭据：没有 TF_MASTER_KEY，无法读取用户已保存的配置", kind)
		}
		return "", "", "", fmt.Errorf(
			"%s 缺少凭据：环境变量 %s/%s 未配置，且没有 TF_MASTER_KEY 无法读取已保存的配置",
			kind, keyVar, secretVar)
	}
	profile, err := store.ActiveBrokerProfile(ctx, ownerUserID, string(kind))
	if err != nil {
		if multiTenant {
			return "", "", "", fmt.Errorf(
				"%s 缺少凭据：该用户在数据库里没有当前生效的配置（请先在设置页面保存一份）：%w", kind, err)
		}
		return "", "", "", fmt.Errorf(
			"%s 缺少凭据：环境变量未配置，数据库里也没有该用户当前生效的配置（请先在设置页面保存一份）：%w",
			kind, err)
	}
	plaintext, err := secretcrypto.Decrypt(masterKey, profile.EncryptedCredentials, profile.KeySalt, profile.KeyNonce)
	if err != nil {
		return "", "", "", fmt.Errorf("解密 %s 已保存的配置失败（TF_MASTER_KEY 是否跟保存时一致）：%w", kind, err)
	}
	var creds struct {
		APIKey     string `json:"api_key"`
		APISecret  string `json:"api_secret"`
		Passphrase string `json:"passphrase"`
	}
	if err := json.Unmarshal([]byte(plaintext), &creds); err != nil {
		return "", "", "", fmt.Errorf("解析 %s 已保存的配置失败：%w", kind, err)
	}
	return creds.APIKey, creds.APISecret, creds.Passphrase, nil
}

func printSummary(sup *execution.Supervisor, logger *slog.Logger) {
	for _, ss := range sup.StatsByStrategy() {
		st := ss.Stats
		logger.Info("执行实例统计",
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
