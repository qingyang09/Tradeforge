//go:build integration

// Requires a Postgres started via docker-compose to run:
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
		ID: idgen.NewUUID(), UserID: userID, Name: "PaperStats integration test", Symbol: "BTCUSDT",
		Timeframe: types.TF1h, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{{Module: "volume_breakout", Params: map[string]any{}}},
		Risk:    types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State:   types.StateDraft,
	}
}

// latestTransitionTime reads back the real persisted time of a strategy's
// most recent transition into the target state.
// created_at is assigned by Postgres's server-side now(), which is not the
// same clock source as the Go test process's time.Now(); constructing a
// timestamp that is causally "after this transition" must be based on this
// value read back from the database.
func latestTransitionTime(t *testing.T, ctx context.Context, s *Store, userID, strategyID string, target types.StrategyState) time.Time {
	t.Helper()
	transitions, err := s.ListTransitions(ctx, userID, strategyID)
	if err != nil {
		t.Fatalf("query transition records: %v", err)
	}
	for i := len(transitions) - 1; i >= 0; i-- {
		if transitions[i].To == target {
			return transitions[i].CreatedAt
		}
	}
	t.Fatalf("strategy %s has no transition record to %s", strategyID, target)
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
		t.Fatalf("insert test order: %v", err)
	}
}

// PaperStats must only count fills "since the most recent entry into
// PAPER_TRADING", and only FILLED orders in PAPER mode -- LIVE orders,
// REJECTED orders, and old orders from before re-entering paper trading
// should all be excluded.
func TestPaperStatsCountsOnlyRecentPaperTradingWindow(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connect to Postgres (did you run docker compose up -d?): %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "paperstats-window")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("clean up test data: %v", err)
		}
	}()

	sc := newTestStrategy(t, u.ID)
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("save strategy: %v", err)
	}

	advance := func(from, to types.StrategyState) {
		t.Helper()
		if err := store.UpdateStrategyState(ctx, u.ID, Transition{
			StrategyID: sc.ID, From: from, To: to,
			Actor: "system:test", Reason: "integration test advance",
		}); err != nil {
			t.Fatalf("advance %s -> %s: %v", from, to, err)
		}
	}

	// First entry into paper trading.
	advance(types.StateDraft, types.StateBacktested)
	advance(types.StateBacktested, types.StatePaperTrading)

	// One fill in the first round -- it will then be suspended by risk
	// controls, and this fill should not count toward the post-re-entry stats.
	recordFilledPaperOrder(t, ctx, store, sc.ID, time.Now().UTC())

	advance(types.StatePaperTrading, types.StateSuspended)
	time.Sleep(10 * time.Millisecond) // ensure the second entry's timestamp is strictly after the first
	advance(types.StateSuspended, types.StatePaperTrading)

	// The transition's created_at is assigned by Postgres's server-side
	// now(), while the order's created_at is assigned by the caller's (here,
	// the test process's) time.Now() -- these are not the same clock source.
	// In real production, the execution layer must first make a database
	// round trip to "see the strategy is in PAPER_TRADING" before it starts
	// placing orders, so it's naturally after the transition commit; here we
	// must explicitly read back the transition's real persisted time and
	// build the order timestamp from that, otherwise even a few
	// milliseconds of skew between the test process clock and the database
	// server clock could cause a false result.
	reenteredAt := latestTransitionTime(t, ctx, store, u.ID, sc.ID, types.StatePaperTrading)
	secondRoundOrder1 := reenteredAt.Add(time.Millisecond)
	recordFilledPaperOrder(t, ctx, store, sc.ID, secondRoundOrder1)
	recordFilledPaperOrder(t, ctx, store, sc.ID, reenteredAt.Add(2*time.Millisecond))

	// A rejected order and a live order, neither of which should count toward the paper trading trade count.
	if err := store.RecordOrder(ctx, types.Order{
		ID: idgen.NewUUID(), StrategyID: sc.ID, Symbol: "BTCUSDT",
		Side: types.SideBuy, Type: types.OrderMarket, Mode: types.ModePaper,
		Quantity: decimal.NewFromInt(1), Status: types.OrderRejected,
		RejectReason: "rejected by risk controls", CreatedAt: time.Now().UTC(),
		Provenance: types.OrderProvenance{DecisionID: idgen.NewUUID()},
	}); err != nil {
		t.Fatalf("insert rejected order: %v", err)
	}
	if err := store.RecordOrder(ctx, types.Order{
		ID: idgen.NewUUID(), StrategyID: sc.ID, Symbol: "BTCUSDT",
		Side: types.SideBuy, Type: types.OrderMarket, Mode: types.ModeLive,
		Quantity: decimal.NewFromInt(1), FilledPrice: decimal.NewFromInt(100),
		Status: types.OrderFilled, CreatedAt: time.Now().UTC(), FilledAt: time.Now().UTC(),
		Provenance: types.OrderProvenance{DecisionID: idgen.NewUUID()},
	}); err != nil {
		t.Fatalf("insert live order: %v", err)
	}

	stats, err := store.PaperStats(ctx, u.ID, sc.ID)
	if err != nil {
		t.Fatalf("read PaperStats: %v", err)
	}
	if stats.TradeCount != 2 {
		t.Errorf("TradeCount = %d, want 2 (only FILLED PAPER orders from the second paper trading round)", stats.TradeCount)
	}
	if !stats.StartedAt.Equal(reenteredAt) {
		t.Errorf("StartedAt = %s, want it to equal the second paper trading entry's transition time %s", stats.StartedAt, reenteredAt)
	}
	if stats.Now.IsZero() {
		t.Error("Now must not be zero")
	}
}

func TestPaperStatsErrorsWhenStrategyNeverEnteredPaperTrading(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "paperstats-never-entered")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("clean up test data: %v", err)
		}
	}()

	sc := newTestStrategy(t, u.ID)
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("save strategy: %v", err)
	}

	if _, err := store.PaperStats(ctx, u.ID, sc.ID); err == nil {
		t.Fatal("a strategy that has never entered paper trading should return an error")
	}
}
