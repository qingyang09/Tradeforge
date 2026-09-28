package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// TelegramSender is the minimal interface for the Telegram channel, for
// tests to inject a fake implementation.
type TelegramSender interface {
	SendMessage(ctx context.Context, chatID, text string) error
}

// TelegramConfig is the plaintext shape packed into the encrypted_config
// ciphertext when kind="telegram" — it only stores chat_id; the bot token
// is deployment-level config (TF_TELEGRAM_BOT_TOKEN), not a per-user secret.
type TelegramConfig struct {
	ChatID string `json:"chat_id"`
}

const defaultTelegramBaseURL = "https://api.telegram.org"

// TelegramClient is a minimal Telegram Bot API client: plain net/http POST,
// following the same conventions as internal/marketdata/okx.Client
// (functional-options for customization, context threaded through, errors
// wrapped as "<description>: %w").
type TelegramClient struct {
	botToken   string
	baseURL    string
	httpClient *http.Client
}

// TelegramOption customizes TelegramClient construction, mainly used by
// tests to inject a fake base URL / fake http client.
type TelegramOption func(*TelegramClient)

func WithTelegramBaseURL(base string) TelegramOption {
	return func(c *TelegramClient) { c.baseURL = base }
}

func WithTelegramHTTPClient(hc *http.Client) TelegramOption {
	return func(c *TelegramClient) { c.httpClient = hc }
}

func NewTelegramClient(botToken string, opts ...TelegramOption) *TelegramClient {
	c := &TelegramClient{botToken: botToken, baseURL: defaultTelegramBaseURL, httpClient: http.DefaultClient}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// telegramResponse is the Telegram Bot API's common response envelope; only
// the fields we actually use are parsed.
type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

func (c *TelegramClient) SendMessage(ctx context.Context, chatID, text string) error {
	if c.botToken == "" {
		return fmt.Errorf("Telegram bot token not configured (TF_TELEGRAM_BOT_TOKEN), Telegram channel unavailable")
	}
	reqURL := c.baseURL + "/bot" + c.botToken + "/sendMessage"
	payload, err := json.Marshal(map[string]string{"chat_id": chatID, "text": text})
	if err != nil {
		return fmt.Errorf("failed to marshal Telegram message: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("failed to construct request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to call Telegram Bot API: %w", err)
	}
	defer resp.Body.Close()

	var body telegramResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("failed to parse Telegram response: %w", err)
	}
	if !body.OK {
		return fmt.Errorf("Telegram returned an error: %s", body.Description)
	}
	return nil
}
