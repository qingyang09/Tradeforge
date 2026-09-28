package agent

import (
	"fmt"
	"strings"

	"tradeforge/internal/i18n"
	"tradeforge/pkg/types"
)

// Compliance red line: user-facing copy must never contain investment-advice
// language.
//
// The prompt already asks for this constraint, but a prompt is a request,
// not a guarantee -- the LLM can drift off it at any time. This layer is the
// hard block: any output that trips it is rejected outright, and the user
// gets asked to try again rather than ever letting a sentence like "I
// suggest..." reach the UI.
var bannedPhrases = []string{
	// Suggestion / recommendation
	"建议", "推荐", "不妨", "最好是", "应该买", "应该卖", "可以考虑", "值得",
	// Value judgments
	"更好", "更优", "不错的策略", "风险较高", "风险较低", "比较激进", "比较保守",
	"这个策略很", "效果会更",
	// Return / prediction
	"预期收益", "大概率", "有望", "看好", "看涨预期", "看跌预期",
	"能赚", "盈利概率", "胜率会",
	// English fallback: the model occasionally mixes English into its output
	"I recommend", "I suggest", "you should buy", "you should sell",
	"better strategy", "more profitable",
}

// ComplianceError means an output tripped the investment-advice-language check.
type ComplianceError struct {
	// Field is the machine-readable name of the field that tripped the
	// check ("restatement" or "questions[N]").
	Field string
	// Phrase is the banned phrase that was matched.
	Phrase string
	// Text is the rejected original text.
	Text string
}

func (e *ComplianceError) Error() string {
	return i18n.Render(i18n.LangEN, e.Reason())
}

// Reason returns this error as a translatable types.Message, for callers
// that want to render it in the viewer's actual language rather than the
// fixed-English Error() string.
func (e *ComplianceError) Reason() types.Message {
	return types.Msg("agent.compliance.violation",
		"field", e.Field, "phrase", e.Phrase, "text", truncate(e.Text, 120))
}

// checkCompliance scans every piece of user-facing copy.
//
// Only fields actually shown to the user are scanned: restatement and
// questions. Machine fields like module/param names aren't scanned, to
// avoid false positives (e.g. a hypothetical future module literally named
// "trend_advice").
func checkCompliance(p *Proposal) error {
	if err := scanText("restatement", p.Restatement); err != nil {
		return err
	}
	for i, q := range p.Questions {
		if err := scanText(fmt.Sprintf("questions[%d]", i), q); err != nil {
			return err
		}
	}
	return nil
}

func scanText(field, text string) error {
	lower := strings.ToLower(text)
	for _, phrase := range bannedPhrases {
		if strings.Contains(lower, strings.ToLower(phrase)) {
			return &ComplianceError{Field: field, Phrase: phrase, Text: text}
		}
	}
	return nil
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
