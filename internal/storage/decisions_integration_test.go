//go:build integration

// Requires a Postgres started via docker-compose to run:
//
//	docker compose up -d
//	go test -tags=integration ./internal/storage/... -run Decision -v
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

// TestRecordDecisionRoundTripsStructuredReason is a regression test for a bug
// that actually reproduced in practice while manually testing this session's
// bilingual work in a browser: decisions.reason is a plain text column, not
// jsonb like signals/provenance/detail -- RecordDecision passed
// types.Message (a struct) straight to pgx as the query argument, and
// ListDecisions scanned straight into *types.Message. Neither pgx nor the
// Go sql/driver machinery knows how to encode/decode an arbitrary struct
// into a text column on its own (unlike a jsonb column, where the driver
// just writes the bytes json.Marshal produced): the write failed outright
// ("cannot find encode plan"), and even a plain string already in that
// column failed to scan ("cannot scan text ... into *types.Message"). No
// test exercised this path against a real database, so it wasn't caught
// until manual browser verification hit a 500 on the strategy detail page.
func TestRecordDecisionRoundTripsStructuredReason(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "decisions-structured-reason")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	sc := newTestStrategy(t, u.ID)
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("saving strategy: %v", err)
	}

	d := types.Decision{
		ID: idgen.NewUUID(), StrategyID: sc.ID, Symbol: sc.Symbol, Direction: types.DirectionLong,
		Score: 0.8, Triggered: true, Price: decimal.NewFromInt(50000),
		Reason:      types.Msg("engine.decision.all_triggered", "count", 1, "direction", "LONG", "score", "0.800"),
		Timestamp:   time.Now(),
		EvaluatedAt: time.Now(),
		Signals: []types.Signal{
			{Module: "volume_breakout", Direction: types.DirectionLong, Confidence: 0.8,
				Reason: types.Msg("modules.volume_breakout.reason.triggered_bullish_candle",
					"window", 20, "ratio", "3.20", "multiplier", "3.00")},
		},
	}
	if err := store.RecordDecision(ctx, d); err != nil {
		t.Fatalf("RecordDecision with a structured Reason: %v", err)
	}

	got, err := store.ListDecisions(ctx, u.ID, sc.ID, 10)
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(got))
	}
	if got[0].Reason.Key != "engine.decision.all_triggered" {
		t.Errorf("Reason.Key = %q, want %q", got[0].Reason.Key, "engine.decision.all_triggered")
	}
	if got[0].Reason.Args["direction"] != "LONG" {
		t.Errorf("Reason.Args[direction] = %v, want LONG", got[0].Reason.Args["direction"])
	}
	if len(got[0].Signals) != 1 || got[0].Signals[0].Reason.Key != "modules.volume_breakout.reason.triggered_bullish_candle" {
		t.Errorf("Signals[0].Reason not round-tripped correctly: %+v", got[0].Signals)
	}
}

// TestListDecisionsReadsLegacyPlainTextReason is a regression test for the
// backward-compatibility half of the same bug: a row written before
// Decision.Reason became a types.Message holds the plain sentence directly
// in the reason column, with no JSON quoting at all (it was just a Go
// string written straight into a text column) -- this must still read back
// as a Message with that text as its Literal, not fail to parse as JSON.
func TestListDecisionsReadsLegacyPlainTextReason(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "decisions-legacy-reason")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	sc := newTestStrategy(t, u.ID)
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("saving strategy: %v", err)
	}

	const legacyReason = "所有模块一致看空（旧格式记录）"
	_, err = store.pool.Exec(ctx, `
		INSERT INTO decisions (id, strategy_id, symbol, direction, score, triggered, reason, price, signals, bar_time, evaluated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		idgen.NewUUID(), sc.ID, sc.Symbol, "SHORT", 0.5, true, legacyReason,
		"49000", []byte(`[]`), time.Now(), time.Now())
	if err != nil {
		t.Fatalf("inserting a legacy-shaped decision row: %v", err)
	}

	got, err := store.ListDecisions(ctx, u.ID, sc.ID, 10)
	if err != nil {
		t.Fatalf("ListDecisions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 decision, got %d", len(got))
	}
	if got[0].Reason.Literal != legacyReason {
		t.Errorf("Reason.Literal = %q, want %q", got[0].Reason.Literal, legacyReason)
	}
	if got[0].Reason.Key != "" {
		t.Errorf("a legacy row should decode with an empty Key, got %q", got[0].Reason.Key)
	}
}
