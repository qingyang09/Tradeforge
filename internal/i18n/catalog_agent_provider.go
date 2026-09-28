package i18n

// Catalog entries for internal/agent's LLM provider display names
// (Provider.Label). Most are brand names that don't really "translate," but
// several of the non-US vendors are commonly known in Chinese by a different
// name than their English one, so this is still per-language rather than a
// hardcoded string.
func init() {
	register(LangEN, map[string]string{
		"agent.provider.anthropic": "Anthropic (Claude)",
		"agent.provider.openai":    "OpenAI (GPT)",
		"agent.provider.gemini":    "Google (Gemini)",
		"agent.provider.grok":      "xAI (Grok)",
		"agent.provider.deepseek":  "DeepSeek",
		"agent.provider.mistral":   "Mistral AI",
		"agent.provider.qwen":      "Alibaba Cloud (Qwen)",
		"agent.provider.glm":       "Zhipu (GLM)",
		"agent.provider.kimi":      "Moonshot AI (Kimi)",
		"agent.provider.custom":    "Custom (OpenAI-compatible API)",
	})
	register(LangZH, map[string]string{
		"agent.provider.anthropic": "Anthropic（Claude）",
		"agent.provider.openai":    "OpenAI（GPT）",
		"agent.provider.gemini":    "Google（Gemini）",
		"agent.provider.grok":      "xAI（Grok）",
		"agent.provider.deepseek":  "DeepSeek（深度求索）",
		"agent.provider.mistral":   "Mistral AI",
		"agent.provider.qwen":      "阿里云（通义千问）",
		"agent.provider.glm":       "智谱（GLM）",
		"agent.provider.kimi":      "月之暗面（Kimi）",
		"agent.provider.custom":    "自定义（OpenAI 兼容接口）",
	})
}
