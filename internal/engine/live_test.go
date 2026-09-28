//go:build integration

// Requires Postgres and Kafka started via docker-compose:
//
//	docker compose up -d
//	go test -tags=integration ./internal/engine/... -run Live -v
package engine

import (
	"context"
	"testing"
	"time"

	"tradeforge/internal/config"
	"tradeforge/internal/messaging"
	"tradeforge/internal/modules"
	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// createEngineTestUser inserts a user row dedicated to this file's
// integration tests — the strategies.user_id foreign key requires an
// actual row in users, it can't be satisfied by just making up a UUID
// (see the identically-named pattern in internal/storage's integration tests).
func createEngineTestUser(ctx context.Context, t *testing.T, store *storage.Store, label string) storage.User {
	t.Helper()
	u := storage.User{
		ID: idgen.NewUUID(), Email: label + "-" + idgen.NewUUID() + "@engine.test",
		PasswordHash: []byte("x"),
	}
	if err := store.CreateUser(ctx, u); err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return u
}

// The real chain: three real modules -> combination engine -> Postgres audit
// + Kafka publish -> consume it back and verify.
func TestLiveEndToEndThroughKafkaAndPostgres(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("failed to connect to Postgres (did you run docker compose up -d?): %v", err)
	}
	defer store.Close()

	u := createEngineTestUser(ctx, t, store, "live-e2e")
	defer func() {
		if _, err := store.Pool().Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("failed to clean up test data: %v", err)
		}
	}()

	if err := messaging.EnsureTopics(ctx, cfg.Kafka); err != nil {
		t.Fatalf("failed to create Kafka topics (did you run docker compose up -d?): %v", err)
	}
	pub := messaging.NewKafkaPublisher(cfg.Kafka)
	defer pub.Close()

	// The decisions table has a foreign key into strategies, so the
	// strategy itself must be persisted first.
	sc := realStrategy(types.CombineAll, 0)
	sc.ID = newUUID()
	sc.UserID = u.ID
	sc.SourceUtterance = "strategy for integration testing"
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("failed to save strategy: %v", err)
	}

	// Set up the consumer before publishing, so the message can't land
	// before the subscription does and go unread.
	reader := messaging.NewDecisionReader(cfg.Kafka, "tradeforge-test-"+sc.ID)
	defer reader.Close()

	e := New(modules.NewDefaultRegistry(),
		WithAuditor(store), WithPublisher(pub), WithLogger(quietLogger()))

	decision, err := e.Process(ctx, sc, feedsFor(bullishBreakoutData()))
	if err != nil {
		t.Fatalf("Process failed: %v", err)
	}
	if !decision.Triggered {
		t.Fatalf("expected trigger, got none: %s", decision.Reason)
	}
	t.Logf("published decision %s: %s", decision.ID, decision.Reason)

	// 1) Verify the audit record.
	stored, err := store.ListDecisions(ctx, u.ID, sc.ID, 10)
	if err != nil {
		t.Fatalf("failed to read decision audit records: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("audit table has %d records, want 1", len(stored))
	}
	got := stored[0]
	if got.ID != decision.ID {
		t.Errorf("audit record ID = %s, want %s", got.ID, decision.ID)
	}
	if got.Direction != decision.Direction || got.Triggered != decision.Triggered {
		t.Errorf("audit record doesn't match the in-memory decision: %+v vs %+v", got, decision)
	}
	if !got.Price.Equal(decision.Price) {
		t.Errorf("price after storage = %s, want %s (a NUMERIC round trip must not lose precision)", got.Price, decision.Price)
	}
	if len(got.Signals) != 3 {
		t.Errorf("signal count in audit record = %d, want 3", len(got.Signals))
	}

	// 2) Verify the Kafka consumption.
	//
	// The decisions topic is shared across test runs and has a retention
	// period: a brand-new consumer group with no committed offset defaults
	// to reading from the earliest message, so any leftover decisions from
	// a previous run get read before the one this run just published. Skip
	// non-matching messages one at a time instead of assuming the first
	// message read is the right one.
	readCtx, readCancel := context.WithTimeout(ctx, 60*time.Second)
	defer readCancel()
	var consumed types.Decision
	for {
		consumed, err = reader.Read(readCtx)
		if err != nil {
			t.Fatalf("failed to read decision from Kafka: %v", err)
		}
		if consumed.ID == decision.ID {
			break
		}
		t.Logf("skipping leftover message %s (not published by this test run)", consumed.ID)
	}
	if consumed.Direction != decision.Direction {
		t.Errorf("consumed Direction = %s, want %s", consumed.Direction, decision.Direction)
	}
	if len(consumed.Signals) != 3 {
		t.Errorf("consumed signal count = %d, want 3 (explainability data must survive the message queue intact)", len(consumed.Signals))
	}
	t.Logf("consumed decision %s back from Kafka, %d signals", consumed.ID, len(consumed.Signals))
}

// A real-database round trip for state transitions and strategy read/write.
func TestLiveStrategyRoundTrip(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("failed to connect to Postgres: %v", err)
	}
	defer store.Close()

	u := createEngineTestUser(ctx, t, store, "live-roundtrip")
	defer func() {
		if _, err := store.Pool().Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("failed to clean up test data: %v", err)
		}
	}()

	sc := realStrategy(types.CombineWeighted, 0.6)
	sc.ID = newUUID()
	sc.UserID = u.ID
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("failed to save strategy: %v", err)
	}

	loaded, err := store.GetStrategy(ctx, u.ID, sc.ID)
	if err != nil {
		t.Fatalf("failed to read strategy: %v", err)
	}
	if loaded.Symbol != sc.Symbol || len(loaded.Modules) != len(sc.Modules) {
		t.Errorf("config doesn't match after round trip: %+v", loaded)
	}
	if !loaded.Risk.MaxPositionSizeQuote.Equal(sc.Risk.MaxPositionSizeQuote) {
		t.Errorf("risk amount after round trip = %s, want %s",
			loaded.Risk.MaxPositionSizeQuote, sc.Risk.MaxPositionSizeQuote)
	}
	if loaded.State != types.StateDraft {
		t.Errorf("new strategy State = %s, want DRAFT", loaded.State)
	}

	// The state transition and its audit record must commit in the same transaction.
	if err := store.UpdateStrategyState(ctx, u.ID, storage.Transition{
		StrategyID: sc.ID, From: types.StateDraft, To: types.StateBacktested,
		Actor: "system:test", Reason: "integration test advancement",
		Evidence: map[string]any{"out_of_sample_sharpe": 1.2},
	}); err != nil {
		t.Fatalf("state transition failed: %v", err)
	}

	after, err := store.GetStrategy(ctx, u.ID, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != types.StateBacktested {
		t.Errorf("State after transition = %s, want BACKTESTED", after.State)
	}

	// Optimistic locking: from_state no longer matches, so the repeated
	// transition must be rejected.
	err = store.UpdateStrategyState(ctx, u.ID, storage.Transition{
		StrategyID: sc.ID, From: types.StateDraft, To: types.StateBacktested,
		Actor: "system:test", Reason: "repeated transition",
	})
	if err == nil {
		t.Error("a mismatched from_state should reject the transition (optimistic lock not enforced)")
	}

	trans, err := store.ListTransitions(ctx, u.ID, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trans) != 1 {
		t.Fatalf("audit record count = %d, want 1 (a rejected transition should leave no trace)", len(trans))
	}
	if trans[0].Evidence["out_of_sample_sharpe"] == nil {
		t.Error("the transition's evidence wasn't persisted, so there's no way to answer 'what justified this transition' after the fact")
	}
}
