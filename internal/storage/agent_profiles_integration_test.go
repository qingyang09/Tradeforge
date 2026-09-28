//go:build integration

// Requires a Postgres started via docker-compose, with migrations/002_agent_profiles.sql already applied:
//
//	docker compose up -d
//	go test -tags=integration ./internal/storage/... -run AgentProfile -v
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradeforge/internal/config"
	"tradeforge/pkg/idgen"
)

// createTestUserForStorage inserts a user row dedicated to this test --
// agent_profiles/broker_profiles' user_id foreign key requires an actual
// row in users, we can't just make up a random UUID. The email is built
// from the test name plus a random ID, to avoid colliding with the unique
// index when multiple integration tests run concurrently.
func createTestUserForStorage(ctx context.Context, t *testing.T, store *Store, label string) User {
	t.Helper()
	u := User{
		ID: idgen.NewUUID(), Email: label + "-" + idgen.NewUUID() + "@integration.test",
		PasswordHash: []byte("x"),
	}
	if err := store.CreateUser(ctx, u); err != nil {
		t.Fatalf("create test user: %v", err)
	}
	return u
}

func newProfileForTest(userID, label string) AgentProfile {
	return AgentProfile{
		ID: idgen.NewUUID(), UserID: userID, Label: label, Provider: "anthropic", Model: "claude-opus-5",
		BaseURL: "", KeyHint: "sk-a…b12d",
		EncryptedAPIKey: []byte{0x01, 0x02, 0x03, 0xff},
		KeySalt:         []byte{0xaa, 0xbb},
		KeyNonce:        []byte{0xcc, 0xdd, 0xee},
	}
}

func TestAgentProfilesRoundTripAndActivationSwitches(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connect to Postgres (did you run docker compose up -d and apply migration 002?): %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "agent-profiles-roundtrip")
	a := newProfileForTest(u.ID, "integration test A")
	b := newProfileForTest(u.ID, "integration test B")
	// defer instead of t.Cleanup: callbacks registered with t.Cleanup run
	// after the test function body returns, which is after the
	// defer store.Close() already registered earlier in the body -- using
	// store.pool here at that point would get a "pool already closed"
	// error, cleanup would fail outright, and the garbage data would be
	// left behind in the shared dev database (this actually happened once:
	// a leftover fake-ciphertext row caused LoadActiveAgentProfile to panic
	// on a real process's startup). defer is LIFO, so registering this
	// after defer store.Close() makes it run before that close.
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("clean up test data, please manually check the users/agent_profiles tables: %v", err)
		}
	}()

	if err := store.SaveAgentProfile(ctx, a, true); err != nil {
		t.Fatalf("save config A: %v", err)
	}
	if err := store.SaveAgentProfile(ctx, b, true); err != nil {
		t.Fatalf("save config B: %v", err)
	}

	// B was saved and activated last, so it should be the currently active one; A should be automatically deactivated.
	active, err := store.ActiveAgentProfile(ctx, u.ID)
	if err != nil {
		t.Fatalf("read active config: %v", err)
	}
	if active.ID != b.ID {
		t.Errorf("active config should be B, got: %+v", active)
	}

	gotA, err := store.GetAgentProfile(ctx, u.ID, a.ID)
	if err != nil {
		t.Fatalf("read config A: %v", err)
	}
	if gotA.IsActive {
		t.Error("after saving and activating B, A should have been switched to inactive")
	}
	if string(gotA.EncryptedAPIKey) != string(a.EncryptedAPIKey) {
		t.Errorf("ciphertext did not round-trip: got=%v want=%v", gotA.EncryptedAPIKey, a.EncryptedAPIKey)
	}

	// Switch back to A.
	if err := store.ActivateAgentProfile(ctx, u.ID, a.ID); err != nil {
		t.Fatalf("activate config A: %v", err)
	}
	active, err = store.ActiveAgentProfile(ctx, u.ID)
	if err != nil || active.ID != a.ID {
		t.Errorf("after activating A, active config should be A, got: %+v err=%v", active, err)
	}

	all, err := store.ListAgentProfiles(ctx, u.ID)
	if err != nil {
		t.Fatalf("list all configs: %v", err)
	}
	if len(all) < 2 {
		t.Errorf("should include at least the two configs just saved, got %d", len(all))
	}

	if err := store.DeleteAgentProfile(ctx, u.ID, b.ID); err != nil {
		t.Fatalf("delete config B: %v", err)
	}
	if _, err := store.GetAgentProfile(ctx, u.ID, b.ID); err == nil {
		t.Error("config B should not be found after deletion")
	}
}

func TestActivateAgentProfileRejectsUnknownID(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "agent-profiles-unknown-id")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("clean up test data: %v", err)
		}
	}()

	if err := store.ActivateAgentProfile(ctx, u.ID, idgen.NewUUID()); err == nil {
		t.Fatal("activating a nonexistent config ID should return an error")
	}
}

// TestAgentProfileGetActivateDeleteRejectOtherUsersRow tests the core
// property of multi-user isolation: when user B calls Get/Activate/Delete
// on user A's row, the resulting error must be exactly the same as "this
// row doesn't exist at all" -- the requester must not be able to probe,
// via the error type, that "this row exists, it's just not yours".
func TestAgentProfileGetActivateDeleteRejectOtherUsersRow(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "agent-profiles-cross-a")
	userB := createTestUserForStorage(ctx, t, store, "agent-profiles-cross-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("clean up test data: %v", err)
		}
	}()

	a := newProfileForTest(userA.ID, "A's profile")
	if err := store.SaveAgentProfile(ctx, a, true); err != nil {
		t.Fatalf("save profile A: %v", err)
	}

	if _, err := store.GetAgentProfile(ctx, userB.ID, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B reading A's profile should return ErrNotFound just like reading a nonexistent ID, got: %v", err)
	}
	if err := store.ActivateAgentProfile(ctx, userB.ID, a.ID); err == nil {
		t.Error("B should not be able to activate A's profile")
	}
	if err := store.DeleteAgentProfile(ctx, userB.ID, a.ID); err == nil {
		t.Error("B should not be able to delete A's profile")
	}

	// Confirm A's profile is really still there and wasn't accidentally
	// modified by B's failed operations.
	gotA, err := store.GetAgentProfile(ctx, userA.ID, a.ID)
	if err != nil || !gotA.IsActive {
		t.Errorf("A's profile should remain intact and active, got: %+v err=%v", gotA, err)
	}
}
