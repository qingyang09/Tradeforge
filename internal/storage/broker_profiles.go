package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// BrokerProfile 是一份保存下来的交易所下单通道凭据。EncryptedCredentials/KeySalt/
// KeyNonce 是加密后的密文（见 internal/secretcrypto），这一层只负责存取字节，不关心
// 加密算法、也不关心 EncryptedCredentials 里打包的是几个字段——那是调用方
// （webui 包）的责任。UserID 是这份配置的归属用户。
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

// SaveBrokerProfile 插入一条新的交易所配置（不支持更新已有配置——改配置约定为
// "删掉重加"，避免部分字段更新时不小心让密文和盐/nonce 不再匹配）。activate 为 true
// 时在同一事务内把它设为该用户该 broker 当前生效的一份，该用户名下同 broker 的其它
// 配置全部置为不生效——不同用户之间、同一用户的不同 broker 之间都互不影响。
func (s *Store) SaveBrokerProfile(ctx context.Context, p BrokerProfile, activate bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败：%w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // 提交成功后 Rollback 是空操作

	const insertQ = `
		INSERT INTO broker_profiles
			(id, user_id, label, broker, key_hint, encrypted_credentials, key_salt, key_nonce, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, false, now(), now())`
	if _, err := tx.Exec(ctx, insertQ,
		p.ID, p.UserID, p.Label, p.Broker, p.KeyHint, p.EncryptedCredentials, p.KeySalt, p.KeyNonce,
	); err != nil {
		return fmt.Errorf("保存交易所配置失败：%w", err)
	}

	if activate {
		if err := activateBrokerProfileTx(ctx, tx, p.UserID, p.ID, p.Broker); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ActivateBrokerProfile 把某一份配置设为该用户该 broker 当前生效的一份，该用户名下
// 同 broker 的其余配置全部置为不生效——先查出它属于哪个 broker（同时校验它确实属于
// 当前用户），再按"用户+broker"分组去激活。userID 不匹配（配置存在但不是当前用户的）
// 跟配置根本不存在一样返回 ErrNotFound。
func (s *Store) ActivateBrokerProfile(ctx context.Context, userID, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败：%w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var broker string
	if err := tx.QueryRow(ctx,
		`SELECT broker FROM broker_profiles WHERE id = $1 AND user_id = $2`, id, userID,
	).Scan(&broker); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("交易所配置 %s：%w", id, ErrNotFound)
		}
		return fmt.Errorf("查询交易所配置失败：%w", err)
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
		return fmt.Errorf("清除原有生效配置失败：%w", err)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE broker_profiles SET is_active = true, updated_at = now() WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("设置生效配置失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("交易所配置 %s：%w", id, ErrNotFound)
	}
	return nil
}

// DeleteBrokerProfile 删除一份保存的配置。
func (s *Store) DeleteBrokerProfile(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM broker_profiles WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("删除交易所配置失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("交易所配置 %s：%w", id, ErrNotFound)
	}
	return nil
}

// GetBrokerProfile 按 ID 读取一份配置。
func (s *Store) GetBrokerProfile(ctx context.Context, userID, id string) (BrokerProfile, error) {
	const q = `
		SELECT id, user_id, label, broker, key_hint, encrypted_credentials, key_salt, key_nonce, is_active, created_at, updated_at
		FROM broker_profiles WHERE id = $1 AND user_id = $2`
	var p BrokerProfile
	err := s.pool.QueryRow(ctx, q, id, userID).Scan(&p.ID, &p.UserID, &p.Label, &p.Broker, &p.KeyHint,
		&p.EncryptedCredentials, &p.KeySalt, &p.KeyNonce, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BrokerProfile{}, fmt.Errorf("交易所配置 %s：%w", id, ErrNotFound)
	}
	if err != nil {
		return BrokerProfile{}, fmt.Errorf("读取交易所配置 %s 失败：%w", id, err)
	}
	return p, nil
}

// ListBrokerProfiles 列出该用户全部已保存的交易所配置（跨该用户名下所有 broker），
// 按创建时间正序，供设置页面展示。
func (s *Store) ListBrokerProfiles(ctx context.Context, userID string) ([]BrokerProfile, error) {
	const q = `
		SELECT id, user_id, label, broker, key_hint, encrypted_credentials, key_salt, key_nonce, is_active, created_at, updated_at
		FROM broker_profiles WHERE user_id = $1 ORDER BY created_at`
	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("查询交易所配置失败：%w", err)
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

// ActiveBrokerProfile 读取该用户某个下单通道当前生效的那份配置，没有任何一份生效时
// 返回 ErrNotFound——cmd/executor 启动时据此判断能不能从数据库恢复出凭据。
func (s *Store) ActiveBrokerProfile(ctx context.Context, userID, broker string) (BrokerProfile, error) {
	const q = `
		SELECT id, user_id, label, broker, key_hint, encrypted_credentials, key_salt, key_nonce, is_active, created_at, updated_at
		FROM broker_profiles WHERE user_id = $1 AND broker = $2 AND is_active LIMIT 1`
	var p BrokerProfile
	err := s.pool.QueryRow(ctx, q, userID, broker).Scan(&p.ID, &p.UserID, &p.Label, &p.Broker, &p.KeyHint,
		&p.EncryptedCredentials, &p.KeySalt, &p.KeyNonce, &p.IsActive, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BrokerProfile{}, fmt.Errorf("%s 当前生效的交易所配置：%w", broker, ErrNotFound)
	}
	if err != nil {
		return BrokerProfile{}, fmt.Errorf("读取 %s 当前生效的交易所配置失败：%w", broker, err)
	}
	return p, nil
}
