package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AgentProfile 是一份保存下来的 LLM 供应商配置。EncryptedAPIKey/KeySalt/KeyNonce
// 是加密后的密文（见 internal/secretcrypto），这一层只负责存取字节，不关心
// 加密算法——密钥管理是调用方（webui 包）的责任，storage 包不该知道服务端主密钥
// 这种概念。UserID 是这份配置的归属用户——每个用户各自最多一份生效配置
// （见 007_agent_profiles_ownership.sql 的分组唯一索引），不同用户互不影响。
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

// SaveAgentProfile 插入一条新的模型配置（不支持更新已有配置——改配置约定为"删掉重加"，
// 避免部分字段更新时不小心让密文和盐/nonce 不再匹配）。activate 为 true 时在同一事务内
// 把它设为该用户当前生效的一份，该用户名下的其它配置全部置为不生效——不同用户之间
// 互不影响。
func (s *Store) SaveAgentProfile(ctx context.Context, p AgentProfile, activate bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败：%w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // 提交成功后 Rollback 是空操作

	const insertQ = `
		INSERT INTO agent_profiles
			(id, user_id, label, provider, model, base_url, key_hint, encrypted_api_key, key_salt, key_nonce, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, false, now(), now())`
	if _, err := tx.Exec(ctx, insertQ,
		p.ID, p.UserID, p.Label, p.Provider, p.Model, p.BaseURL, p.KeyHint, p.EncryptedAPIKey, p.KeySalt, p.KeyNonce,
	); err != nil {
		return fmt.Errorf("保存模型配置失败：%w", err)
	}

	if activate {
		if err := activateProfileTx(ctx, tx, p.UserID, p.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ActivateAgentProfile 把某一份配置设为该用户当前生效的一份，该用户名下其余全部置为
// 不生效——部分唯一索引（见 007_agent_profiles_ownership.sql）保证同一用户同一时刻
// 只有一行 is_active = true，这里用事务显式地"先全部关掉、再打开这一行"，不依赖索引
// 冲突去兜底。userID 不匹配（这份配置存在但不是当前用户的）跟配置根本不存在一样返回
// ErrNotFound——不让调用方探测出"这个 ID 存在，只是不是你的"。
func (s *Store) ActivateAgentProfile(ctx context.Context, userID, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败：%w", err)
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
		return fmt.Errorf("清除原有生效配置失败：%w", err)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE agent_profiles SET is_active = true, updated_at = now() WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("设置生效配置失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("模型配置 %s：%w", id, ErrNotFound)
	}
	return nil
}

// DeleteAgentProfile 删除一份保存的配置。删除当前生效的那份不会自动激活另一份——
// 界面回落到"未配置"状态，避免用户没注意到就悄悄切换到另一把可能不是他想用的 key。
func (s *Store) DeleteAgentProfile(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM agent_profiles WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("删除模型配置失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("模型配置 %s：%w", id, ErrNotFound)
	}
	return nil
}

// GetAgentProfile 按 ID 读取一份配置——激活/删除前用它确认这份配置真的存在，
// 激活时还要用它把加密的 API key 取出来解密。
func (s *Store) GetAgentProfile(ctx context.Context, userID, id string) (AgentProfile, error) {
	const q = `
		SELECT id, user_id, label, provider, model, base_url, key_hint, encrypted_api_key, key_salt, key_nonce, is_active, created_at, updated_at
		FROM agent_profiles WHERE id = $1 AND user_id = $2`
	var p AgentProfile
	err := s.pool.QueryRow(ctx, q, id, userID).Scan(&p.ID, &p.UserID, &p.Label, &p.Provider, &p.Model, &p.BaseURL, &p.KeyHint,
		&p.EncryptedAPIKey, &p.KeySalt, &p.KeyNonce, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentProfile{}, fmt.Errorf("模型配置 %s：%w", id, ErrNotFound)
	}
	if err != nil {
		return AgentProfile{}, fmt.Errorf("读取模型配置 %s 失败：%w", id, err)
	}
	return p, nil
}

// ListAgentProfiles 列出该用户全部已保存的模型配置，按创建时间正序，供设置页面展示。
func (s *Store) ListAgentProfiles(ctx context.Context, userID string) ([]AgentProfile, error) {
	const q = `
		SELECT id, user_id, label, provider, model, base_url, key_hint, encrypted_api_key, key_salt, key_nonce, is_active, created_at, updated_at
		FROM agent_profiles WHERE user_id = $1 ORDER BY created_at`
	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("查询模型配置失败：%w", err)
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

// ActiveAgentProfile 读取该用户当前生效的那份配置，没有任何一份生效时返回 ErrNotFound。
func (s *Store) ActiveAgentProfile(ctx context.Context, userID string) (AgentProfile, error) {
	const q = `
		SELECT id, user_id, label, provider, model, base_url, key_hint, encrypted_api_key, key_salt, key_nonce, is_active, created_at, updated_at
		FROM agent_profiles WHERE user_id = $1 AND is_active LIMIT 1`
	var p AgentProfile
	err := s.pool.QueryRow(ctx, q, userID).Scan(&p.ID, &p.UserID, &p.Label, &p.Provider, &p.Model, &p.BaseURL, &p.KeyHint,
		&p.EncryptedAPIKey, &p.KeySalt, &p.KeyNonce, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentProfile{}, fmt.Errorf("当前生效的模型配置：%w", ErrNotFound)
	}
	if err != nil {
		return AgentProfile{}, fmt.Errorf("读取当前生效的模型配置失败：%w", err)
	}
	return p, nil
}
