package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"tradeforge/internal/notify"
	"tradeforge/internal/secretcrypto"
	"tradeforge/internal/storage"
	"tradeforge/pkg/types"
)

// channelSenders gathers internal/notify's four channel interfaces into one
// dispatch point keyed by kind, and is responsible for decrypting each
// channel's own encrypted_config with the master key — decryption happens
// right before sending, with no plaintext cached ahead of time elsewhere,
// keeping plaintext credentials' lifetime as short as possible.
type channelSenders struct {
	masterKey string
	dryRun    bool
	logger    *slog.Logger

	email    notify.EmailSender
	telegram notify.TelegramSender
	webhook  notify.WebhookSender
	webpush  notify.WebPushSender
}

func (cs channelSenders) send(ctx context.Context, ch storage.NotificationChannel, msg notify.Message, d types.Decision, mode notify.Mode) error {
	plaintext, err := secretcrypto.Decrypt(cs.masterKey, ch.EncryptedConfig, ch.KeySalt, ch.KeyNonce)
	if err != nil {
		return fmt.Errorf("failed to decrypt channel config (does TF_MASTER_KEY match what it was saved with?): %w", err)
	}

	if cs.dryRun {
		cs.logger.Info("(dry-run) would have sent an alert", "channel_kind", ch.Kind, "channel_id", ch.ID, "title", msg.Title)
		return nil
	}

	switch ch.Kind {
	case "email":
		var c notify.EmailConfig
		if err := json.Unmarshal([]byte(plaintext), &c); err != nil {
			return fmt.Errorf("failed to parse email channel config: %w", err)
		}
		return cs.email.SendEmail(ctx, c.Address, msg.Subject, msg.Body)
	case "telegram":
		var c notify.TelegramConfig
		if err := json.Unmarshal([]byte(plaintext), &c); err != nil {
			return fmt.Errorf("failed to parse Telegram channel config: %w", err)
		}
		return cs.telegram.SendMessage(ctx, c.ChatID, msg.PlainText())
	case "webhook":
		var c notify.WebhookConfig
		if err := json.Unmarshal([]byte(plaintext), &c); err != nil {
			return fmt.Errorf("failed to parse webhook channel config: %w", err)
		}
		return cs.webhook.SendWebhook(ctx, c.URL, c.Secret, msg.ToWebhookPayload(d, mode))
	case "webpush":
		var sub notify.PushSubscription
		if err := json.Unmarshal([]byte(plaintext), &sub); err != nil {
			return fmt.Errorf("failed to parse web push subscription: %w", err)
		}
		return cs.webpush.SendPush(ctx, sub, msg.Title, msg.PlainText())
	default:
		return fmt.Errorf("unknown alert channel kind %q", ch.Kind)
	}
}
