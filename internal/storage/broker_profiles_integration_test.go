//go:build integration

// Requires a Postgres started via docker-compose, with migrations/004_broker_profiles.sql
// already applied:
//
//	docker compose up -d
//	go test -tags=integration ./internal/storage/... -run BrokerProfile -v
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradeforge/internal/config"
	"tradeforge/pkg/idgen"
)

func newBrokerProfileForTest(userID, label, broker string) BrokerProfile {
	return BrokerProfile{
		ID: idgen.NewUUID(), UserID: userID, Label: label, Broker: broker, KeyHint: "sk-a…b12d",
		EncryptedCredentials: []byte{0x01, 0x02, 0x03, 0xff},
		KeySalt:              []byte{0xaa, 0xbb},
		KeyNonce:             []byte{0xcc, 0xdd, 0xee},
	}
}

func TestBrokerProfilesRoundTripAndActivationSwitchesWithinSameBroker(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres (did you run docker compose up -d and apply migration 004?): %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "broker-profiles-roundtrip")
	a := newBrokerProfileForTest(u.ID, "integration test A", "okx-demo")
	b := newBrokerProfileForTest(u.ID, "integration test B", "okx-demo")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("cleaning up test data, check the users/broker_profiles tables manually: %v", err)
		}
	}()

	if err := store.SaveBrokerProfile(ctx, a, true); err != nil {
		t.Fatalf("saving profile A: %v", err)
	}
	if err := store.SaveBrokerProfile(ctx, b, true); err != nil {
		t.Fatalf("saving profile B: %v", err)
	}

	active, err := store.ActiveBrokerProfile(ctx, u.ID, "okx-demo")
	if err != nil {
		t.Fatalf("reading currently active profile: %v", err)
	}
	if active.ID != b.ID {
		t.Errorf("currently active profile should be B, got: %+v", active)
	}

	gotA, err := store.GetBrokerProfile(ctx, u.ID, a.ID)
	if err != nil {
		t.Fatalf("reading profile A: %v", err)
	}
	if gotA.IsActive {
		t.Error("after saving and activating B, A should have been switched to inactive")
	}
	if string(gotA.EncryptedCredentials) != string(a.EncryptedCredentials) {
		t.Errorf("ciphertext round-trip mismatch: got=%v want=%v", gotA.EncryptedCredentials, a.EncryptedCredentials)
	}

	if err := store.ActivateBrokerProfile(ctx, u.ID, a.ID); err != nil {
		t.Fatalf("activating profile A: %v", err)
	}
	active, err = store.ActiveBrokerProfile(ctx, u.ID, "okx-demo")
	if err != nil || active.ID != a.ID {
		t.Errorf("after activating A, currently active profile should be A, got: %+v err=%v", active, err)
	}

	all, err := store.ListBrokerProfiles(ctx, u.ID)
	if err != nil {
		t.Fatalf("listing all profiles: %v", err)
	}
	if len(all) < 2 {
		t.Errorf("should include at least the two profiles just saved, got %d", len(all))
	}

	if err := store.DeleteBrokerProfile(ctx, u.ID, b.ID); err != nil {
		t.Fatalf("deleting profile B: %v", err)
	}
	if _, err := store.GetBrokerProfile(ctx, u.ID, b.ID); err == nil {
		t.Error("profile B should not be found after deletion")
	}
}

// TestBrokerProfilesActivationIsScopedPerBroker covers the biggest design difference
// between this table and agent_profiles: agent_profiles has only one active
// configuration per user, while broker_profiles groups by "user + broker" — activating a
// configuration for one broker must not affect a configuration already active for the
// same user's other broker.
func TestBrokerProfilesActivationIsScopedPerBroker(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "broker-profiles-per-broker")
	okx := newBrokerProfileForTest(u.ID, "integration test OKX", "okx-demo")
	binance := newBrokerProfileForTest(u.ID, "integration test Binance", "binance-testnet")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("cleaning up test data, check the users/broker_profiles tables manually: %v", err)
		}
	}()

	if err := store.SaveBrokerProfile(ctx, okx, true); err != nil {
		t.Fatalf("saving OKX profile: %v", err)
	}
	if err := store.SaveBrokerProfile(ctx, binance, true); err != nil {
		t.Fatalf("saving Binance profile: %v", err)
	}

	activeOKX, err := store.ActiveBrokerProfile(ctx, u.ID, "okx-demo")
	if err != nil || activeOKX.ID != okx.ID {
		t.Errorf("activating Binance should not affect OKX's currently active profile, got: %+v err=%v", activeOKX, err)
	}
	activeBinance, err := store.ActiveBrokerProfile(ctx, u.ID, "binance-testnet")
	if err != nil || activeBinance.ID != binance.ID {
		t.Errorf("Binance's currently active profile should be the one just saved, got: %+v err=%v", activeBinance, err)
	}
}

func TestActivateBrokerProfileRejectsUnknownID(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "broker-profiles-unknown-id")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	if err := store.ActivateBrokerProfile(ctx, u.ID, idgen.NewUUID()); err == nil {
		t.Fatal("activating a nonexistent profile ID should return an error")
	}
}

// TestBrokerProfileGetActivateDeleteRejectOtherUsersRow covers the core property of
// multi-user isolation: when user B Get/Activate/Deletes user A's broker profile, the
// error must be identical to the row simply not existing.
func TestBrokerProfileGetActivateDeleteRejectOtherUsersRow(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "broker-profiles-cross-a")
	userB := createTestUserForStorage(ctx, t, store, "broker-profiles-cross-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	a := newBrokerProfileForTest(userA.ID, "A's profile", "okx-demo")
	if err := store.SaveBrokerProfile(ctx, a, true); err != nil {
		t.Fatalf("saving profile A: %v", err)
	}

	if _, err := store.GetBrokerProfile(ctx, userB.ID, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B reading A's profile should return ErrNotFound just like reading a nonexistent ID, got: %v", err)
	}
	if err := store.ActivateBrokerProfile(ctx, userB.ID, a.ID); err == nil {
		t.Error("B should not be able to activate A's profile")
	}
	if err := store.DeleteBrokerProfile(ctx, userB.ID, a.ID); err == nil {
		t.Error("B should not be able to delete A's profile")
	}

	gotA, err := store.GetBrokerProfile(ctx, userA.ID, a.ID)
	if err != nil || !gotA.IsActive {
		t.Errorf("A's profile should remain intact and active, got: %+v err=%v", gotA, err)
	}
}
