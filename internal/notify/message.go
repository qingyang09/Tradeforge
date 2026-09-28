// Package notify implements the four alert delivery channels
// (email/Telegram/webhook/browser push) and unified message formatting.
// cmd/notifier is this package's only caller.
package notify

import (
	"fmt"
	"time"

	"tradeforge/pkg/types"
)

// Mode marks whether an alert's underlying strategy is paper-trading
// preview or real live trading — every channel's copy has to make this
// clear, so a user can never mistake a paper-trading alert for a real fill,
// or vice versa.
type Mode string

const (
	ModePreview Mode = "PAPER_TRADING" // paper trading: preview/validation experience
	ModeLive    Mode = "LIVE"          // live trading: real alert
)

// Message is formatted alert content ready to feed directly to any channel.
//
// Compliance boundary (see the project's claude.md): every word of
// Title/Body may only factually describe "what the signal computed"
// (direction, strength, price, trigger reason, which modules were
// involved) — it must never contain wording like "recommend"/"suggest"/
// "would be better" that implies investment advice. This boundary is
// already mandatory in the Agent's system prompt; this is its extension
// into notification copy, not a reinvention of it. message_test.go turns
// this constraint into a mechanical, grep-style check rather than relying
// on manual review alone.
type Message struct {
	Title   string
	Subject string // for email; usually equal to Title
	Body    string // multi-line body; used by email/Telegram/webhook
}

// PlainText is the single-block text used by Telegram/web push.
func (m Message) PlainText() string { return m.Title + "\n\n" + m.Body }

// WebhookPayload is the webhook channel's structured JSON payload — besides
// the human-readable Title/Body, it also carries every key field of the
// decision as-is, so the receiver's own system can consume it directly
// instead of having to parse the natural-language text back out of Body.
type WebhookPayload struct {
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Mode       Mode      `json:"mode"`
	StrategyID string    `json:"strategy_id"`
	Symbol     string    `json:"symbol"`
	Direction  string    `json:"direction"`
	Score      float64   `json:"score"`
	Price      string    `json:"price"`
	Reason     string    `json:"reason"`
	Timestamp  time.Time `json:"timestamp"`
}

// ToWebhookPayload packs a Message together with the Decision and Mode that
// produced it into a webhook payload.
func (m Message) ToWebhookPayload(d types.Decision, mode Mode) WebhookPayload {
	return WebhookPayload{
		Title: m.Title, Body: m.Body, Mode: mode, StrategyID: d.StrategyID, Symbol: d.Symbol,
		Direction: string(d.Direction), Score: d.Score, Price: d.Price.String(),
		Reason: d.Reason, Timestamp: d.Timestamp,
	}
}

// complianceDisclaimer is the compliance notice fixed to the end of every
// alert body — the sentence itself necessarily contains the word
// "建议"/"advice" (as part of the negated phrase "不构成投资建议"/"does not
// constitute investment advice"), and this is the one place that word is
// allowed to appear. message_test.go's mechanical check strips this
// sentence out of Body first, then checks the remainder for "建议"/"推荐"
// ("recommend"/"suggest") — don't delete or rewrite this sentence just
// because it would otherwise trip that check.
const complianceDisclaimer = "此提醒只描述系统按你设定的规则计算出的结果，不构成投资建议。"

// BuildMessage formats a triggered decision into alert copy — a factual
// description, with no investment-advice wording of any kind. sc only uses
// its Name/Symbol; it doesn't expose the full strategy config (risk
// parameters etc. don't need to leak to email/Telegram/webhook receivers).
func BuildMessage(sc types.StrategyConfig, d types.Decision, mode Mode) Message {
	modeLabel := "模拟盘预览"
	if mode == ModeLive {
		modeLabel = "实盘"
	}
	title := fmt.Sprintf("[%s] %s 触发：%s %s", modeLabel, sc.Name, d.Symbol, directionLabel(d.Direction))
	body := fmt.Sprintf(
		"策略：%s\n标的：%s\n方向：%s\n强度：%.2f\n价格：%s\n原因：%s\n触发时间：%s\n\n%s",
		sc.Name, d.Symbol, directionLabel(d.Direction), d.Score, d.Price.String(), d.Reason,
		d.Timestamp.Format(time.RFC3339), complianceDisclaimer)
	return Message{Title: title, Subject: title, Body: body}
}

func directionLabel(dir types.Direction) string {
	switch dir {
	case types.DirectionLong:
		return "做多"
	case types.DirectionShort:
		return "做空"
	default:
		return "中性"
	}
}
