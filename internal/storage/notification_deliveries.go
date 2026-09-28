package storage

import (
	"context"
	"fmt"
)

// AlreadyDelivered 报告某条决策是否已经成功投递给某个渠道——cmd/notifier 在真正
// 发送前调用，避免 Kafka 消费组的 at-least-once 语义（进程崩溃重启后重新处理同一条
// 已消费过的决策）导致用户收到重复提醒。只看 status='sent'：之前失败过的记录不算
// "已投递"，应当允许重试。
func (s *Store) AlreadyDelivered(ctx context.Context, decisionID, channelID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM notification_deliveries WHERE decision_id = $1 AND channel_id = $2 AND status = 'sent')`,
		decisionID, channelID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("查询投递记录失败：%w", err)
	}
	return exists, nil
}

// RecordDelivery 记录一次投递尝试的结果（成功或失败），供审计与 AlreadyDelivered
// 的幂等检查使用。errMsg 仅在 status="failed" 时有意义。
func (s *Store) RecordDelivery(ctx context.Context, decisionID, channelID, status, errMsg string) error {
	var errArg any
	if errMsg != "" {
		errArg = errMsg
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO notification_deliveries (decision_id, channel_id, status, error, sent_at) VALUES ($1, $2, $3, $4, now())`,
		decisionID, channelID, status, errArg,
	); err != nil {
		return fmt.Errorf("记录提醒投递失败：%w", err)
	}
	return nil
}
