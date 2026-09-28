package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrEmailTaken 表示注册时邮箱已经被占用（大小写不敏感）。
var ErrEmailTaken = errors.New("邮箱已被注册")

// User 是一个平台账号。PasswordHash 是 bcrypt 哈希——这是登录口令的哈希算法，
// 跟 internal/secretcrypto 给存储的交易所/LLM 凭据做 AES 加密用的 scrypt 派生密钥
// 是两件完全独立的事，不要混用。
type User struct {
	ID           string
	Email        string
	PasswordHash []byte
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateUser 插入一个新账号。邮箱唯一性冲突（大小写不敏感，见 005_users.sql 的
// idx_users_email_lower）映射成 ErrEmailTaken，调用方据此给出"该邮箱已注册"这类
// 事实性提示，不是泄露"数据库唯一约束冲突"这种内部细节。
func (s *Store) CreateUser(ctx context.Context, u User) error {
	const q = `
		INSERT INTO users (id, email, password_hash, created_at, updated_at)
		VALUES ($1, $2, $3, now(), now())`
	if _, err := s.pool.Exec(ctx, q, u.ID, u.Email, u.PasswordHash); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrEmailTaken
		}
		return fmt.Errorf("创建账号失败：%w", err)
	}
	return nil
}

// GetUserByEmail 按邮箱查账号（大小写不敏感），登录时用来核对密码。
func (s *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	const q = `
		SELECT id, email, password_hash, created_at, updated_at
		FROM users WHERE lower(email) = lower($1)`
	var u User
	err := s.pool.QueryRow(ctx, q, email).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, fmt.Errorf("账号 %s：%w", email, ErrNotFound)
	}
	if err != nil {
		return User{}, fmt.Errorf("读取账号失败：%w", err)
	}
	return u, nil
}

// GetUser 按 ID 查账号。
func (s *Store) GetUser(ctx context.Context, id string) (User, error) {
	const q = `
		SELECT id, email, password_hash, created_at, updated_at
		FROM users WHERE id = $1`
	var u User
	err := s.pool.QueryRow(ctx, q, id).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, fmt.Errorf("账号 %s：%w", id, ErrNotFound)
	}
	if err != nil {
		return User{}, fmt.Errorf("读取账号失败：%w", err)
	}
	return u, nil
}
