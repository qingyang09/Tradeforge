package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// BrokerProfile is a saved set of credentials for an exchange order-routing channel.
// EncryptedCredentials/KeySalt/KeyNonce are the encrypted ciphertext (see
// internal/secretcrypto) — this layer only stores and retrieves the bytes; it doesn't
// care about the encryption algorithm, nor how many fields are packed into
// EncryptedCredentials — that's the caller's responsibility (the webui package). UserID
// is the owning user of this configuration.
type BrokerProfile struct {
	ID                   string
	UserID               string
	Label                string
	Broker               string
	KeyHint              string
	EncryptedCredentials []byte
	KeySalt              []byte
	KeyNonce             []byte
	IsActive             bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// SaveBrokerProfile inserts a new exchange configuration (updating an existing
// configuration is not supported — the convention for changing config is "delete and
// re-add," to avoid a partial field update accidentally leaving the ciphertext and
// salt/nonce out of sync). When activate is true, within the same transaction it also
// sets this row as the user's currently-active configuration for that broker, and
// deactivates all of the user's other configurations for that broker — this has no
// effect across different users, or across a single user's different brokers.
func (s *Store) SaveBrokerProfile(ctx context.Context, p BrokerProfile, activate bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // Rollback is a no-op once Commit has succeeded

	const insertQ = `
		INSERT INTO broker_profiles
			(id, user_id, label, broker, key_hint, encrypted_credentials, key_salt, key_nonce, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, false, now(), now())`
	if _, err := tx.Exec(ctx, insertQ,
		p.ID, p.UserID, p.Label, p.Broker, p.KeyHint, p.EncryptedCredentials, p.KeySalt, p.KeyNonce,
	); err != nil {
		return fmt.Errorf("saving broker profile: %w", err)
	}

	if activate {
		if err := activateBrokerProfileTx(ctx, tx, p.UserID, p.ID, p.Broker); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ActivateBrokerProfile sets a given configuration as the user's currently-active one
// for its broker, deactivating all of the user's other configurations for that same
// broker — it first looks up which broker the row belongs to (also verifying it
// actually belongs to the current user), then activates grouped by "user + broker." A
// userID mismatch (configuration exists but belongs to a different user) returns
// ErrNotFound, same as the configuration not existing at all.
func (s *Store) ActivateBrokerProfile(ctx context.Context, userID, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var broker string
	if err := tx.QueryRow(ctx,
		`SELECT broker FROM broker_profiles WHERE id = $1 AND user_id = $2`, id, userID,
	).Scan(&broker); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("broker profile %s: %w", id, ErrNotFound)
		}
		return fmt.Errorf("querying broker profile: %w", err)
	}

	if err := activateBrokerProfileTx(ctx, tx, userID, id, broker); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func activateBrokerProfileTx(ctx context.Context, tx pgx.Tx, userID, id, broker string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE broker_profiles SET is_active = false, updated_at = now() WHERE user_id = $1 AND broker = $2 AND is_active`,
		userID, broker,
	); err != nil {
		return fmt.Errorf("clearing previously active profile: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE broker_profiles SET is_active = true, updated_at = now() WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("setting active profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("broker profile %s: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteBrokerProfile deletes a saved configuration.
func (s *Store) DeleteBrokerProfile(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM broker_profiles WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("deleting broker profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("broker profile %s: %w", id, ErrNotFound)
	}
	return nil
}

// GetBrokerProfile reads one configuration by ID.
func (s *Store) GetBrokerProfile(ctx context.Context, userID, id string) (BrokerProfile, error) {
	const q = `
		SELECT id, user_id, label, broker, key_hint, encrypted_credentials, key_salt, key_nonce, is_active, created_at, updated_at
		FROM broker_profiles WHERE id = $1 AND user_id = $2`
	var p BrokerProfile
	err := s.pool.QueryRow(ctx, q, id, userID).Scan(&p.ID, &p.UserID, &p.Label, &p.Broker, &p.KeyHint,
		&p.EncryptedCredentials, &p.KeySalt, &p.KeyNonce, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BrokerProfile{}, fmt.Errorf("broker profile %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return BrokerProfile{}, fmt.Errorf("reading broker profile %s: %w", id, err)
	}
	return p, nil
}

// ListBrokerProfiles lists all of a user's saved exchange configurations (across every
// broker the user has), ordered by creation time ascending, for display on the settings
// page.
func (s *Store) ListBrokerProfiles(ctx context.Context, userID string) ([]BrokerProfile, error) {
	const q = `
		SELECT id, user_id, label, broker, key_hint, encrypted_credentials, key_salt, key_nonce, is_active, created_at, updated_at
		FROM broker_profiles WHERE user_id = $1 ORDER BY created_at`
	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("querying broker profiles: %w", err)
	}
	defer rows.Close()

	var out []BrokerProfile
	for rows.Next() {
		var p BrokerProfile
		if err := rows.Scan(&p.ID, &p.UserID, &p.Label, &p.Broker, &p.KeyHint,
			&p.EncryptedCredentials, &p.KeySalt, &p.KeyNonce, &p.IsActive, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ActiveBrokerProfile reads the user's currently-active configuration for a given order
// routing channel, returning ErrNotFound when none is active — cmd/executor uses this at
// startup to determine whether it can recover credentials from the database.
func (s *Store) ActiveBrokerProfile(ctx context.Context, userID, broker string) (BrokerProfile, error) {
	const q = `
		SELECT id, user_id, label, broker, key_hint, encrypted_credentials, key_salt, key_nonce, is_active, created_at, updated_at
		FROM broker_profiles WHERE user_id = $1 AND broker = $2 AND is_active LIMIT 1`
	var p BrokerProfile
	err := s.pool.QueryRow(ctx, q, userID, broker).Scan(&p.ID, &p.UserID, &p.Label, &p.Broker, &p.KeyHint,
		&p.EncryptedCredentials, &p.KeySalt, &p.KeyNonce, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BrokerProfile{}, fmt.Errorf("active broker profile for %s: %w", broker, ErrNotFound)
	}
	if err != nil {
		return BrokerProfile{}, fmt.Errorf("reading active broker profile for %s: %w", broker, err)
	}
	return p, nil
}
