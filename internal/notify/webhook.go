package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
)

// WebhookSender 是 webhook 渠道的最小接口，供测试注入假实现。
type WebhookSender interface {
	SendWebhook(ctx context.Context, url, secret string, payload WebhookPayload) error
}

// WebhookConfig 是打包进 encrypted_config 密文里的、kind="webhook" 时的明文结构。
type WebhookConfig struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"` // 可选：用于 HMAC 签名，接收方可选校验
}

// HTTPWebhookSender 用纯 net/http POST 一段 JSON 载荷——不引入
// standard-webhooks/standard-webhooks/libraries（go.mod 里只是间接依赖，本项目
// 代码从未导入过），HMAC 签名直接用标准库 crypto/hmac/crypto/sha256 手写，
// 一个请求头，没必要为此升级成直接依赖。
type HTTPWebhookSender struct {
	httpClient *http.Client
}

func NewHTTPWebhookSender(hc *http.Client) *HTTPWebhookSender {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &HTTPWebhookSender{httpClient: hc}
}

func (s *HTTPWebhookSender) SendWebhook(ctx context.Context, url, secret string, payload WebhookPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("序列化 webhook 载荷失败：%w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-TradeForge-Signature", hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求 webhook 失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook 返回非 2xx 状态码：%d", resp.StatusCode)
	}
	return nil
}
