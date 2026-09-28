// Package messaging wraps Kafka read/write access, decoupling signal and
// decision delivery between modules.
package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"

	"tradeforge/internal/config"
	"tradeforge/pkg/types"
)

// EnsureTopics idempotently creates the topics the platform uses.
//
// Relying on auto-creation is unreliable: the first write often fails once
// before metadata propagation finishes. Creating topics explicitly at
// service startup surfaces a "topic doesn't exist" problem at startup
// instead of on the first decision.
func EnsureTopics(ctx context.Context, cfg config.KafkaConfig) error {
	conn, err := (&kafka.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", cfg.Brokers[0])
	if err != nil {
		return fmt.Errorf("failed to connect to Kafka (%s): %w", cfg.Brokers[0], err)
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		return fmt.Errorf("failed to get the Kafka controller: %w", err)
	}
	ctrlConn, err := (&kafka.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp",
		net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		return fmt.Errorf("failed to connect to the Kafka controller: %w", err)
	}
	defer ctrlConn.Close()

	// Local single-node setup: 1 partition, 1 replica. Production should
	// plan this separately based on throughput and availability needs.
	specs := []kafka.TopicConfig{
		{Topic: cfg.DecisionTopic, NumPartitions: 1, ReplicationFactor: 1},
		{Topic: cfg.SignalTopic, NumPartitions: 1, ReplicationFactor: 1},
	}
	if err := ctrlConn.CreateTopics(specs...); err != nil {
		return fmt.Errorf("failed to create topics: %w", err)
	}
	return nil
}

// KafkaPublisher publishes decisions and signals to Kafka.
type KafkaPublisher struct {
	decisions *kafka.Writer
	signals   *kafka.Writer
}

// NewKafkaPublisher creates a publisher from config.
//
// The strategy ID is used as the message key: decisions for the same
// strategy land on the same partition, guaranteeing consumers see them in
// the same order they were produced. Out-of-order decisions would let the
// execution layer overwrite a newer signal with an older one, which is
// fatal in a trading system.
func NewKafkaPublisher(cfg config.KafkaConfig) *KafkaPublisher {
	mk := func(topic string) *kafka.Writer {
		return &kafka.Writer{
			Addr:         kafka.TCP(cfg.Brokers...),
			Topic:        topic,
			Balancer:     &kafka.Hash{},
			RequiredAcks: kafka.RequireAll,
			WriteTimeout: 10 * time.Second,
			Async:        false,
			// In local development the topic may not exist yet. In
			// production it should be pre-created by EnsureTopics or an ops
			// process; auto-creation here is just a fallback.
			AllowAutoTopicCreation: true,
		}
	}
	return &KafkaPublisher{
		decisions: mk(cfg.DecisionTopic),
		signals:   mk(cfg.SignalTopic),
	}
}

// PublishDecision implements engine.Publisher.
func (p *KafkaPublisher) PublishDecision(ctx context.Context, d types.Decision) error {
	payload, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("failed to marshal decision: %w", err)
	}
	msg := kafka.Message{
		Key:   []byte(d.StrategyID),
		Value: payload,
		Time:  d.EvaluatedAt,
		Headers: []kafka.Header{
			{Key: "symbol", Value: []byte(d.Symbol)},
			{Key: "triggered", Value: []byte(fmt.Sprint(d.Triggered))},
		},
	}
	if err := p.decisions.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("failed to write to the decision topic: %w", err)
	}
	return nil
}

// PublishSignals publishes each module's raw signal from a single
// evaluation, for debugging and replay.
func (p *KafkaPublisher) PublishSignals(ctx context.Context, strategyID string, signals []types.Signal) error {
	msgs := make([]kafka.Message, 0, len(signals))
	for _, s := range signals {
		payload, err := json.Marshal(s)
		if err != nil {
			return fmt.Errorf("failed to marshal signal for module %s: %w", s.Module, err)
		}
		msgs = append(msgs, kafka.Message{
			Key:   []byte(strategyID),
			Value: payload,
			Time:  s.Timestamp,
			Headers: []kafka.Header{
				{Key: "module", Value: []byte(s.Module)},
				{Key: "symbol", Value: []byte(s.Symbol)},
			},
		})
	}
	if len(msgs) == 0 {
		return nil
	}
	if err := p.signals.WriteMessages(ctx, msgs...); err != nil {
		return fmt.Errorf("failed to write to the signal topic: %w", err)
	}
	return nil
}

// Close closes the underlying writers.
func (p *KafkaPublisher) Close() error {
	var firstErr error
	for _, w := range []*kafka.Writer{p.decisions, p.signals} {
		if err := w.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// DecisionReader consumes the decision topic, for use by the execution
// layer and the backtest engine.
type DecisionReader struct {
	r *kafka.Reader
}

// NewDecisionReader creates a decision consumer. Instances sharing the same
// groupID split partitions among themselves.
func NewDecisionReader(cfg config.KafkaConfig, groupID string) *DecisionReader {
	return &DecisionReader{r: kafka.NewReader(kafka.ReaderConfig{
		Brokers:  cfg.Brokers,
		Topic:    cfg.DecisionTopic,
		GroupID:  groupID,
		MinBytes: 1,
		MaxBytes: 10 << 20,
		MaxWait:  500 * time.Millisecond,
	})}
}

// Read blocks until the next decision is available.
func (r *DecisionReader) Read(ctx context.Context) (types.Decision, error) {
	msg, err := r.r.ReadMessage(ctx)
	if err != nil {
		return types.Decision{}, err
	}
	var d types.Decision
	if err := json.Unmarshal(msg.Value, &d); err != nil {
		return types.Decision{}, fmt.Errorf("failed to unmarshal decision (offset %d): %w", msg.Offset, err)
	}
	return d, nil
}

// Close closes the underlying reader.
func (r *DecisionReader) Close() error { return r.r.Close() }
