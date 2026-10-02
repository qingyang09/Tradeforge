package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"tradeforge/internal/i18n"
	"tradeforge/internal/messaging"
	"tradeforge/internal/notify"
	"tradeforge/internal/storage"
	"tradeforge/pkg/types"
)

// notifierStore is the minimal persistence interface cmd/notifier depends on
// at runtime, same pattern as executorStore/promotionStore: using an
// interface rather than *storage.Store lets handleDecision be unit tested
// without a real Postgres. *storage.Store satisfies this interface
// structurally, with no extra code needed.
type notifierStore interface {
	GetStrategyAllUsers(ctx context.Context, id string) (types.StrategyConfig, error)
	ListNotificationChannels(ctx context.Context, userID string) ([]storage.NotificationChannel, error)
	AlreadyDelivered(ctx context.Context, decisionID, channelID string) (bool, error)
	RecordDelivery(ctx context.Context, decisionID, channelID, status, errMsg string) error
	DeleteNotificationChannel(ctx context.Context, userID, id string) error
	// GetUser is used to read the strategy owner's preferred_lang (see
	// handleDecision) -- a background process has no per-request cookie to
	// resolve a language from, so the alert's language has to come from each
	// recipient's own stored account preference instead.
	GetUser(ctx context.Context, id string) (storage.User, error)
}

// consume is the decision consumption loop, structured directly after
// cmd/executor/main.go's consume(): an infinite for loop, reader.Read(ctx),
// checking ctx.Err() first on error (to distinguish graceful shutdown from
// a transient error), a fixed 1-second backoff (not exponential), and
// handing off to handleDecision on success — one decision's processing
// failure only gets logged, it never stops the loop or interrupts
// processing of the next decision.
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
			logger.Error("failed to read decision, retrying later", "err", err)
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

// handleDecision processes a single decision: decides whether to alert,
// finds the enabled channels for that user, and sends to each. Three layers
// of isolation apply here:
//  1. One channel's send failure doesn't affect the other channels this same
//     decision should go to (dispatchToChannel catches the error internally,
//     only recording it for audit, never returning an error);
//  2. One decision's processing failure (strategy lookup failed, channel
//     lookup failed) doesn't affect the consume loop processing the next
//     decision (handleDecision itself never returns an error, so the caller,
//     consume, always keeps looping);
//  3. In single-tenant mode, filtering out other users' decisions doesn't
//     affect the decisions for the user actually being served.
//
// This isolation principle runs throughout the execution layer
// (Supervisor.Dispatch, checkPromotions), and applies here too.
func handleDecision(
	ctx context.Context, store notifierStore, senders channelSenders,
	ownerUserID string, d types.Decision, logger *slog.Logger,
) {
	if !d.Triggered {
		return // Only alert on an actual trigger; a non-triggered decision is already audited by the engine and doesn't need to bother the user.
	}

	sc, err := store.GetStrategyAllUsers(ctx, d.StrategyID)
	if err != nil {
		// The strategy may have been deleted between decision creation and consumption — not an error, a common case, so it's not escalated to Warn.
		if !errors.Is(err, storage.ErrNotFound) {
			logger.Warn("failed to look up strategy, skipping the alert for this decision", "strategy_id", d.StrategyID, "err", err)
		}
		return
	}
	if ownerUserID != "" && sc.UserID != ownerUserID {
		return // Single-tenant mode: not this user's decision, silently skip (same handling as cmd/executor's treatment of ErrUnknownStrategy).
	}

	mode, alert := alertMode(sc.State)
	if !alert {
		return // DRAFT/BACKTESTED/LIVE_ELIGIBLE/SUSPENDED never alert.
	}

	channels, err := store.ListNotificationChannels(ctx, sc.UserID)
	if err != nil {
		logger.Warn("failed to look up alert channels, skipping the alert for this decision",
			"strategy_id", d.StrategyID, "user_id", sc.UserID, "err", err)
		return
	}

	// Each recipient gets alerted in their own stored language preference
	// (migrations/011_users_preferred_lang.sql), not a hardcoded default --
	// a failed lookup (deleted account, transient DB error) just falls back
	// to i18n.DefaultLang rather than dropping the alert entirely.
	lang := i18n.DefaultLang
	if u, err := store.GetUser(ctx, sc.UserID); err == nil {
		lang = i18n.ParseLang(u.PreferredLang)
	} else {
		logger.Warn("failed to look up the recipient's language preference, defaulting",
			"strategy_id", d.StrategyID, "user_id", sc.UserID, "err", err)
	}
	msg := notify.BuildMessage(sc, d, mode, lang)
	for _, ch := range channels {
		if !ch.IsEnabled {
			continue
		}
		dispatchToChannel(ctx, store, senders, ch, d, msg, mode, lang, logger)
	}
}

// alertMode decides whether to alert for a given state, and how the alert
// copy should label the nature of the alert (preview/validation vs real).
// Both PAPER_TRADING and LIVE alert — this is a product decision: the
// paper-trading stage should alert too, to help the user build trust in a
// strategy before risking real money; LIVE_ELIGIBLE, despite being "close to
// live", hasn't been manually unlocked by the user yet, so it doesn't alert;
// DRAFT/BACKTESTED/SUSPENDED alert even less.
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

// dispatchToChannel sends one already-formatted alert to a single channel,
// handling the idempotency check, sending, and audit persistence. Any step
// failing is only logged/persisted as a failed record, never propagated
// upward — one channel's failure (e.g. an SMTP timeout) must not affect the
// other channels this same decision should go to, nor interrupt the consume
// loop. A dead web push subscription (ErrSubscriptionGone) additionally
// triggers self-cleanup: deleting the channel config that can never be
// delivered to again, avoiding a failed audit record on every future trigger.
func dispatchToChannel(
	ctx context.Context, store notifierStore, senders channelSenders,
	ch storage.NotificationChannel, d types.Decision, msg notify.Message, mode notify.Mode, lang i18n.Lang, logger *slog.Logger,
) {
	already, err := store.AlreadyDelivered(ctx, d.ID, ch.ID)
	if err != nil {
		logger.Warn("failed to check delivery record, skipping this time to avoid a duplicate alert",
			"decision_id", d.ID, "channel_id", ch.ID, "err", err)
		return
	}
	if already {
		return // Idempotent: the Kafka consumer group may replay an already-processed decision, see the comment on migrations/010.
	}

	sendErr := senders.send(ctx, ch, msg, d, mode, lang)

	status := "sent"
	errMsg := ""
	if sendErr != nil {
		status = "failed"
		errMsg = sendErr.Error()
		logger.Warn("failed to send alert", "channel_kind", ch.Kind, "channel_id", ch.ID, "decision_id", d.ID, "err", sendErr)

		if errors.Is(sendErr, notify.ErrSubscriptionGone) {
			if delErr := store.DeleteNotificationChannel(ctx, ch.UserID, ch.ID); delErr != nil {
				logger.Warn("failed to clean up dead web push subscription", "channel_id", ch.ID, "err", delErr)
			} else {
				logger.Info("cleaned up dead web push subscription", "channel_id", ch.ID)
			}
		}
	}
	if err := store.RecordDelivery(ctx, d.ID, ch.ID, status, errMsg); err != nil {
		logger.Error("failed to record delivery audit", "decision_id", d.ID, "channel_id", ch.ID, "err", err)
	}
}
