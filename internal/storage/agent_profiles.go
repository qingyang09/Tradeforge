package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AgentProfile is a saved LLM provider configuration. EncryptedAPIKey/
// KeySalt/KeyNonce are the encrypted ciphertext (see internal/secretcrypto);
// this layer only stores and retrieves bytes and doesn't concern itself with
// the encryption algorithm -- key management is the caller's (webui
// package's) responsibility, the storage package shouldn't know about
// concepts like a server-side master key. UserID is the owner of this
// config -- each user has at most one active config at a time (see the
// partial unique index in 007_agent_profiles_ownership.sql); different
// users don't affect each other.
type AgentProfile struct {
	ID              string
	UserID          string
	Label           string
	Provider        string
	Model           string
	BaseURL         string
	KeyHint         string
	EncryptedAPIKey []byte
	KeySalt         []byte
	KeyNonce        []byte
	IsActive        bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SaveAgentProfile inserts a new model config (updating an existing config
// is not supported -- the convention for changing a config is "delete and
// re-add," to avoid a partial field update accidentally leaving the
// ciphertext out of sync with its salt/nonce). When activate is true, it
// sets this config as the user's currently active one within the same
// transaction, deactivating all of that user's other configs -- different
// users don't affect each other.
func (s *Store) SaveAgentProfile(ctx context.Context, p AgentProfile, activate bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // Rollback is a no-op after a successful commit

	const insertQ = `
		INSERT INTO agent_profiles
			(id, user_id, label, provider, model, base_url, key_hint, encrypted_api_key, key_salt, key_nonce, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, false, now(), now())`
	if _, err := tx.Exec(ctx, insertQ,
		p.ID, p.UserID, p.Label, p.Provider, p.Model, p.BaseURL, p.KeyHint, p.EncryptedAPIKey, p.KeySalt, p.KeyNonce,
	); err != nil {
		return fmt.Errorf("save model config: %w", err)
	}

	if activate {
		if err := activateProfileTx(ctx, tx, p.UserID, p.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ActivateAgentProfile sets one config as the user's currently active one,
// deactivating all others under that user -- a partial unique index (see
// 007_agent_profiles_ownership.sql) guarantees at most one row with
// is_active = true per user at any time; this method uses a transaction to
// explicitly "turn everything off first, then turn this one on," rather
// than relying on an index conflict as a backstop. A userID mismatch (this
// config exists but belongs to a different user) returns ErrNotFound, same
// as if the config didn't exist at all -- this keeps the caller from
// probing whether "this ID exists, it's just not yours".
func (s *Store) ActivateAgentProfile(ctx context.Context, userID, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if err := activateProfileTx(ctx, tx, userID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func activateProfileTx(ctx context.Context, tx pgx.Tx, userID, id string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE agent_profiles SET is_active = false, updated_at = now() WHERE user_id = $1 AND is_active`, userID,
	); err != nil {
		return fmt.Errorf("clear existing active config: %w", err)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE agent_profiles SET is_active = true, updated_at = now() WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("set active config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("model config %s: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteAgentProfile deletes a saved config. Deleting the currently active
// one doesn't automatically activate another -- the UI falls back to an
// "unconfigured" state, avoiding a silent switch to another key the user
// might not have noticed and might not have wanted.
func (s *Store) DeleteAgentProfile(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM agent_profiles WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete model config: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("model config %s: %w", id, ErrNotFound)
	}
	return nil
}

// GetAgentProfile reads a config by ID -- used before activating/deleting to
// confirm it actually exists, and used during activation to pull out the
// encrypted API key for decryption.
func (s *Store) GetAgentProfile(ctx context.Context, userID, id string) (AgentProfile, error) {
	const q = `
		SELECT id, user_id, label, provider, model, base_url, key_hint, encrypted_api_key, key_salt, key_nonce, is_active, created_at, updated_at
		FROM agent_profiles WHERE id = $1 AND user_id = $2`
	var p AgentProfile
	err := s.pool.QueryRow(ctx, q, id, userID).Scan(&p.ID, &p.UserID, &p.Label, &p.Provider, &p.Model, &p.BaseURL, &p.KeyHint,
		&p.EncryptedAPIKey, &p.KeySalt, &p.KeyNonce, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentProfile{}, fmt.Errorf("model config %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return AgentProfile{}, fmt.Errorf("read model config %s: %w", id, err)
	}
	return p, nil
}

// ListAgentProfiles lists all of the user's saved model configs, ordered by
// creation time ascending, for display on the settings page.
func (s *Store) ListAgentProfiles(ctx context.Context, userID string) ([]AgentProfile, error) {
	const q = `
		SELECT id, user_id, label, provider, model, base_url, key_hint, encrypted_api_key, key_salt, key_nonce, is_active, created_at, updated_at
		FROM agent_profiles WHERE user_id = $1 ORDER BY created_at`
	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("query model configs: %w", err)
	}
	defer rows.Close()

	var out []AgentProfile
	for rows.Next() {
		var p AgentProfile
		if err := rows.Scan(&p.ID, &p.UserID, &p.Label, &p.Provider, &p.Model, &p.BaseURL, &p.KeyHint,
			&p.EncryptedAPIKey, &p.KeySalt, &p.KeyNonce, &p.IsActive, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ActiveAgentProfile reads the user's currently active config, returning
// ErrNotFound if none is active.
func (s *Store) ActiveAgentProfile(ctx context.Context, userID string) (AgentProfile, error) {
	const q = `
		SELECT id, user_id, label, provider, model, base_url, key_hint, encrypted_api_key, key_salt, key_nonce, is_active, created_at, updated_at
		FROM agent_profiles WHERE user_id = $1 AND is_active LIMIT 1`
	var p AgentProfile
	err := s.pool.QueryRow(ctx, q, userID).Scan(&p.ID, &p.UserID, &p.Label, &p.Provider, &p.Model, &p.BaseURL, &p.KeyHint,
		&p.EncryptedAPIKey, &p.KeySalt, &p.KeyNonce, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentProfile{}, fmt.Errorf("active model config: %w", ErrNotFound)
	}
	if err != nil {
		return AgentProfile{}, fmt.Errorf("read active model config: %w", err)
	}
	return p, nil
}
