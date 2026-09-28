//go:build integration

// Requires a Postgres started via docker-compose to run:
//
//	docker compose up -d
//	go test -tags=integration ./internal/storage/... -run Strategy -v
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradeforge/internal/config"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// TestGetStrategyReadsCreatedUpdatedFromColumnsNotConfigBlob is a regression test for a
// bug that actually reproduced in practice: the cfg.CreatedAt/UpdatedAt passed into
// SaveStrategy is just whatever snapshot the caller happens to carry (many call paths
// don't set it at all, leaving it as the Go zero value), but the created_at/updated_at
// columns are maintained by the database itself via now() and are trustworthy.
// GetStrategy/ListStrategies used to only overlay State from its dedicated column back
// onto config and missed these two time fields, which really did cause the UI to show a
// zero value time like "0001-01-01 00:00" that was never actually written.
func TestGetStrategyReadsCreatedUpdatedFromColumnsNotConfigBlob(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "strategies-created-updated")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	sc := newTestStrategy(t, u.ID)
	// Deliberately leave CreatedAt/UpdatedAt unset, simulating the real-world scenario
	// where "the caller didn't provide these two fields" (sc.CreatedAt/UpdatedAt is the
	// Go zero value time.Time{} at this point).
	before := time.Now().Add(-time.Second)
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("saving strategy: %v", err)
	}

	got, err := store.GetStrategy(ctx, u.ID, sc.ID)
	if err != nil {
		t.Fatalf("reading strategy: %v", err)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("CreatedAt/UpdatedAt should not be zero: %+v", got)
	}
	if got.CreatedAt.Before(before) {
		t.Errorf("CreatedAt = %s, should be after the test start time %s (meaning it came from the database's now(), not a zero value)",
			got.CreatedAt, before)
	}

	list, err := store.ListStrategies(ctx, u.ID)
	if err != nil {
		t.Fatalf("listing strategies: %v", err)
	}
	found := false
	for _, s := range list {
		if s.ID == sc.ID {
			found = true
			if s.CreatedAt.IsZero() {
				t.Errorf("this strategy's CreatedAt in ListStrategies should also not be zero")
			}
		}
	}
	if !found {
		t.Fatal("ListStrategies did not return the strategy just saved")
	}
}

func TestDeleteStrategyCascadesToBacktestResults(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "strategies-delete-cascade")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	sc := newTestStrategy(t, u.ID)
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("saving strategy: %v", err)
	}

	btID := idgen.NewUUID()
	_, err = store.pool.Exec(ctx, `
		INSERT INTO backtest_results (
			id, strategy_id, symbol, overall, in_sample, out_of_sample,
			segments, fee_model, initial_capital, data_start, data_end, engine_version
		) VALUES ($1, $2, $3, '{}', '{}', '{}', '[]', '{}', '1000', now(), now(), 'test')`,
		btID, sc.ID, sc.Symbol)
	if err != nil {
		t.Fatalf("writing test backtest result: %v", err)
	}

	if err := store.DeleteStrategy(ctx, u.ID, sc.ID); err != nil {
		t.Fatalf("deleting strategy: %v", err)
	}

	if _, err := store.GetStrategy(ctx, u.ID, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("reading after deletion should return ErrNotFound, got: %v", err)
	}
	if _, err := store.LatestBacktestResult(ctx, u.ID, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a strategy should cascade-delete its associated backtest results, got: %v", err)
	}
}

func TestDeleteStrategyErrorsWhenNotFound(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "strategies-delete-not-found")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	if err := store.DeleteStrategy(ctx, u.ID, idgen.NewUUID()); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting a nonexistent strategy should return ErrNotFound, got: %v", err)
	}
}

// TestSaveStrategyRejectsCrossUserOverwrite is a regression test for a real
// vulnerability found during design: SaveStrategy upserts by id, and without an
// ownership check, user B could quietly overwrite user A's strategy content by calling
// SaveStrategy with A's existing strategy id.
func TestSaveStrategyRejectsCrossUserOverwrite(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "strategies-cross-user-a")
	userB := createTestUserForStorage(ctx, t, store, "strategies-cross-user-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	scA := newTestStrategy(t, userA.ID)
	if err := store.SaveStrategy(ctx, scA); err != nil {
		t.Fatalf("A saving strategy: %v", err)
	}

	// B forges a config carrying A's strategy id and tries to overwrite it under B's own identity.
	forged := newTestStrategy(t, userB.ID)
	forged.ID = scA.ID
	forged.Name = "name tampered with by B"
	if err := store.SaveStrategy(ctx, forged); !errors.Is(err, ErrNotFound) {
		t.Errorf("B overwriting A's strategy id with B's own UserID should return ErrNotFound, got: %v", err)
	}

	// A's strategy must be untouched.
	got, err := store.GetStrategy(ctx, userA.ID, scA.ID)
	if err != nil {
		t.Fatalf("reading A's strategy: %v", err)
	}
	if got.Name != scA.Name {
		t.Errorf("A's strategy name should remain unchanged, got %q", got.Name)
	}
}

// TestStrategyGetDeleteRejectOtherUsersRow covers the core property of multi-user
// isolation: when user B Get/Deletes user A's strategy, the error must be identical to
// the strategy simply not existing.
func TestStrategyGetDeleteRejectOtherUsersRow(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "strategies-get-delete-cross-a")
	userB := createTestUserForStorage(ctx, t, store, "strategies-get-delete-cross-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	sc := newTestStrategy(t, userA.ID)
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("saving strategy: %v", err)
	}

	if _, err := store.GetStrategy(ctx, userB.ID, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B reading A's strategy should return ErrNotFound just like reading a nonexistent ID, got: %v", err)
	}
	if err := store.DeleteStrategy(ctx, userB.ID, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B deleting A's strategy should return ErrNotFound, got: %v", err)
	}

	if _, err := store.GetStrategy(ctx, userA.ID, sc.ID); err != nil {
		t.Errorf("A's strategy should remain intact, but reading it failed: %v", err)
	}
}

// TestListStrategiesByStateAllUsersSpansMultipleUsers verifies the cross-user query
// method added for the multi-user concurrent execution rework: two real users each save
// a strategy in the target state plus a decoy strategy in a different state, and we
// assert the cross-user method returns both users' target-state strategies at once while
// the state filter still holds (the decoy strategies are not pulled in too) — this is
// direct proof that cmd/executor/cmd/signal-engine's multi-user mode can actually see
// "every user's strategies in this state," not just that the parameter was passed
// through.
func TestListStrategiesByStateAllUsersSpansMultipleUsers(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "strategies-all-users-a")
	userB := createTestUserForStorage(ctx, t, store, "strategies-all-users-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	scA := newTestStrategy(t, userA.ID)
	scA.State = types.StatePaperTrading
	if err := store.SaveStrategy(ctx, scA); err != nil {
		t.Fatalf("A saving strategy: %v", err)
	}

	scB := newTestStrategy(t, userB.ID)
	scB.State = types.StatePaperTrading
	if err := store.SaveStrategy(ctx, scB); err != nil {
		t.Fatalf("B saving strategy: %v", err)
	}

	// Decoy: another strategy of A's, not in PAPER_TRADING state — should not appear in the results.
	decoy := newTestStrategy(t, userA.ID)
	decoy.State = types.StateDraft
	if err := store.SaveStrategy(ctx, decoy); err != nil {
		t.Fatalf("saving decoy strategy: %v", err)
	}

	got, err := store.ListStrategiesByStateAllUsers(ctx, types.StatePaperTrading)
	if err != nil {
		t.Fatalf("cross-user query: %v", err)
	}

	gotIDs := make(map[string]string, len(got)) // id -> user_id
	for _, s := range got {
		gotIDs[s.ID] = s.UserID
	}
	if gotIDs[scA.ID] != userA.ID {
		t.Errorf("should include A's strategy with the correct UserID, got: %+v", gotIDs)
	}
	if gotIDs[scB.ID] != userB.ID {
		t.Errorf("should include B's strategy with the correct UserID, got: %+v", gotIDs)
	}
	if _, ok := gotIDs[decoy.ID]; ok {
		t.Errorf("state filtering should exclude the decoy strategy, but it showed up in the results: %+v", gotIDs)
	}
}

// TestGetStrategyAllUsersIgnoresOwnership is the one test in this file that "should not
// filter by user," the opposite direction from every other test here —
// GetStrategyAllUsers exists specifically for cmd/notifier, to look up the owning user
// from a StrategyID found in a Kafka decision, and must be able to read any user's
// strategy.
func TestGetStrategyAllUsersIgnoresOwnership(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "strategies-get-all-users-a")
	userB := createTestUserForStorage(ctx, t, store, "strategies-get-all-users-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	scA := newTestStrategy(t, userA.ID)
	scB := newTestStrategy(t, userB.ID)
	if err := store.SaveStrategy(ctx, scA); err != nil {
		t.Fatalf("saving A's strategy: %v", err)
	}
	if err := store.SaveStrategy(ctx, scB); err != nil {
		t.Fatalf("saving B's strategy: %v", err)
	}

	gotA, err := store.GetStrategyAllUsers(ctx, scA.ID)
	if err != nil || gotA.UserID != userA.ID {
		t.Errorf("should be able to read A's strategy without user identity, got: %+v err=%v", gotA, err)
	}
	gotB, err := store.GetStrategyAllUsers(ctx, scB.ID)
	if err != nil || gotB.UserID != userB.ID {
		t.Errorf("should be able to read B's strategy without user identity, got: %+v err=%v", gotB, err)
	}

	if _, err := store.GetStrategyAllUsers(ctx, idgen.NewUUID()); !errors.Is(err, ErrNotFound) {
		t.Errorf("a nonexistent strategy should return ErrNotFound, got: %v", err)
	}
}
