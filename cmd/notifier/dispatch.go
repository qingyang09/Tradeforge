package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"tradeforge/internal/messaging"
	"tradeforge/internal/notify"
	"tradeforge/internal/storage"
	"tradeforge/pkg/types"
)

// notifierStore 是 cmd/notifier 运行时依赖的最小持久化接口，跟 executorStore/
// promotionStore 是同一个模式：用接口而不是 *storage.Store，让 handleDecision 不需要
// 真实 Postgres 就能单元测试。*storage.Store 结构性满足这个接口，不需要任何额外代码。
type notifierStore interface {
	GetStrategyAllUsers(ctx context.Context, id string) (types.StrategyConfig, error)
	ListNotificationChannels(ctx context.Context, userID string) ([]storage.NotificationChannel, error)
	AlreadyDelivered(ctx context.Context, decisionID, channelID string) (bool, error)
	RecordDelivery(ctx context.Context, decisionID, channelID, status, errMsg string) error
	DeleteNotificationChannel(ctx context.Context, userID, id string) error
}

// consume 是决策消费循环，结构直接照抄 cmd/executor/main.go 的 consume()：无限
// for 循环、reader.Read(ctx)、出错先判断 ctx.Err()（区分优雅关闭与瞬时错误）、
// 固定 1 秒退避（不是指数退避），成功后交给 handleDecision——一条决策处理失败
// 只记日志，不会让循环停下来，也不会中断对下一条决策的处理。
func consume(
	ctx context.Context, reader *messaging.DecisionReader, store notifierStore,
	senders channelSenders, ownerUserID string, logger *slog.Logger,
) {
	for {
		d, err := reader.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Error("读取决策失败，稍后重试", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		handleDecision(ctx, store, senders, ownerUserID, d, logger)
	}
}

// handleDecision 处理单条决策：判断要不要提醒、找出该用户开启的渠道、逐个渠道
// 发送。三层隔离在这里都要体现：
//  1. 一个渠道发送失败不影响同一条决策要发给的其它渠道（dispatchToChannel 内部
//     catch 住错误，只记审计，不 return error）；
//  2. 一条决策的处理失败（查策略失败、查渠道失败）不影响 consume 循环处理下一条
//     决策（handleDecision 本身不返回 error，调用方 consume 永远继续循环）；
//  3. 单用户模式下过滤掉其它用户的决策，不影响本该服务的这个用户的决策。
// 这条隔离原则贯穿执行层（Supervisor.Dispatch、checkPromotions），这里同样适用。
func handleDecision(
	ctx context.Context, store notifierStore, senders channelSenders,
	ownerUserID string, d types.Decision, logger *slog.Logger,
) {
	if !d.Triggered {
		return // 只在真正触发时提醒；未触发的决策已经由引擎落库审计，不需要打扰用户。
	}

	sc, err := store.GetStrategyAllUsers(ctx, d.StrategyID)
	if err != nil {
		// 策略可能在决策产生后、消费前被删除——不是错误，是常见情况，不升级为 Warn。
		if !errors.Is(err, storage.ErrNotFound) {
			logger.Warn("查询策略失败，跳过本条决策的提醒", "strategy_id", d.StrategyID, "err", err)
		}
		return
	}
	if ownerUserID != "" && sc.UserID != ownerUserID {
		return // 单用户模式：不是这个用户的决策，静默跳过（同 cmd/executor 对 ErrUnknownStrategy 的处理）。
	}

	mode, alert := alertMode(sc.State)
	if !alert {
		return // DRAFT/BACKTESTED/LIVE_ELIGIBLE/SUSPENDED 都不提醒。
	}

	channels, err := store.ListNotificationChannels(ctx, sc.UserID)
	if err != nil {
		logger.Warn("查询提醒渠道失败，跳过本条决策的提醒",
			"strategy_id", d.StrategyID, "user_id", sc.UserID, "err", err)
		return
	}

	msg := notify.BuildMessage(sc, d, mode)
	for _, ch := range channels {
		if !ch.IsEnabled {
			continue
		}
		dispatchToChannel(ctx, store, senders, ch, d, msg, mode, logger)
	}
}

// alertMode 判断某个状态下要不要提醒，以及提醒文案里该怎么标注这条提醒的性质
// （预览/验证 vs 真实）。PAPER_TRADING 与 LIVE 都提醒——这是产品决策：模拟盘阶段
// 也要提醒，帮用户在真金白银之前建立对策略的信任感；LIVE_ELIGIBLE 虽然"接近实盘"
// 但还没被用户手动解锁，不提醒；DRAFT/BACKTESTED/SUSPENDED 更不提醒。
func alertMode(state types.StrategyState) (mode notify.Mode, alert bool) {
	switch state {
	case types.StatePaperTrading:
		return notify.ModePreview, true
	case types.StateLive:
		return notify.ModeLive, true
	default:
		return "", false
	}
}

// dispatchToChannel 把一条已经格式化好的提醒发给单个渠道，处理幂等检查、发送、
// 审计落库。任何一步失败都只记日志/落库失败记录，不向上传播——不能让一个渠道的
// 故障（比如 SMTP 超时）影响同一条决策要发给的其它渠道，也不能让它中断 consume
// 循环。web push 订阅失效（ErrSubscriptionGone）额外触发自我清理：删除这条已经
// 不可能再送达的渠道配置，避免它在每次触发时都留下一条 failed 审计记录。
func dispatchToChannel(
	ctx context.Context, store notifierStore, senders channelSenders,
	ch storage.NotificationChannel, d types.Decision, msg notify.Message, mode notify.Mode, logger *slog.Logger,
) {
	already, err := store.AlreadyDelivered(ctx, d.ID, ch.ID)
	if err != nil {
		logger.Warn("查询投递记录失败，为避免重复提醒本次跳过",
			"decision_id", d.ID, "channel_id", ch.ID, "err", err)
		return
	}
	if already {
		return // 幂等：Kafka 消费组可能重放已处理过的决策，见 migrations/010 的注释。
	}

	sendErr := senders.send(ctx, ch, msg, d, mode)

	status := "sent"
	errMsg := ""
	if sendErr != nil {
		status = "failed"
		errMsg = sendErr.Error()
		logger.Warn("提醒发送失败", "channel_kind", ch.Kind, "channel_id", ch.ID, "decision_id", d.ID, "err", sendErr)

		if errors.Is(sendErr, notify.ErrSubscriptionGone) {
			if delErr := store.DeleteNotificationChannel(ctx, ch.UserID, ch.ID); delErr != nil {
				logger.Warn("清理失效的 web push 订阅失败", "channel_id", ch.ID, "err", delErr)
			} else {
				logger.Info("已清理失效的 web push 订阅", "channel_id", ch.ID)
			}
		}
	}
	if err := store.RecordDelivery(ctx, d.ID, ch.ID, status, errMsg); err != nil {
		logger.Error("记录投递审计失败", "decision_id", d.ID, "channel_id", ch.ID, "err", err)
	}
}
