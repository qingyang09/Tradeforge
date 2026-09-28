package agent

import (
	"fmt"
	"strings"
)

// 合规红线：面向用户的文案里不得出现投资建议措辞。
//
// prompt 里已经写了约束，但 prompt 是"请求"不是"保证"——LLM 随时可能跑偏。
// 这一层是硬拦截：凡是命中的输出一律拒绝，宁可让用户重说一遍，
// 也不能让一句"建议你……"流到界面上。
var bannedPhrases = []string{
	// 建议 / 推荐类
	"建议", "推荐", "不妨", "最好是", "应该买", "应该卖", "可以考虑", "值得",
	// 优劣评价类
	"更好", "更优", "不错的策略", "风险较高", "风险较低", "比较激进", "比较保守",
	"这个策略很", "效果会更",
	// 收益 / 预测类
	"预期收益", "大概率", "有望", "看好", "看涨预期", "看跌预期",
	"能赚", "盈利概率", "胜率会",
	// 英文兜底：模型偶尔会中英混输
	"I recommend", "I suggest", "you should buy", "you should sell",
	"better strategy", "more profitable",
}

// ComplianceError 表示输出命中了投资建议措辞。
type ComplianceError struct {
	// Field 是命中的字段名（restatement / questions[i]）。
	Field string
	// Phrase 是命中的措辞。
	Phrase string
	// Text 是被拒绝的原文。
	Text string
}

func (e *ComplianceError) Error() string {
	return fmt.Sprintf("合规检查未通过：%s 中出现了投资建议措辞 %q。"+
		"Agent 只能翻译用户的规则，不得给出建议或评价。原文：%s",
		e.Field, e.Phrase, truncate(e.Text, 120))
}

// checkCompliance 扫描所有面向用户的文案。
//
// 只扫描会展示给用户的字段：restatement 与 questions。
// 模块名、参数名这类机器字段不扫，避免误伤（例如未来出现名为 "trend_advice" 的模块）。
func checkCompliance(p *Proposal) error {
	if err := scanText("复述（restatement）", p.Restatement); err != nil {
		return err
	}
	for i, q := range p.Questions {
		if err := scanText(fmt.Sprintf("澄清问题 questions[%d]", i), q); err != nil {
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
