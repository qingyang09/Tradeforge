// Package notify implements the four alert delivery channels
// (email/Telegram/webhook/browser push) and unified message formatting.
// cmd/notifier is this package's only caller.
package notify

import (
	"fmt"
	"time"

	"tradeforge/internal/i18n"
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
// produced it into a webhook payload. lang controls how the embedded Reason
// text (a types.Message under the hood, see pkg/types/i18n.go) is rendered
// -- the receiver gets plain text either way, same as Title/Body, since a
// webhook payload has no way to carry a translation catalog of its own.
func (m Message) ToWebhookPayload(d types.Decision, mode Mode, lang i18n.Lang) WebhookPayload {
	return WebhookPayload{
		Title: m.Title, Body: m.Body, Mode: mode, StrategyID: d.StrategyID, Symbol: d.Symbol,
		Direction: string(d.Direction), Score: d.Score, Price: d.Price.String(),
		Reason: i18n.Render(lang, d.Reason), Timestamp: d.Timestamp,
	}
}

// complianceDisclaimer is the compliance notice fixed to the end of every
// alert body — the sentence itself necessarily contains the word
// "建议"/"advice" (as part of the negated phrase "不构成投资建议"/"does not
// constitute investment advice"), and this is the one place that word is
// allowed to appear in either language. message_test.go's mechanical check
// strips this sentence out of Body first, then checks the remainder for
// "建议"/"推荐" ("recommend"/"suggest") — don't delete or rewrite this
// sentence just because it would otherwise trip that check.
func complianceDisclaimer(lang i18n.Lang) string {
	return i18n.T(lang, "notify.compliance_disclaimer")
}

// BuildMessage formats a triggered decision into alert copy — a factual
// description, with no investment-advice wording of any kind — in the
// recipient's chosen language. sc only uses its Name/Symbol; it doesn't
// expose the full strategy config (risk parameters etc. don't need to leak
// to email/Telegram/webhook receivers).
//
// lang is the recipient's own stored language preference (cmd/notifier is a
// headless background service with no per-request cookie to read, unlike
// the webui), not a live UI toggle -- see the plan's per-user language
// preference section.
func BuildMessage(sc types.StrategyConfig, d types.Decision, mode Mode, lang i18n.Lang) Message {
	modeLabel := i18n.T(lang, "notify.mode.preview")
	if mode == ModeLive {
		modeLabel = i18n.T(lang, "notify.mode.live")
	}
	reason := i18n.Render(lang, d.Reason)
	title := i18n.T(lang, "notify.title",
		"mode_label", modeLabel, "strategy", sc.Name, "symbol", d.Symbol, "direction", directionLabel(lang, d.Direction))
	body := i18n.T(lang, "notify.body",
		"strategy", sc.Name, "symbol", d.Symbol, "direction", directionLabel(lang, d.Direction),
		"score", fmt.Sprintf("%.2f", d.Score), "price", d.Price.String(), "reason", reason,
		"timestamp", d.Timestamp.Format(time.RFC3339), "disclaimer", complianceDisclaimer(lang))
	return Message{Title: title, Subject: title, Body: body}
}

func directionLabel(lang i18n.Lang, dir types.Direction) string {
	switch dir {
	case types.DirectionLong:
		return i18n.T(lang, "notify.direction.long")
	case types.DirectionShort:
		return i18n.T(lang, "notify.direction.short")
	default:
		return i18n.T(lang, "notify.direction.neutral")
	}
}
