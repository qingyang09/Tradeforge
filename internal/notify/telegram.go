package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// TelegramSender 是 Telegram 渠道的最小接口，供测试注入假实现。
type TelegramSender interface {
	SendMessage(ctx context.Context, chatID, text string) error
}

// TelegramConfig 是打包进 encrypted_config 密文里的、kind="telegram" 时的明文
// 结构——只存 chat_id，bot token 是部署级配置（TF_TELEGRAM_BOT_TOKEN），不是
// 每用户各自的密钥。
type TelegramConfig struct {
	ChatID string `json:"chat_id"`
}

const defaultTelegramBaseURL = "https://api.telegram.org"

// TelegramClient 是 Telegram Bot API 的最小客户端：纯 net/http POST，跟
// internal/marketdata/okx.Client 同一套写法（functional-options 定制、
// context 贯穿、错误包装用"<中文描述>：%w"）。
type TelegramClient struct {
	botToken   string
	baseURL    string
	httpClient *http.Client
}

// TelegramOption 定制 TelegramClient 的构造，主要给测试用来注入假地址/假 http client。
type TelegramOption func(*TelegramClient)

func WithTelegramBaseURL(base string) TelegramOption { return func(c *TelegramClient) { c.baseURL = base } }

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

// telegramResponse 是 Telegram Bot API 统一的响应外壳，只解析用得到的字段。
type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

func (c *TelegramClient) SendMessage(ctx context.Context, chatID, text string) error {
	if c.botToken == "" {
		return fmt.Errorf("未配置 Telegram bot token（TF_TELEGRAM_BOT_TOKEN），Telegram 渠道不可用")
	}
	reqURL := c.baseURL + "/bot" + c.botToken + "/sendMessage"
	payload, err := json.Marshal(map[string]string{"chat_id": chatID, "text": text})
	if err != nil {
		return fmt.Errorf("序列化 Telegram 消息失败：%w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求 Telegram Bot API 失败：%w", err)
	}
	defer resp.Body.Close()

	var body telegramResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("解析 Telegram 响应失败：%w", err)
	}
	if !body.OK {
		return fmt.Errorf("Telegram 返回错误：%s", body.Description)
	}
	return nil
}
