// Command signal-engine 是组合引擎的常驻服务入口。
//
// 它持续获取各策略标的的行情，调用组合引擎算出决策，落库审计后发布到 Kafka，
// 供执行层与回测消费。
//
// 用法：
//
//	go run ./cmd/signal-engine -candles testdata/btcusdt_1h.csv       # 重放本地 CSV
//	go run ./cmd/signal-engine -source okx -state PAPER_TRADING       # 多用户模式：接 OKX 实时行情，服务所有用户
//	go run ./cmd/signal-engine -source okx -owner-email you@example.com  # 单用户模式
//
// -source csv（默认）从本地 CSV 逐根重放，用于离线跑通整条链路；
// -source okx 启动时用 REST 回填一段历史窗口，随后订阅 WebSocket 持续喂实时收盘 K 线，
// 是真正意义上的"活"起来。选 OKX 而不是 CLAUDE.md 里默认提到的 Binance，是因为从常见的
// 云端开发环境出口 IP 访问 Binance 会被地区限制拒绝（451），OKX 没有这个问题；
// 细节见 internal/marketdata/okx 包注释。
//
// 多用户并发执行（第二阶段）：-owner-email 现在是可选的，语义跟 cmd/executor 的
// -owner-email 完全一样——留空 = 多用户模式，一个进程同时为所有用户计算决策；显式指定
// = 单用户模式，行为跟改造前完全一致。多用户模式下天然省掉重复拉行情的成本：不同用户
// 交易同一个标的同一个周期时，live.go 的 candleCache 按 (symbol, timeframe) 去重，
// 本来就没有用户维度，两个用户的策略会自然共享同一路 REST 回填 + WebSocket 订阅。
//
// -source okx 路径下每 -rescan-interval 一个周期会重新扫描一次策略列表，给全新出现的
// (symbol, timeframe) 组合起一路新订阅——这只解决"新用户在一个还没人订阅的标的/周期上
// 建了策略"这种情况；"新用户的策略刚好落在一个已经有人在订阅的 (symbol, timeframe) 上"
// 这种情况这一轮不解决，仍然需要重启进程才能生效（live.go 里每路订阅的 triggerStrategies
// 是启动时固定传入的闭包参数，不是能事后追加的共享结构，深入支持这个需要更大的改动）。
// -source csv 是离线一次性重放，不需要重新扫描的概念，天然不适用。
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
		"单用户模式：只服务这一个用户（可选，该用户的邮箱）；留空则是多用户模式，服务所有用户——"+
			"跟 cmd/executor 的 -owner-email 是同一个边界，见那边 main.go 顶部的注释")
	source := flag.String("source", "csv", "行情来源：csv（重放本地文件）或 okx（实时行情）")
	candlesPath := flag.String("candles", "", "行情 CSV 路径（-source csv 时必填）")
	stateFlag := flag.String("state", string(types.StatePaperTrading),
		"为处于该状态的策略计算决策")
	interval := flag.Duration("interval", 200*time.Millisecond, "重放每根 K 线的间隔（仅 -source csv）")
	window := flag.Int("window", 1200, "喂给模块的最大历史 K 线根数")
	backfill := flag.Int("backfill", 300, "启动时用 REST 回填的历史 K 线根数（仅 -source okx，"+
		"OKX 单次请求上限就是 300）")
	rescanInterval := flag.Duration("rescan-interval", 5*time.Minute,
		"多久重新扫描一次策略列表、给全新出现的 (symbol, timeframe) 组合起订阅（仅 -source okx；"+
			"已经在订阅中的组合追加新用户的策略仍需要重启才能生效，见顶部注释）")
	flag.Parse()

	if *source == "csv" && *candlesPath == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *source != "csv" && *source != "okx" {
		fatal("-source 只支持 csv 或 okx，实际 %q", *source)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		fatal("连接数据库失败：%v", err)
	}
	defer store.Close()

	if err := messaging.EnsureTopics(ctx, cfg.Kafka); err != nil {
		fatal("创建 Kafka topic 失败：%v", err)
	}
	pub := messaging.NewKafkaPublisher(cfg.Kafka)
	defer pub.Close()

	// 留空 -owner-email = 多用户模式：ownerUserID 保持空字符串，loadStrategies
	// 按这个空字符串分支到"跨全部用户查询"。
	ownerUserID := ""
	if trimmed := strings.TrimSpace(*ownerEmail); trimmed != "" {
		owner, err := store.GetUserByEmail(ctx, trimmed)
		if err != nil {
			fatal("找不到 -owner-email 指定的用户 %q：%v", trimmed, err)
		}
		ownerUserID = owner.ID
	}
	multiTenant := ownerUserID == ""

	state := types.StrategyState(*stateFlag)
	strategies, err := loadStrategies(ctx, store, ownerUserID, state)
	if err != nil {
		fatal("加载策略失败：%v", err)
	}
	if len(strategies) == 0 {
		// 不再 fatal：多用户模式下"暂时没有用户在这个状态下有策略"是正常的瞬态
		// （配合下面的定期重新扫描，后面随时可能出现），不是需要人工介入的错误；
		// 单用户模式下也没必要因为用户还没建好第一条策略就让进程直接退出。
		logger.Warn("没有找到匹配的策略，暂时空转", "multi_tenant", multiTenant, "state", state)
	}

	e := engine.New(modules.NewDefaultRegistry(),
		engine.WithAuditor(store),
		engine.WithPublisher(pub),
		engine.WithTimeout(cfg.Engine.ModuleTimeout),
		engine.WithLogger(logger),
	)

	for _, s := range strategies {
		logger.Info("已加载策略",
			"strategy_id", s.ID, "symbol", s.Symbol, "name", s.Name, "combine", s.Combine)
	}

	if *source == "okx" {
		client := okx.NewClient()
		logger.Info("使用 OKX 实时行情", "backfill", *backfill, "window", *window, "multi_tenant", multiTenant)
		reload := func(ctx context.Context) ([]types.StrategyConfig, error) {
			return loadStrategies(ctx, store, ownerUserID, state)
		}
		runLive(ctx, e, strategies, client, client, *backfill, *window, logger, reload, *rescanInterval)
		logger.Info("已停止")
		return
	}

	feeds := loadCSVFeeds(*candlesPath, strategies, logger)
	replay(ctx, e, strategies, feeds, *window, *interval, logger)
	logger.Info("重放结束")
}

// strategyLister 是 loadStrategies 所需的最小持久化接口，只为了让调用方不需要真实
// Postgres 就能单元测试。*storage.Store 结构性满足这个接口。
type strategyLister interface {
	ListStrategiesByState(ctx context.Context, userID string, state types.StrategyState) ([]types.StrategyConfig, error)
	ListStrategiesByStateAllUsers(ctx context.Context, state types.StrategyState) ([]types.StrategyConfig, error)
}

// loadStrategies 按 ownerUserID 是否为空分支查询：非空 = 单用户模式，只查这一个用户的；
// 空 = 多用户模式，跨全部用户查——跟 cmd/executor 用的是同一个分支模式。
func loadStrategies(ctx context.Context, store strategyLister, ownerUserID string, state types.StrategyState) ([]types.StrategyConfig, error) {
	if ownerUserID != "" {
		return store.ListStrategiesByState(ctx, ownerUserID, state)
	}
	return store.ListStrategiesByStateAllUsers(ctx, state)
}

// loadCSVFeeds 按 (symbol, timeframe) 去重加载 CSV 行情。
//
// 按组合去重而不是只按 symbol：如果两个策略共用同一个标的但周期不同
// （比如同一个 BTCUSDT 一个跑 1h 一个跑 4h），只按 symbol 去重会让第二个
// 悄悄读到第一个的行情，两边周期对不上却不会报错——这是比"直接漏掉"更危险的错法。
//
// 多周期策略需要的每个周期（触发周期 + 各模块声明的背景周期，见
// StrategyConfig.RequiredTimeframes）都要展开成各自的 key，不再只按策略自身的
// 触发周期加载一份——否则背景周期的模块在这条 CSV 重放路径下永远拿不到数据、
// 全程降级。
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
				logger.Error("加载行情失败，跳过该周期", "symbol", s.Symbol, "timeframe", tf, "err", err)
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
					continue // 缺失的背景周期让引擎自己按模块降级处理，这里不特殊拦截
				}
				aligned := types.AlignAsOf(ctxMD.Candles, cutoff)
				if window > 0 && len(aligned) > window {
					aligned = aligned[len(aligned)-window:]
				}
				strategyFeeds[tf] = types.MarketData{Symbol: ctxMD.Symbol, Timeframe: tf, Candles: aligned}
			}

			d, err := e.Process(ctx, s, strategyFeeds)
			if err != nil {
				// 单个策略出错不影响其它策略继续计算。
				logger.Error("计算决策失败", "strategy_id", s.ID, "symbol", s.Symbol, "err", err)
				continue
			}
			if d.Triggered {
				logger.Info("决策触发",
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
