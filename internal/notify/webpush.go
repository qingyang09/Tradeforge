package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	webpush "github.com/SherClockHolmes/webpush-go"

	"tradeforge/internal/config"
)

// WebPushSender is the minimal interface for the web push channel, for
// tests to inject a fake implementation.
type WebPushSender interface {
	SendPush(ctx context.Context, sub PushSubscription, title, body string) error
}

// PushSubscription mirrors the shape returned by the browser's
// PushManager.subscribe() — it matches exactly the JSON body the client-side
// JS in internal/webui POSTs to /settings/notifications/webpush-subscribe,
// see handlers_settings_notifications.go.
type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// ErrSubscriptionGone means the browser-side push subscription has gone
// stale (the user uninstalled the PWA, cleared browser data, or the push
// service itself decided it expired) — not a retryable error. The caller
// (cmd/notifier's dispatchToChannel) should delete this channel config in
// response, rather than letting it fail on every single trigger.
var ErrSubscriptionGone = errors.New("web push subscription has gone stale")

// VAPIDWebPushSender sends RFC 8291/8292-compliant web push using
// github.com/SherClockHolmes/webpush-go — the correctness risk of hand-rolling
// aes128gcm payload encryption plus VAPID JWT signing far outweighs the one
// dependency saved by not using it: this is the kind of "small, easy to
// silently get wrong without a real push service to test against" crypto
// protocol implementation where the project already has precedent for
// depending on a third-party library instead of hand-writing it stdlib-only
// (compare kafka-go/pgx/coder-websocket) — same tradeoff here. The library's
// only substantive dependency beyond golang.org/x/crypto is golang-jwt/jwt/v5,
// used to sign the VAPID JWT.
type VAPIDWebPushSender struct {
	publicKey, privateKey, subject string
}

func NewVAPIDWebPushSender(cfg config.WebPushConfig) *VAPIDWebPushSender {
	return &VAPIDWebPushSender{
		publicKey: cfg.VAPIDPublicKey, privateKey: cfg.VAPIDPrivateKey, subject: cfg.VAPIDSubject,
	}
}

func (s *VAPIDWebPushSender) SendPush(ctx context.Context, sub PushSubscription, title, body string) error {
	if s.privateKey == "" {
		return fmt.Errorf("VAPID key not configured (TF_VAPID_PRIVATE_KEY), web push channel unavailable")
	}
	payload, err := json.Marshal(map[string]string{"title": title, "body": body})
	if err != nil {
		return fmt.Errorf("failed to marshal push payload: %w", err)
	}

	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		VAPIDPublicKey:  s.publicKey,
		VAPIDPrivateKey: s.privateKey,
		Subscriber:      s.subject,
		TTL:             300,
	})
	if err != nil {
		return fmt.Errorf("failed to send web push: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 || resp.StatusCode == 410 {
		return fmt.Errorf("%w (status code %d)", ErrSubscriptionGone, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("web push service returned a non-2xx status code: %d", resp.StatusCode)
	}
	return nil
}
