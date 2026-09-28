//go:build integration

// Requires a Postgres started via docker-compose, with
// migrations/009_notification_channels.sql and migrations/010_notification_deliveries.sql
// already applied:
//
//	docker compose up -d
//	go test -tags=integration ./internal/storage/... -run NotificationChannel -v
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradeforge/internal/config"
	"tradeforge/pkg/idgen"
)

func newChannelForTest(userID, kind, label string) NotificationChannel {
	return NotificationChannel{
		ID: idgen.NewUUID(), UserID: userID, Kind: kind, Label: label, KeyHint: "hint",
		EncryptedConfig: []byte{0x01, 0x02, 0x03},
		KeySalt:         []byte{0xaa, 0xbb},
		KeyNonce:        []byte{0xcc, 0xdd, 0xee},
		IsEnabled:       true,
	}
}

// TestNotificationChannelsAllowMultipleEnabledOfSameKind directly proves the core
// semantics of this design: the same user and the same kind can have multiple rows with
// is_enabled=true at once — deliberately unlike broker_profiles' "at most one active row
// per broker" — so this must be verified directly, not just against a fake store.
func TestNotificationChannelsAllowMultipleEnabledOfSameKind(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres (did you run docker compose up -d and apply migrations 009/010?): %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "notification-channels-multi-enabled")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	a := newChannelForTest(u.ID, "webhook", "first webhook")
	b := newChannelForTest(u.ID, "webhook", "second webhook")
	if err := store.SaveNotificationChannel(ctx, a); err != nil {
		t.Fatalf("saving channel A: %v", err)
	}
	if err := store.SaveNotificationChannel(ctx, b); err != nil {
		t.Fatalf("saving channel B: %v", err)
	}

	list, err := store.ListNotificationChannels(ctx, u.ID)
	if err != nil {
		t.Fatalf("listing channels: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected two channels, got %d", len(list))
	}
	enabledCount := 0
	for _, c := range list {
		if c.IsEnabled {
			enabledCount++
		}
	}
	if enabledCount != 2 {
		t.Errorf("both channels of the same kind should remain is_enabled=true, only %d were enabled", enabledCount)
	}
}

func TestNotificationChannelToggleAndDelete(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "notification-channels-toggle-delete")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	ch := newChannelForTest(u.ID, "email", "my email")
	if err := store.SaveNotificationChannel(ctx, ch); err != nil {
		t.Fatalf("saving channel: %v", err)
	}

	if err := store.SetNotificationChannelEnabled(ctx, u.ID, ch.ID, false); err != nil {
		t.Fatalf("disabling channel: %v", err)
	}
	got, err := store.GetNotificationChannel(ctx, u.ID, ch.ID)
	if err != nil || got.IsEnabled {
		t.Errorf("should be is_enabled=false after disabling, got: %+v err=%v", got, err)
	}

	if err := store.SetNotificationChannelEnabled(ctx, u.ID, ch.ID, true); err != nil {
		t.Fatalf("re-enabling channel: %v", err)
	}
	got, err = store.GetNotificationChannel(ctx, u.ID, ch.ID)
	if err != nil || !got.IsEnabled {
		t.Errorf("should be is_enabled=true after re-enabling, got: %+v err=%v", got, err)
	}

	if err := store.DeleteNotificationChannel(ctx, u.ID, ch.ID); err != nil {
		t.Fatalf("deleting channel: %v", err)
	}
	if _, err := store.GetNotificationChannel(ctx, u.ID, ch.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("should return ErrNotFound after deletion, got: %v", err)
	}
}

// TestNotificationChannelGetSetDeleteRejectOtherUsersRow covers the core property of
// multi-user isolation: when user B Get/toggles/Deletes user A's channel, the error must
// be identical to the row simply not existing.
func TestNotificationChannelGetSetDeleteRejectOtherUsersRow(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "notification-channels-cross-a")
	userB := createTestUserForStorage(ctx, t, store, "notification-channels-cross-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()

	ch := newChannelForTest(userA.ID, "webhook", "A's webhook")
	if err := store.SaveNotificationChannel(ctx, ch); err != nil {
		t.Fatalf("saving channel: %v", err)
	}

	if _, err := store.GetNotificationChannel(ctx, userB.ID, ch.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B reading A's channel should return ErrNotFound, got: %v", err)
	}
	if err := store.SetNotificationChannelEnabled(ctx, userB.ID, ch.ID, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("B disabling A's channel should return ErrNotFound, got: %v", err)
	}
	if err := store.DeleteNotificationChannel(ctx, userB.ID, ch.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B deleting A's channel should return ErrNotFound, got: %v", err)
	}

	got, err := store.GetNotificationChannel(ctx, userA.ID, ch.ID)
	if err != nil || !got.IsEnabled {
		t.Errorf("A's channel should remain intact, got: %+v err=%v", got, err)
	}
}

// TestAlreadyDeliveredTracksSentButNotFailed verifies the three idempotency-check
// cases: no record, only a failed record, and a successful record — only a successful
// record should make AlreadyDelivered return true; a failed record must not block a
// retry.
func TestAlreadyDeliveredTracksSentButNotFailed(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connecting to Postgres: %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "notification-deliveries-idempotent")
	sc := newTestStrategy(t, u.ID)
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("cleaning up test data: %v", err)
		}
	}()
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("saving strategy: %v", err)
	}

	// The decisions table has a foreign key constraint, so insert a minimal usable
	// decision row directly by hand.
	decisionID := idgen.NewUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO decisions (id, strategy_id, symbol, direction, score, triggered, reason, price, signals, bar_time)
		VALUES ($1, $2, 'BTCUSDT', 'LONG', 0.8, true, 'test', 100, '[]', now())`,
		decisionID, sc.ID); err != nil {
		t.Fatalf("writing test decision: %v", err)
	}

	ch := newChannelForTest(u.ID, "email", "test channel")
	if err := store.SaveNotificationChannel(ctx, ch); err != nil {
		t.Fatalf("saving channel: %v", err)
	}

	already, err := store.AlreadyDelivered(ctx, decisionID, ch.ID)
	if err != nil || already {
		t.Fatalf("should return false when there is no delivery record yet, got already=%v err=%v", already, err)
	}

	if err := store.RecordDelivery(ctx, decisionID, ch.ID, "failed", "boom"); err != nil {
		t.Fatalf("recording failed delivery: %v", err)
	}
	already, err = store.AlreadyDelivered(ctx, decisionID, ch.ID)
	if err != nil || already {
		t.Fatalf("should still return false with only a failed record (retry allowed), got already=%v err=%v", already, err)
	}

	if err := store.RecordDelivery(ctx, decisionID, ch.ID, "sent", ""); err != nil {
		t.Fatalf("recording successful delivery: %v", err)
	}
	already, err = store.AlreadyDelivered(ctx, decisionID, ch.ID)
	if err != nil || !already {
		t.Fatalf("should return true once there is a successful record, got already=%v err=%v", already, err)
	}
}
