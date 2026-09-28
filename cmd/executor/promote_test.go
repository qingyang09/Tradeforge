package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// fakePromotionStore 是 promotionStore 的内存实现，让 checkPromotions 的核心逻辑
// 不需要真实 Postgres 就能测试。
type fakePromotionStore struct {
	strategies []types.StrategyConfig
	stats      map[string]strategy.PaperStats
	statsErr   map[string]error

	listErr   error
	updateErr map[string]error

	updated []storage.Transition

	// paperStatsUserIDs/updateStateUserIDs 记录每次调用实际收到的 userID，按
	// strategyID 索引——用来验证多用户模式下 checkPromotions 传的是每条策略自己的
	// sc.UserID，而不是某个写死的值。
	paperStatsUserIDs  map[string]string
	updateStateUserIDs map[string]string
}

func (f *fakePromotionStore) ListStrategiesByState(_ context.Context, userID string, state types.StrategyState) ([]types.StrategyConfig, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []types.StrategyConfig
	for _, s := range f.strategies {
		if s.State == state && s.UserID == userID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakePromotionStore) ListStrategiesByStateAllUsers(_ context.Context, state types.StrategyState) ([]types.StrategyConfig, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []types.StrategyConfig
	for _, s := range f.strategies {
		if s.State == state {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakePromotionStore) PaperStats(_ context.Context, userID string, strategyID string) (strategy.PaperStats, error) {
	if f.paperStatsUserIDs == nil {
		f.paperStatsUserIDs = make(map[string]string)
	}
	f.paperStatsUserIDs[strategyID] = userID
	if err, ok := f.statsErr[strategyID]; ok {
		return strategy.PaperStats{}, err
	}
	return f.stats[strategyID], nil
}

func (f *fakePromotionStore) UpdateStrategyState(_ context.Context, userID string, t storage.Transition) error {
	if f.updateStateUserIDs == nil {
		f.updateStateUserIDs = make(map[string]string)
	}
	f.updateStateUserIDs[t.StrategyID] = userID
	if err, ok := f.updateErr[t.StrategyID]; ok {
		return err
	}
	f.updated = append(f.updated, t)
	return nil
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var paperStart = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func paperStrategy(id string) types.StrategyConfig {
	return types.StrategyConfig{ID: id, UserID: testOwnerUserID, Name: "测试策略", Symbol: "BTCUSDT", State: types.StatePaperTrading}
}

func TestCheckPromotionsAdvancesQualifyingStrategy(t *testing.T) {
	gate := strategy.DefaultGate()
	store := &fakePromotionStore{
		strategies: []types.StrategyConfig{paperStrategy("s1")},
		stats: map[string]strategy.PaperStats{
			"s1": {StartedAt: paperStart, Now: paperStart.Add(10 * 24 * time.Hour), TradeCount: 20},
		},
	}

	checkPromotions(context.Background(), store, testOwnerUserID, gate, quietLogger())

	if len(store.updated) != 1 {
		t.Fatalf("推进次数 = %d，期望 1", len(store.updated))
	}
	got := store.updated[0]
	if got.StrategyID != "s1" || got.From != types.StatePaperTrading || got.To != types.StateLiveEligible {
		t.Errorf("推进记录不符预期：%+v", got)
	}
	if got.Evidence["paper_trade_count"] != 20 {
		t.Errorf("推进依据缺少笔数快照：%+v", got.Evidence)
	}
}

func TestCheckPromotionsSkipsUnqualifiedStrategy(t *testing.T) {
	gate := strategy.DefaultGate()
	store := &fakePromotionStore{
		strategies: []types.StrategyConfig{paperStrategy("s1")},
		stats: map[string]strategy.PaperStats{
			// 时长够了但笔数不够。
			"s1": {StartedAt: paperStart, Now: paperStart.Add(10 * 24 * time.Hour), TradeCount: 2},
		},
	}

	checkPromotions(context.Background(), store, testOwnerUserID, gate, quietLogger())

	if len(store.updated) != 0 {
		t.Fatalf("未达标的策略不应被推进，实际推进了 %d 次", len(store.updated))
	}
}

// 一个策略的统计读取失败不能拖累其它已经达标的策略——执行层的隔离原则同样适用于这里。
func TestCheckPromotionsIsolatesPerStrategyFailures(t *testing.T) {
	gate := strategy.DefaultGate()
	store := &fakePromotionStore{
		strategies: []types.StrategyConfig{paperStrategy("bad"), paperStrategy("good")},
		stats: map[string]strategy.PaperStats{
			"good": {StartedAt: paperStart, Now: paperStart.Add(10 * 24 * time.Hour), TradeCount: 20},
		},
		statsErr: map[string]error{"bad": errors.New("boom")},
	}

	checkPromotions(context.Background(), store, testOwnerUserID, gate, quietLogger())

	if len(store.updated) != 1 || store.updated[0].StrategyID != "good" {
		t.Fatalf("期望只有 good 被推进，实际：%+v", store.updated)
	}
}

func TestCheckPromotionsIsolatesUpdateFailures(t *testing.T) {
	gate := strategy.DefaultGate()
	store := &fakePromotionStore{
		strategies: []types.StrategyConfig{paperStrategy("s1"), paperStrategy("s2")},
		stats: map[string]strategy.PaperStats{
			"s1": {StartedAt: paperStart, Now: paperStart.Add(10 * 24 * time.Hour), TradeCount: 20},
			"s2": {StartedAt: paperStart, Now: paperStart.Add(10 * 24 * time.Hour), TradeCount: 20},
		},
		updateErr: map[string]error{"s1": errors.New("db down")},
	}

	checkPromotions(context.Background(), store, testOwnerUserID, gate, quietLogger())

	if len(store.updated) != 1 || store.updated[0].StrategyID != "s2" {
		t.Fatalf("期望 s1 推进失败但 s2 成功，实际：%+v", store.updated)
	}
}

func TestCheckPromotionsHandlesListFailureWithoutPanicking(t *testing.T) {
	store := &fakePromotionStore{listErr: errors.New("db down")}
	checkPromotions(context.Background(), store, testOwnerUserID, strategy.DefaultGate(), quietLogger())
	if len(store.updated) != 0 {
		t.Fatalf("扫描失败时不应有任何推进")
	}
}

// 只扫描 PAPER_TRADING 状态：其它状态的策略不该被这个检查碰到。
func TestCheckPromotionsIgnoresOtherStates(t *testing.T) {
	store := &fakePromotionStore{
		strategies: []types.StrategyConfig{
			{ID: "draft", State: types.StateDraft},
			{ID: "live", State: types.StateLive},
		},
	}
	checkPromotions(context.Background(), store, testOwnerUserID, strategy.DefaultGate(), quietLogger())
	if len(store.updated) != 0 {
		t.Fatalf("非 PAPER_TRADING 状态的策略不该被推进")
	}
}

// TestCheckPromotionsMultiTenantIsolatesFailuresAcrossUsers 是这一轮改造新增的关键
// 属性测试：多用户模式（ownerUserID=""）下，A 用户的策略统计读取失败，不能拖累 B
// 用户本该被推进的策略——这是"单个策略失败不拖累其它策略"这条既有隔离原则，第一次
// 在跨用户场景下被验证（此前的 TestCheckPromotionsIsolatesPerStrategyFailures 只验证过
// 同一用户内的隔离）。同时验证 PaperStats/UpdateStrategyState 收到的 userID 确实是
// 每条策略自己的 sc.UserID，不是任何写死的值。
func TestCheckPromotionsMultiTenantIsolatesFailuresAcrossUsers(t *testing.T) {
	const userA = "00000000-0000-4000-8000-0000000000aa"
	const userB = "00000000-0000-4000-8000-0000000000bb"
	gate := strategy.DefaultGate()
	store := &fakePromotionStore{
		strategies: []types.StrategyConfig{
			{ID: "bad", UserID: userA, Name: "A 的策略", Symbol: "BTCUSDT", State: types.StatePaperTrading},
			{ID: "good", UserID: userB, Name: "B 的策略", Symbol: "BTCUSDT", State: types.StatePaperTrading},
		},
		stats: map[string]strategy.PaperStats{
			"good": {StartedAt: paperStart, Now: paperStart.Add(10 * 24 * time.Hour), TradeCount: 20},
		},
		statsErr: map[string]error{"bad": errors.New("boom")},
	}

	// ownerUserID 传空字符串 = 多用户模式，走 ListStrategiesByStateAllUsers。
	checkPromotions(context.Background(), store, "", gate, quietLogger())

	if len(store.updated) != 1 || store.updated[0].StrategyID != "good" {
		t.Fatalf("A 的统计读取失败不应拖累 B 的策略推进，实际：%+v", store.updated)
	}
	if store.paperStatsUserIDs["bad"] != userA {
		t.Errorf("A 的策略应该用 A 自己的 userID 查统计，实际用了 %q", store.paperStatsUserIDs["bad"])
	}
	if store.paperStatsUserIDs["good"] != userB {
		t.Errorf("B 的策略应该用 B 自己的 userID 查统计，实际用了 %q", store.paperStatsUserIDs["good"])
	}
	if store.updateStateUserIDs["good"] != userB {
		t.Errorf("推进 B 的策略应该用 B 自己的 userID，实际用了 %q", store.updateStateUserIDs["good"])
	}
}

// runPromotionLoop 必须在 ctx 取消后退出，不能泄漏 goroutine。
func TestRunPromotionLoopStopsOnContextCancel(t *testing.T) {
	store := &fakePromotionStore{}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runPromotionLoop(ctx, store, testOwnerUserID, strategy.DefaultGate(), quietLogger(), time.Hour)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 runPromotionLoop 应当退出，但超时未退出")
	}
}
