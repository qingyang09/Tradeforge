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

// channelSenders 把 internal/notify 的四个渠道接口按 kind 收拢成一个分发点，
// 并负责用主密钥解密每个渠道自己的 encrypted_config——解密就近发生在发送前，
// 不在别处提前解出明文缓存，缩小明文凭据的生命周期。
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
		return fmt.Errorf("解密渠道配置失败（TF_MASTER_KEY 是否跟保存时一致）：%w", err)
	}

	if cs.dryRun {
		cs.logger.Info("（dry-run）本应发送提醒", "channel_kind", ch.Kind, "channel_id", ch.ID, "title", msg.Title)
		return nil
	}

	switch ch.Kind {
	case "email":
		var c notify.EmailConfig
		if err := json.Unmarshal([]byte(plaintext), &c); err != nil {
			return fmt.Errorf("解析邮件渠道配置失败：%w", err)
		}
		return cs.email.SendEmail(ctx, c.Address, msg.Subject, msg.Body)
	case "telegram":
		var c notify.TelegramConfig
		if err := json.Unmarshal([]byte(plaintext), &c); err != nil {
			return fmt.Errorf("解析 Telegram 渠道配置失败：%w", err)
		}
		return cs.telegram.SendMessage(ctx, c.ChatID, msg.PlainText())
	case "webhook":
		var c notify.WebhookConfig
		if err := json.Unmarshal([]byte(plaintext), &c); err != nil {
			return fmt.Errorf("解析 webhook 渠道配置失败：%w", err)
		}
		return cs.webhook.SendWebhook(ctx, c.URL, c.Secret, msg.ToWebhookPayload(d, mode))
	case "webpush":
		var sub notify.PushSubscription
		if err := json.Unmarshal([]byte(plaintext), &sub); err != nil {
			return fmt.Errorf("解析 web push 订阅失败：%w", err)
		}
		return cs.webpush.SendPush(ctx, sub, msg.Title, msg.PlainText())
	default:
		return fmt.Errorf("未知的提醒渠道类型 %q", ch.Kind)
	}
}
