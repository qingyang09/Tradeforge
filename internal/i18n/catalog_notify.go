package i18n

// Catalog entries for internal/notify's alert message formatting.
func init() {
	register(LangEN, map[string]string{
		"notify.mode.preview":      "Paper Trading Preview",
		"notify.mode.live":         "Live",
		"notify.direction.long":    "Long",
		"notify.direction.short":   "Short",
		"notify.direction.neutral": "Neutral",
		"notify.title":             "[{mode_label}] {strategy} triggered: {symbol} {direction}",
		"notify.body":              "Strategy: {strategy}\nSymbol: {symbol}\nDirection: {direction}\nStrength: {score}\nPrice: {price}\nReason: {reason}\nTriggered at: {timestamp}\n\n{disclaimer}",
		// See message.go's complianceDisclaimer doc comment: this sentence
		// legitimately contains "advice" as part of the negation "does not
		// constitute investment advice" -- message_test.go's mechanical check
		// strips exactly this string before checking the rest of the body.
		"notify.compliance_disclaimer": "This alert only describes what the system computed from your own configured rules; it is not investment advice.",
	})
	register(LangZH, map[string]string{
		"notify.mode.preview":          "模拟盘预览",
		"notify.mode.live":             "实盘",
		"notify.direction.long":        "做多",
		"notify.direction.short":       "做空",
		"notify.direction.neutral":     "中性",
		"notify.title":                 "[{mode_label}] {strategy} 触发：{symbol} {direction}",
		"notify.body":                  "策略：{strategy}\n标的：{symbol}\n方向：{direction}\n强度：{score}\n价格：{price}\n原因：{reason}\n触发时间：{timestamp}\n\n{disclaimer}",
		"notify.compliance_disclaimer": "此提醒只描述系统按你设定的规则计算出的结果，不构成投资建议。",
	})
}
