package i18n

// Catalog entries for internal/webui/handlers_builder.go's remaining
// handler-built strings (the describe/translate endpoints' error banners,
// and the context-prefix injected ahead of a builder-page natural-language
// utterance before it's sent to the Agent). Class A, same as every other
// webui handler's error strings.
//
// webui.wizard.err.agent_not_ready and webui.wizard.err.translate_failed
// (catalog_webui_wizard.go) are reused here rather than duplicated --
// handlers_builder.go builds the exact same two messages for its own
// natural-language entry point.
func init() {
	register(LangEN, map[string]string{
		"webui.builder.err.read_body_failed":     "Failed to read the request body: {error}",
		"webui.builder.err.config_too_large":     "The config exceeds the size limit.",
		"webui.builder.err.bad_config_format":    "The config is malformed: {error}",
		"webui.builder.err.validation_failed":    "The config did not pass validation: {error}",
		"webui.builder.err.empty_plan":           "The trading plan description cannot be empty.",
		"webui.builder.source_utterance_label":   "Visual strategy builder",
		"webui.builder.context_prefix":           "(Currently working on the {symbol} visual-builder chart; unless the user explicitly states a different symbol, this rule defaults to applying to {symbol}) {utterance}",
		"webui.wizard.transition.user_confirmed": "The user confirmed the strategy configuration",
		"webui.batch.transition.batch_confirmed": "Batch-scan confirmation ({count} symbols in this batch)",
		"webui.batch.context_prefix":             "(This rule will be applied to {count} different symbols found by the scan; you don't need to decide which specific symbol -- just fill the symbol field with an exchange-format placeholder, which will be replaced by each symbol's own code. Just interpret everything else normally: timeframe, modules, parameters, stop-loss/take-profit, risk controls.) {utterance}",
	})
	register(LangZH, map[string]string{
		"webui.builder.err.read_body_failed":     "读取请求体失败：{error}",
		"webui.builder.err.config_too_large":     "配置体积超出限制。",
		"webui.builder.err.bad_config_format":    "配置格式有误：{error}",
		"webui.builder.err.validation_failed":    "配置未通过校验：{error}",
		"webui.builder.err.empty_plan":           "交易计划描述不能为空。",
		"webui.builder.source_utterance_label":   "可视化建策",
		"webui.builder.context_prefix":           "（当前正在 {symbol} 的可视化建策画板上操作，除非用户明确说了别的标的，否则这条规则默认就是针对 {symbol} 的）{utterance}",
		"webui.wizard.transition.user_confirmed": "用户确认了策略配置",
		"webui.batch.transition.batch_confirmed": "批量扫描确认（本批共 {count} 个标的）",
		"webui.batch.context_prefix":             "（这条规则将被套用到扫描出的 {count} 个不同标的上，不需要你决定具体是哪个标的，标的字段填一个交易所格式的占位符即可（实际会被替换成每个标的各自的代码）——只需要正常理解其它信息：周期、模块、参数、止损止盈、风控）{utterance}",
	})
}
