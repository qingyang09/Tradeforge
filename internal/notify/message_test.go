package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/i18n"
	"tradeforge/pkg/types"
)

// TestBuildMessageNeverUsesRecommendationWording is a grep-style mechanical
// check — it turns the claude.md compliance boundary "never generate
// wording that recommends what to buy" from a purely manual review into a
// check that fails the build red, the same idea as
// internal/webui/security_guard_test.go's "policy -> mechanical check".
// The matrix covers direction x reason x mode x language; no combination's
// Title/Body may contain "建议"/"推荐" ("recommend"/"suggest").
func TestBuildMessageNeverUsesRecommendationWording(t *testing.T) {
	forbidden := []string{"建议", "推荐", "recommend", "suggest"}

	// The Reasons here are all factual descriptions (matching the style of
	// Reason copy the engine actually produces, see the Reason wording in
	// each internal/modules module) — this test verifies that BuildMessage's
	// own fixed copy skeleton doesn't additionally introduce "建议"/"推荐",
	// not that "the Reason the engine produces is itself compliant" (that's
	// already handled by the boundary in internal/agent's system prompt, see
	// claude.md).
	directions := []types.Direction{types.DirectionLong, types.DirectionShort, types.DirectionNeutral}
	reasons := []types.Message{
		{Literal: "支撑位放量突破"}, {Literal: "成交量为近期均量的 3 倍"}, {Literal: "CVD 失衡达到阈值"},
	}
	modes := []Mode{ModePreview, ModeLive}
	scores := []float64{0.1, 0.5, 0.95}
	langs := []i18n.Lang{i18n.LangZH, i18n.LangEN}

	sc := types.StrategyConfig{Name: "测试策略", Symbol: "BTCUSDT"}

	for _, dir := range directions {
		for _, reason := range reasons {
			for _, mode := range modes {
				for _, score := range scores {
					for _, lang := range langs {
						d := types.Decision{
							StrategyID: "s1", Symbol: "BTCUSDT", Direction: dir, Score: score,
							Triggered: true, Reason: reason, Price: decimal.NewFromInt(50000),
							Timestamp: time.Now(),
						}
						msg := BuildMessage(sc, d, mode, lang)
						// complianceDisclaimer itself necessarily contains the word
						// "建议"/"advice" (part of the negated phrase "不构成投资建议"/
						// "does not constitute investment advice", and the one place
						// that word is allowed to appear) — strip it out before
						// checking, and only check the part of Body outside it.
						bodyWithoutDisclaimer := strings.Replace(msg.Body, complianceDisclaimer(lang), "", 1)
						for _, bad := range forbidden {
							if strings.Contains(strings.ToLower(msg.Title), strings.ToLower(bad)) {
								t.Errorf("Title should not contain %q, got: %q (dir=%s mode=%s lang=%s)", bad, msg.Title, dir, mode, lang)
							}
							if strings.Contains(strings.ToLower(bodyWithoutDisclaimer), strings.ToLower(bad)) {
								t.Errorf("Body (outside the compliance disclaimer) should not contain %q, got: %q (dir=%s mode=%s lang=%s)",
									bad, bodyWithoutDisclaimer, dir, mode, lang)
							}
						}
						if !strings.Contains(msg.Body, complianceDisclaimer(lang)) {
							t.Errorf("Body should contain the fixed compliance disclaimer, got: %q", msg.Body)
						}
					}
				}
			}
		}
	}
}

func TestBuildMessageLabelsModeCorrectly(t *testing.T) {
	sc := types.StrategyConfig{Name: "测试策略", Symbol: "BTCUSDT"}
	d := types.Decision{
		StrategyID: "s1", Symbol: "BTCUSDT", Direction: types.DirectionLong, Score: 0.8,
		Triggered: true, Reason: types.Message{Literal: "测试原因"}, Price: decimal.NewFromInt(100), Timestamp: time.Now(),
	}

	preview := BuildMessage(sc, d, ModePreview, i18n.LangZH)
	if !strings.Contains(preview.Title, "模拟盘预览") {
		t.Errorf("PAPER_TRADING mode's Title should be labeled 「模拟盘预览」, got: %q", preview.Title)
	}

	live := BuildMessage(sc, d, ModeLive, i18n.LangZH)
	if !strings.Contains(live.Title, "实盘") {
		t.Errorf("LIVE mode's Title should be labeled 「实盘」, got: %q", live.Title)
	}
	if strings.Contains(live.Title, "模拟盘预览") {
		t.Errorf("LIVE mode's Title should not contain 「模拟盘预览」, got: %q", live.Title)
	}

	// Same checks in English, to prove the mode label is genuinely
	// per-language and not just a Chinese constant with an unused lang param.
	previewEN := BuildMessage(sc, d, ModePreview, i18n.LangEN)
	if !strings.Contains(previewEN.Title, "Paper Trading Preview") {
		t.Errorf("English PAPER_TRADING Title should say Paper Trading Preview, got: %q", previewEN.Title)
	}
	liveEN := BuildMessage(sc, d, ModeLive, i18n.LangEN)
	if !strings.Contains(liveEN.Title, "Live") || strings.Contains(liveEN.Title, "Paper Trading Preview") {
		t.Errorf("English LIVE Title should say Live and not Paper Trading Preview, got: %q", liveEN.Title)
	}
}

func TestToWebhookPayloadCarriesDecisionFields(t *testing.T) {
	sc := types.StrategyConfig{Name: "测试策略", Symbol: "BTCUSDT"}
	now := time.Now()
	d := types.Decision{
		StrategyID: "s1", Symbol: "BTCUSDT", Direction: types.DirectionShort, Score: 0.7,
		Triggered: true, Reason: types.Message{Literal: "测试原因"}, Price: decimal.NewFromInt(42000), Timestamp: now,
	}
	msg := BuildMessage(sc, d, ModeLive, i18n.LangZH)
	payload := msg.ToWebhookPayload(d, ModeLive, i18n.LangZH)

	if payload.StrategyID != "s1" || payload.Symbol != "BTCUSDT" || payload.Direction != "SHORT" {
		t.Errorf("payload fields mismatch: %+v", payload)
	}
	if payload.Mode != ModeLive {
		t.Errorf("Mode = %q, want %q", payload.Mode, ModeLive)
	}
	if payload.Reason != "测试原因" {
		t.Errorf("Reason = %q, want the rendered decision reason", payload.Reason)
	}
	if !payload.Timestamp.Equal(now) {
		t.Errorf("Timestamp = %v, want %v", payload.Timestamp, now)
	}
}
