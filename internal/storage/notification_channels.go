package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// NotificationChannel is a saved notification channel configuration. EncryptedConfig/
// KeySalt/KeyNonce are the encrypted ciphertext (see internal/secretcrypto) — this layer
// only stores and retrieves the bytes; it doesn't care about the encryption algorithm,
// nor how many fields are packed into EncryptedConfig — that's the caller's
// responsibility (the webui package / cmd/notifier). UserID is the owning user of this
// channel.
//
// Key difference from BrokerProfile: BrokerProfile allows at most one is_active row per
// broker (only one set of credentials can be used against a given exchange at a time);
// NotificationChannel has no such exclusivity — the same kind can have any number of rows
// with is_enabled=true simultaneously (e.g. two webhooks, or separate web push
// subscriptions per device), which is semantically closer to a "subscription list" than
// "the one currently active configuration."
type NotificationChannel struct {
	ID              string
	UserID          string
	Kind            string // "email" | "telegram" | "webhook" | "webpush"
	Label           string
	KeyHint         string
	EncryptedConfig []byte
	KeySalt         []byte
	KeyNonce        []byte
	IsEnabled       bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SaveNotificationChannel inserts a new notification channel configuration (updating an
// existing configuration's EncryptedConfig is not supported — the convention for
// changing config is "delete and re-add," to avoid a partial field update accidentally
// leaving the ciphertext and salt/nonce out of sync; same rationale as
// SaveBrokerProfile). A newly inserted channel defaults to is_enabled=true (saving means
// enabling; if a user wants to keep it disabled at first, they can save it and then
// immediately disable it).
func (s *Store) SaveNotificationChannel(ctx context.Context, c NotificationChannel) error {
	const q = `
		INSERT INTO notification_channels
			(id, user_id, kind, label, key_hint, encrypted_config, key_salt, key_nonce, is_enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), now())`
	if _, err := s.pool.Exec(ctx, q,
		c.ID, c.UserID, c.Kind, c.Label, c.KeyHint, c.EncryptedConfig, c.KeySalt, c.KeyNonce, c.IsEnabled,
	); err != nil {
		return fmt.Errorf("saving notification channel: %w", err)
	}
	return nil
}

// ListNotificationChannels lists all of a user's saved notification channels (including
// disabled ones), ordered by creation time ascending, for display on the settings page
// and for cmd/notifier to look up which channels a user currently has configured (the
// caller filters on IsEnabled itself to decide whether to actually send).
func (s *Store) ListNotificationChannels(ctx context.Context, userID string) ([]NotificationChannel, error) {
	const q = `
		SELECT id, user_id, kind, label, key_hint, encrypted_config, key_salt, key_nonce, is_enabled, created_at, updated_at
		FROM notification_channels WHERE user_id = $1 ORDER BY created_at`
	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("querying notification channels: %w", err)
	}
	defer rows.Close()

	var out []NotificationChannel
	for rows.Next() {
		var c NotificationChannel
		if err := rows.Scan(&c.ID, &c.UserID, &c.Kind, &c.Label, &c.KeyHint,
			&c.EncryptedConfig, &c.KeySalt, &c.KeyNonce, &c.IsEnabled, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetNotificationChannel reads one configuration by ID. A userID mismatch (channel
// exists but belongs to a different user) returns ErrNotFound, same as the channel not
// existing at all.
func (s *Store) GetNotificationChannel(ctx context.Context, userID, id string) (NotificationChannel, error) {
	const q = `
		SELECT id, user_id, kind, label, key_hint, encrypted_config, key_salt, key_nonce, is_enabled, created_at, updated_at
		FROM notification_channels WHERE id = $1 AND user_id = $2`
	var c NotificationChannel
	err := s.pool.QueryRow(ctx, q, id, userID).Scan(&c.ID, &c.UserID, &c.Kind, &c.Label, &c.KeyHint,
		&c.EncryptedConfig, &c.KeySalt, &c.KeyNonce, &c.IsEnabled, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return NotificationChannel{}, fmt.Errorf("notification channel %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return NotificationChannel{}, fmt.Errorf("reading notification channel %s: %w", id, err)
	}
	return c, nil
}

// SetNotificationChannelEnabled toggles a channel's enabled/disabled state.
//
// Deliberately different from the BrokerProfile family of methods: BrokerProfile has no
// update path at all (only delete-and-re-add); here we specifically add this one update
// method — because toggling is_enabled never touches EncryptedConfig/KeySalt/KeyNonce,
// so there's no risk of "a partial field update leaves ciphertext and salt/nonce out of
// sync," which is the exact reason BrokerProfile refuses to offer an update path. That
// reason doesn't apply here.
func (s *Store) SetNotificationChannelEnabled(ctx context.Context, userID, id string, enabled bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE notification_channels SET is_enabled = $1, updated_at = now() WHERE id = $2 AND user_id = $3`,
		enabled, id, userID)
	if err != nil {
		return fmt.Errorf("toggling notification channel state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("notification channel %s: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteNotificationChannel deletes a saved channel configuration.
func (s *Store) DeleteNotificationChannel(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notification_channels WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("deleting notification channel: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("notification channel %s: %w", id, ErrNotFound)
	}
	return nil
}
