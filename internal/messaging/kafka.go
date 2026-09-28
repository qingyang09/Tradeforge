// Package messaging 封装 Kafka 读写，用于模块间的信号/决策传递解耦。
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

// EnsureTopics 幂等地创建平台使用的 topic。
//
// 依赖自动创建是不可靠的：首次写入常常在元数据传播完成前就失败一次。
// 服务启动时显式建好，能让"topic 不存在"这类问题在启动期而不是首笔决策时暴露。
func EnsureTopics(ctx context.Context, cfg config.KafkaConfig) error {
	conn, err := (&kafka.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", cfg.Brokers[0])
	if err != nil {
		return fmt.Errorf("连接 Kafka（%s）失败：%w", cfg.Brokers[0], err)
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		return fmt.Errorf("获取 Kafka controller 失败：%w", err)
	}
	ctrlConn, err := (&kafka.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp",
		net.JoinHostPort(controller.Host, strconv.Itoa(controller.Port)))
	if err != nil {
		return fmt.Errorf("连接 Kafka controller 失败：%w", err)
	}
	defer ctrlConn.Close()

	// 本地单节点环境：1 分区 1 副本。生产环境应按吞吐与可用性另行规划。
	specs := []kafka.TopicConfig{
		{Topic: cfg.DecisionTopic, NumPartitions: 1, ReplicationFactor: 1},
		{Topic: cfg.SignalTopic, NumPartitions: 1, ReplicationFactor: 1},
	}
	if err := ctrlConn.CreateTopics(specs...); err != nil {
		return fmt.Errorf("创建 topic 失败：%w", err)
	}
	return nil
}

// KafkaPublisher 把决策与信号发布到 Kafka。
type KafkaPublisher struct {
	decisions *kafka.Writer
	signals   *kafka.Writer
}

// NewKafkaPublisher 按配置创建发布器。
//
// 用策略 ID 作为消息 key：同一策略的决策会落到同一分区，
// 从而保证消费端看到的顺序与产生顺序一致。乱序的决策会让执行层
// 拿旧信号覆盖新信号，这在交易系统里是致命的。
func NewKafkaPublisher(cfg config.KafkaConfig) *KafkaPublisher {
	mk := func(topic string) *kafka.Writer {
		return &kafka.Writer{
			Addr:         kafka.TCP(cfg.Brokers...),
			Topic:        topic,
			Balancer:     &kafka.Hash{},
			RequiredAcks: kafka.RequireAll,
			WriteTimeout: 10 * time.Second,
			Async:        false,
			// 本地开发时 topic 可能还不存在。生产环境应当由 EnsureTopics
			// 或运维流程预先建好，自动创建只是兜底。
			AllowAutoTopicCreation: true,
		}
	}
	return &KafkaPublisher{
		decisions: mk(cfg.DecisionTopic),
		signals:   mk(cfg.SignalTopic),
	}
}

// PublishDecision 实现 engine.Publisher。
func (p *KafkaPublisher) PublishDecision(ctx context.Context, d types.Decision) error {
	payload, err := json.Marshal(d)
	if err != nil {
		return fmt.Errorf("序列化决策失败：%w", err)
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
		return fmt.Errorf("写入决策 topic 失败：%w", err)
	}
	return nil
}

// PublishSignals 把单次评估中各模块的原始信号发布出去，用于调试与回放。
func (p *KafkaPublisher) PublishSignals(ctx context.Context, strategyID string, signals []types.Signal) error {
	msgs := make([]kafka.Message, 0, len(signals))
	for _, s := range signals {
		payload, err := json.Marshal(s)
		if err != nil {
			return fmt.Errorf("序列化模块 %s 的信号失败：%w", s.Module, err)
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
		return fmt.Errorf("写入信号 topic 失败：%w", err)
	}
	return nil
}

// Close 关闭底层 writer。
func (p *KafkaPublisher) Close() error {
	var firstErr error
	for _, w := range []*kafka.Writer{p.decisions, p.signals} {
		if err := w.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// DecisionReader 消费决策 topic，供执行层与回测引擎使用。
type DecisionReader struct {
	r *kafka.Reader
}

// NewDecisionReader 创建决策消费者。groupID 相同的实例之间会分摊分区。
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

// Read 阻塞读取下一条决策。
func (r *DecisionReader) Read(ctx context.Context) (types.Decision, error) {
	msg, err := r.r.ReadMessage(ctx)
	if err != nil {
		return types.Decision{}, err
	}
	var d types.Decision
	if err := json.Unmarshal(msg.Value, &d); err != nil {
		return types.Decision{}, fmt.Errorf("反序列化决策失败（offset %d）：%w", msg.Offset, err)
	}
	return d, nil
}

// Close 关闭底层 reader。
func (r *DecisionReader) Close() error { return r.r.Close() }
