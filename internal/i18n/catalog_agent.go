package i18n

// Catalog entries for internal/agent's cross-cutting user-facing producers:
// compliance-check rejections (the LLM-facing prompt/schema text lives in
// internal/agent/prompt.go/schema.go and gets its own English variant
// directly, not through this catalog -- see the plan's LLM
// language-awareness section).
func init() {
	register(LangEN, map[string]string{
		"agent.compliance.violation": "Compliance check failed: investment-advice language {phrase} appeared in {field}. The Agent may only translate the user's own rules, never offer advice or evaluation. Original text: {text}",
	})
	register(LangZH, map[string]string{
		"agent.compliance.violation": "合规检查未通过：{field} 中出现了投资建议措辞 {phrase}。Agent 只能翻译用户的规则，不得给出建议或评价。原文：{text}",
	})
}
