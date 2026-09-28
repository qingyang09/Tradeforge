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
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateUser inserts a new account. A unique-email conflict (case-insensitive,
// via idx_users_email_lower in 005_users.sql) is mapped to ErrEmailTaken, so
// the caller can surface a factual "this email is already registered"
// message instead of leaking an internal detail like "database unique
// constraint violation".
func (s *Store) CreateUser(ctx context.Context, u User) error {
	const q = `
		INSERT INTO users (id, email, password_hash, created_at, updated_at)
		VALUES ($1, $2, $3, now(), now())`
	if _, err := s.pool.Exec(ctx, q, u.ID, u.Email, u.PasswordHash); err != nil {
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
		SELECT id, email, password_hash, created_at, updated_at
		FROM users WHERE lower(email) = lower($1)`
	var u User
	err := s.pool.QueryRow(ctx, q, email).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
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
		SELECT id, email, password_hash, created_at, updated_at
		FROM users WHERE id = $1`
	var u User
	err := s.pool.QueryRow(ctx, q, id).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, fmt.Errorf("account %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return User{}, fmt.Errorf("read account: %w", err)
	}
	return u, nil
}
