package main

import (
	"context"
	"log/slog"
	"time"

	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// promotionActor 标识自动推进检查的操作者，写入审计日志的 actor 字段。
const promotionActor = "paper-monitor"

// promotionStore 是自动推进模拟盘策略所需的最小持久化接口。
//
// 用接口而不是直接依赖 *storage.Store，是为了让 checkPromotions 的核心逻辑
// 不需要真实 Postgres 就能单元测试——storage.Store 本身只在需要真库的集成测试里验证。
type promotionStore interface {
	ListStrategiesByState(ctx context.Context, userID string, state types.StrategyState) ([]types.StrategyConfig, error)
	ListStrategiesByStateAllUsers(ctx context.Context, state types.StrategyState) ([]types.StrategyConfig, error)
	PaperStats(ctx context.Context, userID, strategyID string) (strategy.PaperStats, error)
	UpdateStrategyState(ctx context.Context, userID string, t storage.Transition) error
}

// checkPromotions 扫描 PAPER_TRADING 状态的策略，把模拟盘统计已经达标的推进到
// LIVE_ELIGIBLE。ownerUserID 非空 = 单用户模式，只扫这一个用户的；为空 = 多用户模式，
// 跨全部用户扫——注意这个扫描范围是独立于这个 executor 实例自己 -state 参数的：哪怕
// 这个进程是拿 -state LIVE 启动的（专门执行 LIVE 策略），推进检查也照样在后台跑，
// 这是本来就有的行为，不是这次改造引入的，所以这里不能复用 main.go 里已经按 -state
// 加载好的那份策略列表（state 很可能根本不是 PAPER_TRADING），必须独立查询。
//
// 这一步系统可以自动做：状态机规则里只有 LIVE_ELIGIBLE → LIVE 必须用户手动点头，
// PAPER_TRADING → LIVE_ELIGIBLE 只是"数据说话"，不涉及真金白银。
//
// 单个策略的统计读取失败或推进失败都不能拖累其它策略——这条隔离原则贯穿整个执行层，
// 这里同样适用：一个策略的数据问题不该让其它已经跑够的策略也被卡住，多用户模式下这条
// 隔离自然扩展成"同一批次里其它用户的策略也不受影响"，不需要额外代码。
func checkPromotions(ctx context.Context, store promotionStore, ownerUserID string, gate strategy.Gate, logger *slog.Logger) {
	var strategies []types.StrategyConfig
	var err error
	if ownerUserID != "" {
		strategies, err = store.ListStrategiesByState(ctx, ownerUserID, types.StatePaperTrading)
	} else {
		strategies, err = store.ListStrategiesByStateAllUsers(ctx, types.StatePaperTrading)
	}
	if err != nil {
		logger.Error("扫描模拟盘策略失败", "err", err)
		return
	}

	for _, sc := range strategies {
		// sc.UserID 直接从查出来的这一行读——多用户模式下每条策略归属不同用户，
		// 不能再用一个闭包里固定的 ownerUserID。
		paper, err := store.PaperStats(ctx, sc.UserID, sc.ID)
		if err != nil {
			logger.Error("读取模拟盘统计失败，跳过本次检查", "strategy_id", sc.ID, "err", err)
			continue
		}

		evidence, err := strategy.CheckTransition(strategy.TransitionRequest{
			From: types.StatePaperTrading, To: types.StateLiveEligible,
			Actor: strategy.ActorSystem, ActorID: promotionActor,
			Reason: "模拟盘统计达标，自动推进",
			Paper:  &paper,
		}, gate)
		if err != nil {
			logger.Debug("尚未达到模拟盘门槛", "strategy_id", sc.ID,
				"paper_duration", paper.Duration().String(), "paper_trades", paper.TradeCount, "reason", err)
			continue
		}

		if err := store.UpdateStrategyState(ctx, sc.UserID, storage.Transition{
			StrategyID: sc.ID, From: types.StatePaperTrading, To: types.StateLiveEligible,
			Actor:  string(strategy.ActorSystem) + ":" + promotionActor,
			Reason: "模拟盘统计达标，自动推进", Evidence: evidence,
		}); err != nil {
			logger.Error("推进到 LIVE_ELIGIBLE 失败", "strategy_id", sc.ID, "err", err)
			continue
		}

		logger.Info("策略已推进到 LIVE_ELIGIBLE",
			"strategy_id", sc.ID, "symbol", sc.Symbol,
			"paper_duration", paper.Duration().String(), "paper_trades", paper.TradeCount)
	}
}

// runPromotionLoop 按固定间隔重复调用 checkPromotions，直到 ctx 被取消。
//
// 启动时先立即查一次：不必等第一个 interval 过去，一个策略刚好在executor 重启前
// 就已经达标的话，不该白白多等一个周期。
func runPromotionLoop(ctx context.Context, store promotionStore, ownerUserID string, gate strategy.Gate, logger *slog.Logger, interval time.Duration) {
	checkPromotions(ctx, store, ownerUserID, gate, logger)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			checkPromotions(ctx, store, ownerUserID, gate, logger)
		}
	}
}
