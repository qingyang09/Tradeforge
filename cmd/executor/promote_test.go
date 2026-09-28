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

// fakePromotionStore is an in-memory implementation of promotionStore that
// lets checkPromotions's core logic be tested without a real Postgres.
type fakePromotionStore struct {
	strategies []types.StrategyConfig
	stats      map[string]strategy.PaperStats
	statsErr   map[string]error

	listErr   error
	updateErr map[string]error

	updated []storage.Transition

	// paperStatsUserIDs/updateStateUserIDs record the userID actually
	// received on each call, indexed by strategyID — used to verify that in
	// multi-tenant mode checkPromotions passes each strategy's own
	// sc.UserID, not some hardcoded value.
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
	return types.StrategyConfig{ID: id, UserID: testOwnerUserID, Name: "Test Strategy", Symbol: "BTCUSDT", State: types.StatePaperTrading}
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
		t.Fatalf("promotion count = %d, want 1", len(store.updated))
	}
	got := store.updated[0]
	if got.StrategyID != "s1" || got.From != types.StatePaperTrading || got.To != types.StateLiveEligible {
		t.Errorf("promotion record doesn't match expectations: %+v", got)
	}
	if got.Evidence["paper_trade_count"] != 20 {
		t.Errorf("promotion evidence is missing the trade count snapshot: %+v", got.Evidence)
	}
}

func TestCheckPromotionsSkipsUnqualifiedStrategy(t *testing.T) {
	gate := strategy.DefaultGate()
	store := &fakePromotionStore{
		strategies: []types.StrategyConfig{paperStrategy("s1")},
		stats: map[string]strategy.PaperStats{
			// Duration is enough but trade count isn't.
			"s1": {StartedAt: paperStart, Now: paperStart.Add(10 * 24 * time.Hour), TradeCount: 2},
		},
	}

	checkPromotions(context.Background(), store, testOwnerUserID, gate, quietLogger())

	if len(store.updated) != 0 {
		t.Fatalf("an unqualified strategy should not be promoted, but it was promoted %d time(s)", len(store.updated))
	}
}

// A stats-read failure for one strategy must not drag down other strategies
// that already qualify — the execution layer's isolation principle applies
// here too.
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
		t.Fatalf("expected only good to be promoted, got: %+v", store.updated)
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
		t.Fatalf("expected s1's promotion to fail but s2's to succeed, got: %+v", store.updated)
	}
}

func TestCheckPromotionsHandlesListFailureWithoutPanicking(t *testing.T) {
	store := &fakePromotionStore{listErr: errors.New("db down")}
	checkPromotions(context.Background(), store, testOwnerUserID, strategy.DefaultGate(), quietLogger())
	if len(store.updated) != 0 {
		t.Fatalf("no promotions should happen when the scan fails")
	}
}

// Only PAPER_TRADING strategies should be scanned: strategies in other
// states should never be touched by this check.
func TestCheckPromotionsIgnoresOtherStates(t *testing.T) {
	store := &fakePromotionStore{
		strategies: []types.StrategyConfig{
			{ID: "draft", State: types.StateDraft},
			{ID: "live", State: types.StateLive},
		},
	}
	checkPromotions(context.Background(), store, testOwnerUserID, strategy.DefaultGate(), quietLogger())
	if len(store.updated) != 0 {
		t.Fatalf("strategies not in PAPER_TRADING state should not be promoted")
	}
}

// TestCheckPromotionsMultiTenantIsolatesFailuresAcrossUsers is a key new
// property test added by this refactor: in multi-tenant mode
// (ownerUserID=""), a stats-read failure for user A's strategy must not
// drag down a promotion user B's strategy otherwise qualifies for — this is
// the pre-existing "a single strategy's failure doesn't drag down other
// strategies" isolation principle, verified for the first time across a
// cross-user scenario (the earlier
// TestCheckPromotionsIsolatesPerStrategyFailures only verified isolation
// within a single user). It also verifies that the userID PaperStats/
// UpdateStrategyState receive really is each strategy's own sc.UserID, not
// any hardcoded value.
func TestCheckPromotionsMultiTenantIsolatesFailuresAcrossUsers(t *testing.T) {
	const userA = "00000000-0000-4000-8000-0000000000aa"
	const userB = "00000000-0000-4000-8000-0000000000bb"
	gate := strategy.DefaultGate()
	store := &fakePromotionStore{
		strategies: []types.StrategyConfig{
			{ID: "bad", UserID: userA, Name: "A's strategy", Symbol: "BTCUSDT", State: types.StatePaperTrading},
			{ID: "good", UserID: userB, Name: "B's strategy", Symbol: "BTCUSDT", State: types.StatePaperTrading},
		},
		stats: map[string]strategy.PaperStats{
			"good": {StartedAt: paperStart, Now: paperStart.Add(10 * 24 * time.Hour), TradeCount: 20},
		},
		statsErr: map[string]error{"bad": errors.New("boom")},
	}

	// Passing an empty ownerUserID = multi-tenant mode, taking the ListStrategiesByStateAllUsers path.
	checkPromotions(context.Background(), store, "", gate, quietLogger())

	if len(store.updated) != 1 || store.updated[0].StrategyID != "good" {
		t.Fatalf("A's stats-read failure should not drag down B's promotion, got: %+v", store.updated)
	}
	if store.paperStatsUserIDs["bad"] != userA {
		t.Errorf("A's strategy should query stats with A's own userID, got %q", store.paperStatsUserIDs["bad"])
	}
	if store.paperStatsUserIDs["good"] != userB {
		t.Errorf("B's strategy should query stats with B's own userID, got %q", store.paperStatsUserIDs["good"])
	}
	if store.updateStateUserIDs["good"] != userB {
		t.Errorf("promoting B's strategy should use B's own userID, got %q", store.updateStateUserIDs["good"])
	}
}

// runPromotionLoop must exit after ctx is canceled, without leaking a goroutine.
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
		t.Fatal("runPromotionLoop should exit after ctx is canceled, but timed out instead")
	}
}
