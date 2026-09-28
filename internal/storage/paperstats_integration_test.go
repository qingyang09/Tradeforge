//go:build integration

// 需要 docker-compose 起的 Postgres 才能运行：
//
//	docker compose up -d
//	go test -tags=integration ./internal/storage/... -run PaperStats -v
package storage

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/config"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

func newTestStrategy(t *testing.T, userID string) types.StrategyConfig {
	t.Helper()
	return types.StrategyConfig{
		ID: idgen.NewUUID(), UserID: userID, Name: "PaperStats 集成测试", Symbol: "BTCUSDT",
		Timeframe: types.TF1h, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{{Module: "volume_breakout", Params: map[string]any{}}},
		Risk:    types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State:   types.StateDraft,
	}
}

// latestTransitionTime 读回某策略最近一次流转到 target 状态的真实落库时间。
// created_at 由 Postgres 服务端的 now() 赋值，跟 Go 测试进程的 time.Now() 不是
// 同一个时钟源，构造因果上"晚于该次流转"的时间戳必须以这个读回的值为准。
func latestTransitionTime(t *testing.T, ctx context.Context, s *Store, userID, strategyID string, target types.StrategyState) time.Time {
	t.Helper()
	transitions, err := s.ListTransitions(ctx, userID, strategyID)
	if err != nil {
		t.Fatalf("查询流转记录失败：%v", err)
	}
	for i := len(transitions) - 1; i >= 0; i-- {
		if transitions[i].To == target {
			return transitions[i].CreatedAt
		}
	}
	t.Fatalf("策略 %s 没有流转到 %s 的记录", strategyID, target)
	return time.Time{}
}

func recordFilledPaperOrder(t *testing.T, ctx context.Context, s *Store, strategyID string, at time.Time) {
	t.Helper()
	err := s.RecordOrder(ctx, types.Order{
		ID: idgen.NewUUID(), StrategyID: strategyID, Symbol: "BTCUSDT",
		Side: types.SideBuy, Type: types.OrderMarket, Mode: types.ModePaper,
		Quantity: decimal.NewFromInt(1), FilledPrice: decimal.NewFromInt(100),
		Status: types.OrderFilled, CreatedAt: at, FilledAt: at,
		Provenance: types.OrderProvenance{DecisionID: idgen.NewUUID()},
	})
	if err != nil {
		t.Fatalf("写入测试订单失败：%v", err)
	}
}

// PaperStats 必须只统计"最近一次进入 PAPER_TRADING 之后"的成交，且只认 PAPER 模式的
// FILLED 订单——LIVE 订单、REJECTED 订单、以及重新进入模拟盘之前的旧订单都不该计入。
func TestPaperStatsCountsOnlyRecentPaperTradingWindow(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败（是否已 docker compose up -d？）：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "paperstats-window")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	sc := newTestStrategy(t, u.ID)
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("保存策略失败：%v", err)
	}

	advance := func(from, to types.StrategyState) {
		t.Helper()
		if err := store.UpdateStrategyState(ctx, u.ID, Transition{
			StrategyID: sc.ID, From: from, To: to,
			Actor: "system:test", Reason: "集成测试推进",
		}); err != nil {
			t.Fatalf("推进 %s → %s 失败：%v", from, to, err)
		}
	}

	// 第一次进入模拟盘。
	advance(types.StateDraft, types.StateBacktested)
	advance(types.StateBacktested, types.StatePaperTrading)

	// 第一轮里成交一笔——之后会被风控暂停，这笔不该计入重新进入后的统计。
	recordFilledPaperOrder(t, ctx, store, sc.ID, time.Now().UTC())

	advance(types.StatePaperTrading, types.StateSuspended)
	time.Sleep(10 * time.Millisecond) // 确保第二次进入的时间戳严格晚于第一次
	advance(types.StateSuspended, types.StatePaperTrading)

	// 流转的 created_at 由 Postgres 服务端的 now() 赋值，而订单的 created_at 由调用方
	// （这里是测试进程）的 time.Now() 赋值——两者不是同一个时钟源。真实生产环境里，
	// 执行层要先经过一次"查询到策略处于 PAPER_TRADING"的数据库往返才会开始下单，
	// 天然晚于流转提交；这里必须显式读回流转的真实落库时间再据此构造订单时间戳，
	// 否则测试进程时钟与数据库服务端时钟之间哪怕几毫秒的先后顺序都可能导致误判。
	reenteredAt := latestTransitionTime(t, ctx, store, u.ID, sc.ID, types.StatePaperTrading)
	secondRoundOrder1 := reenteredAt.Add(time.Millisecond)
	recordFilledPaperOrder(t, ctx, store, sc.ID, secondRoundOrder1)
	recordFilledPaperOrder(t, ctx, store, sc.ID, reenteredAt.Add(2*time.Millisecond))

	// 一笔被拒绝的订单和一笔实盘订单，都不该计入模拟盘笔数。
	if err := store.RecordOrder(ctx, types.Order{
		ID: idgen.NewUUID(), StrategyID: sc.ID, Symbol: "BTCUSDT",
		Side: types.SideBuy, Type: types.OrderMarket, Mode: types.ModePaper,
		Quantity: decimal.NewFromInt(1), Status: types.OrderRejected,
		RejectReason: "风控拒绝", CreatedAt: time.Now().UTC(),
		Provenance: types.OrderProvenance{DecisionID: idgen.NewUUID()},
	}); err != nil {
		t.Fatalf("写入被拒订单失败：%v", err)
	}
	if err := store.RecordOrder(ctx, types.Order{
		ID: idgen.NewUUID(), StrategyID: sc.ID, Symbol: "BTCUSDT",
		Side: types.SideBuy, Type: types.OrderMarket, Mode: types.ModeLive,
		Quantity: decimal.NewFromInt(1), FilledPrice: decimal.NewFromInt(100),
		Status: types.OrderFilled, CreatedAt: time.Now().UTC(), FilledAt: time.Now().UTC(),
		Provenance: types.OrderProvenance{DecisionID: idgen.NewUUID()},
	}); err != nil {
		t.Fatalf("写入实盘订单失败：%v", err)
	}

	stats, err := store.PaperStats(ctx, u.ID, sc.ID)
	if err != nil {
		t.Fatalf("读取 PaperStats 失败：%v", err)
	}
	if stats.TradeCount != 2 {
		t.Errorf("TradeCount = %d，期望 2（只数第二轮模拟盘里的 FILLED PAPER 订单）", stats.TradeCount)
	}
	if !stats.StartedAt.Equal(reenteredAt) {
		t.Errorf("StartedAt = %s，期望等于第二次进入模拟盘的流转时间 %s", stats.StartedAt, reenteredAt)
	}
	if stats.Now.IsZero() {
		t.Error("Now 不能为零值")
	}
}

func TestPaperStatsErrorsWhenStrategyNeverEnteredPaperTrading(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "paperstats-never-entered")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	sc := newTestStrategy(t, u.ID)
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("保存策略失败：%v", err)
	}

	if _, err := store.PaperStats(ctx, u.ID, sc.ID); err == nil {
		t.Fatal("从未进入过模拟盘的策略应当返回错误")
	}
}
