package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrEmailTaken indicates the email is already registered at signup (case-insensitive).
var ErrEmailTaken = errors.New("email already registered")

// User is a platform account. PasswordHash is a bcrypt hash -- this is the
// hashing algorithm for the login password, a completely separate thing
// from the scrypt-derived key that internal/secretcrypto uses to AES-encrypt
// stored exchange/LLM credentials. Don't conflate the two.
type User struct {
	ID           string
	Email        string
	PasswordHash []byte
	// PreferredLang is the account's standing UI language preference ("en"/
	// "zh", see migrations/011_users_preferred_lang.sql). The webui's
	// language toggle writes it for a logged-in user (see
	// internal/webui/lang.go's handleSetLang), finishLogin reads it back to
	// set the tf_lang cookie so the choice follows the account across
	// devices, and cmd/notifier reads it to send each user's alerts in their
	// own language. Deliberately a plain string here, not internal/i18n.Lang
	// -- this package stays a thin persistence layer and doesn't depend on
	// i18n; callers normalize with i18n.ParseLang.
	PreferredLang string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// defaultPreferredLang mirrors internal/i18n.DefaultLang's value ("zh") and
// 011_users_preferred_lang.sql's column default -- duplicated as a literal
// rather than imported, since this package deliberately doesn't depend on
// internal/i18n.
const defaultPreferredLang = "zh"

// CreateUser inserts a new account. A unique-email conflict (case-insensitive,
// via idx_users_email_lower in 005_users.sql) is mapped to ErrEmailTaken, so
// the caller can surface a factual "this email is already registered"
// message instead of leaking an internal detail like "database unique
// constraint violation".
//
// An empty u.PreferredLang is normalized to defaultPreferredLang before the
// insert -- the column's own DEFAULT 'zh' only kicks in when the column is
// omitted from the INSERT entirely, not for an explicit empty string, so a
// caller that doesn't set this field (most existing callers) would otherwise
// persist "" instead of getting the intended default.
func (s *Store) CreateUser(ctx context.Context, u User) error {
	preferredLang := u.PreferredLang
	if preferredLang == "" {
		preferredLang = defaultPreferredLang
	}
	const q = `
		INSERT INTO users (id, email, password_hash, preferred_lang, created_at, updated_at)
		VALUES ($1, $2, $3, $4, now(), now())`
	if _, err := s.pool.Exec(ctx, q, u.ID, u.Email, u.PasswordHash, preferredLang); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrEmailTaken
		}
		return fmt.Errorf("create account: %w", err)
	}
	return nil
}

// GetUserByEmail looks up an account by email (case-insensitive), used at
// login to check the password.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	const q = `
		SELECT id, email, password_hash, preferred_lang, created_at, updated_at
		FROM users WHERE lower(email) = lower($1)`
	var u User
	err := s.pool.QueryRow(ctx, q, email).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.PreferredLang, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, fmt.Errorf("account %s: %w", email, ErrNotFound)
	}
	if err != nil {
		return User{}, fmt.Errorf("read account: %w", err)
	}
	return u, nil
}

// GetUser looks up an account by ID.
func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	const q = `
		SELECT id, email, password_hash, preferred_lang, created_at, updated_at
		FROM users WHERE id = $1`
	var u User
	err := s.pool.QueryRow(ctx, q, id).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.PreferredLang, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, fmt.Errorf("account %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return User{}, fmt.Errorf("read account: %w", err)
	}
	return u, nil
}

// SetUserPreferredLang updates a user's standing language preference. Called
// by internal/webui/lang.go's handleSetLang whenever a logged-in user
// toggles the language switcher; cmd/notifier later reads it back via
// GetUser to pick which language to send that user's alerts in, since a
// background process has no per-request cookie to resolve a language from.
func (s *Store) SetUserPreferredLang(ctx context.Context, userID, lang string) error {
	const q = `UPDATE users SET preferred_lang = $2, updated_at = now() WHERE id = $1`
	if _, err := s.pool.Exec(ctx, q, userID, lang); err != nil {
		return fmt.Errorf("update preferred language: %w", err)
	}
	return nil
}
