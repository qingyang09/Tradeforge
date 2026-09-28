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

// WebhookSender is the minimal interface for the webhook channel, for tests
// to inject a fake implementation.
type WebhookSender interface {
	SendWebhook(ctx context.Context, url, secret string, payload WebhookPayload) error
}

// WebhookConfig is the plaintext shape packed into the encrypted_config
// ciphertext when kind="webhook".
type WebhookConfig struct {
	URL    string `json:"url"`
	Secret string `json:"secret,omitempty"` // optional: used for HMAC signing, receiver may verify it
}

// HTTPWebhookSender POSTs a JSON payload with plain net/http — it doesn't
// pull in standard-webhooks/standard-webhooks/libraries (it's only an
// indirect dependency in go.mod; this project's code never imports it).
// HMAC signing is hand-rolled with the stdlib crypto/hmac/crypto/sha256:
// it's a single request header, not worth promoting to a direct dependency
// for.
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
		return fmt.Errorf("failed to marshal webhook payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to construct request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-TradeForge-Signature", hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned a non-2xx status code: %d", resp.StatusCode)
	}
	return nil
}
