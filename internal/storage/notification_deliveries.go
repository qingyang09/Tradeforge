package storage

import (
	"context"
	"fmt"
)

// AlreadyDelivered reports whether a given decision has already been successfully
// delivered to a given channel — cmd/notifier calls this before actually sending, to
// avoid duplicate alerts caused by the Kafka consumer group's at-least-once semantics
// (a crashed-and-restarted process reprocessing a decision it already consumed). Only
// status='sent' counts: a previously failed record doesn't count as "delivered" and
// should be allowed to retry.
func (s *Store) AlreadyDelivered(ctx context.Context, decisionID, channelID string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM notification_deliveries WHERE decision_id = $1 AND channel_id = $2 AND status = 'sent')`,
		decisionID, channelID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("querying delivery record: %w", err)
	}
	return exists, nil
}

// RecordDelivery records the outcome of a delivery attempt (success or failure), for
// auditing and for AlreadyDelivered's idempotency check. errMsg only matters when
// status="failed".
func (s *Store) RecordDelivery(ctx context.Context, decisionID, channelID, status, errMsg string) error {
	var errArg any
	if errMsg != "" {
		errArg = errMsg
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO notification_deliveries (decision_id, channel_id, status, error, sent_at) VALUES ($1, $2, $3, $4, now())`,
		decisionID, channelID, status, errArg,
	); err != nil {
		return fmt.Errorf("recording notification delivery: %w", err)
	}
	return nil
}
