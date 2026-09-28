package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	webpush "github.com/SherClockHolmes/webpush-go"

	"tradeforge/internal/config"
)

// WebPushSender 是 web push 渠道的最小接口，供测试注入假实现。
type WebPushSender interface {
	SendPush(ctx context.Context, sub PushSubscription, title, body string) error
}

// PushSubscription 对应浏览器 PushManager.subscribe() 返回结果的形状——跟
// internal/webui 里客户端 JS POST 给 /settings/notifications/webpush-subscribe
// 的 JSON body 完全一致，见 handlers_settings_notifications.go。
type PushSubscription struct {
	Endpoint string `json:"endpoint"`
	P256dh   string `json:"p256dh"`
	Auth     string `json:"auth"`
}

// ErrSubscriptionGone 表示浏览器端的推送订阅已经失效（用户卸载了 PWA、清了浏览器
// 数据、或推送服务自己判定过期）——不是可重试的错误，调用方（cmd/notifier 的
// dispatchToChannel）应当据此删除这条渠道配置，而不是让它在每次触发时都失败一次。
var ErrSubscriptionGone = errors.New("web push 订阅已失效")

// VAPIDWebPushSender 用 github.com/SherClockHolmes/webpush-go 发送 RFC 8291/8292
// 规格的 web push——手写 aes128gcm 载荷加密 + VAPID JWT 签名的正确性风险远高于
// 这里省下的一个依赖：这类"体量小、极易在没有真实推送服务对照的情况下悄悄算错"
// 的加密协议实现，项目已有先例依赖第三方库而不是 stdlib-only 硬写（对照
// kafka-go/pgx/coder-websocket），这里是同一个权衡。库的唯一实质依赖是
// golang.org/x/crypto 之外新增的 golang-jwt/jwt/v5，用来签 VAPID JWT。
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
		return fmt.Errorf("未配置 VAPID 密钥（TF_VAPID_PRIVATE_KEY），web push 渠道不可用")
	}
	payload, err := json.Marshal(map[string]string{"title": title, "body": body})
	if err != nil {
		return fmt.Errorf("序列化推送载荷失败：%w", err)
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
		return fmt.Errorf("发送 web push 失败：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 || resp.StatusCode == 410 {
		return fmt.Errorf("%w（状态码 %d）", ErrSubscriptionGone, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("web push 服务返回非 2xx 状态码：%d", resp.StatusCode)
	}
	return nil
}
