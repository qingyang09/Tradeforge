package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// NotificationChannel 是一条保存下来的提醒渠道配置。EncryptedConfig/KeySalt/
// KeyNonce 是加密后的密文（见 internal/secretcrypto），这一层只负责存取字节，不关心
// 加密算法、也不关心 EncryptedConfig 里打包的是几个字段——那是调用方
// （webui 包 / cmd/notifier）的责任。UserID 是这条渠道的归属用户。
//
// 跟 BrokerProfile 的关键差异：BrokerProfile 同一个 broker 最多一行 is_active
// （一次只能用一份凭据打给同一家交易所）；NotificationChannel 没有这种互斥性——
// 同一个 kind 可以有任意多行同时 is_enabled=true（比如两个 webhook、多台设备各自的
// web push 订阅），语义上更接近"订阅列表"而不是"当前生效的一份配置"。
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

// SaveNotificationChannel 插入一条新的提醒渠道配置（不支持更新已有配置的
// EncryptedConfig——改配置约定为"删掉重加"，避免部分字段更新时不小心让密文和
// 盐/nonce 不再匹配，跟 SaveBrokerProfile 是同一个理由）。新插入的渠道默认
// is_enabled=true（保存即启用，用户如果想先保留后启用可以保存后立刻停用）。
func (s *Store) SaveNotificationChannel(ctx context.Context, c NotificationChannel) error {
	const q = `
		INSERT INTO notification_channels
			(id, user_id, kind, label, key_hint, encrypted_config, key_salt, key_nonce, is_enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now(), now())`
	if _, err := s.pool.Exec(ctx, q,
		c.ID, c.UserID, c.Kind, c.Label, c.KeyHint, c.EncryptedConfig, c.KeySalt, c.KeyNonce, c.IsEnabled,
	); err != nil {
		return fmt.Errorf("保存提醒渠道失败：%w", err)
	}
	return nil
}

// ListNotificationChannels 列出该用户全部已保存的提醒渠道（含已停用的），按创建
// 时间正序，供设置页面展示、以及 cmd/notifier 查询该用户当前配置了哪些渠道
// （调用方自己按 IsEnabled 过滤要不要真的发送）。
func (s *Store) ListNotificationChannels(ctx context.Context, userID string) ([]NotificationChannel, error) {
	const q = `
		SELECT id, user_id, kind, label, key_hint, encrypted_config, key_salt, key_nonce, is_enabled, created_at, updated_at
		FROM notification_channels WHERE user_id = $1 ORDER BY created_at`
	rows, err := s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("查询提醒渠道失败：%w", err)
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

// GetNotificationChannel 按 ID 读取一条配置。userID 不匹配（渠道存在但不是当前
// 用户的）跟渠道根本不存在一样返回 ErrNotFound。
func (s *Store) GetNotificationChannel(ctx context.Context, userID, id string) (NotificationChannel, error) {
	const q = `
		SELECT id, user_id, kind, label, key_hint, encrypted_config, key_salt, key_nonce, is_enabled, created_at, updated_at
		FROM notification_channels WHERE id = $1 AND user_id = $2`
	var c NotificationChannel
	err := s.pool.QueryRow(ctx, q, id, userID).Scan(&c.ID, &c.UserID, &c.Kind, &c.Label, &c.KeyHint,
		&c.EncryptedConfig, &c.KeySalt, &c.KeyNonce, &c.IsEnabled, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return NotificationChannel{}, fmt.Errorf("提醒渠道 %s：%w", id, ErrNotFound)
	}
	if err != nil {
		return NotificationChannel{}, fmt.Errorf("读取提醒渠道 %s 失败：%w", id, err)
	}
	return c, nil
}

// SetNotificationChannelEnabled 切换一条渠道的启停状态。
//
// 跟 BrokerProfile 家族的方法刻意不同：BrokerProfile 完全没有更新路径（只有
// 删掉重加），这里专门开了这一个更新方法——因为切换 is_enabled 完全不碰
// EncryptedConfig/KeySalt/KeyNonce，不存在"部分字段更新导致密文和盐/nonce 不再
// 匹配"的风险，那正是 BrokerProfile 拒绝提供更新路径的唯一理由，这里不适用。
func (s *Store) SetNotificationChannelEnabled(ctx context.Context, userID, id string, enabled bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE notification_channels SET is_enabled = $1, updated_at = now() WHERE id = $2 AND user_id = $3`,
		enabled, id, userID)
	if err != nil {
		return fmt.Errorf("切换提醒渠道状态失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("提醒渠道 %s：%w", id, ErrNotFound)
	}
	return nil
}

// DeleteNotificationChannel 删除一条保存的渠道配置。
func (s *Store) DeleteNotificationChannel(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notification_channels WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("删除提醒渠道失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("提醒渠道 %s：%w", id, ErrNotFound)
	}
	return nil
}
