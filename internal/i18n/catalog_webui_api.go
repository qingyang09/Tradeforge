package i18n

// Catalog entries for internal/webui/handlers_api.go's JSON API error
// responses (query-param validation for the builder page's candle/preview
// endpoints). These aren't rendered through a template -- they're plain
// text/JSON error bodies read by the builder page's own JS -- so the
// handler renders them through i18n.Render(resolveLang(r), ...) directly
// before writing the response, instead of a template {{t/msg}} call.
func init() {
	register(LangEN, map[string]string{
		"webui.api.before_must_be_unix_seconds": "before must be a Unix-seconds timestamp",
		"webui.api.unsupported_timeframe":       "Unsupported timeframe; valid values are {timeframes}",
		"webui.api.limit_must_be_positive_int":  "limit must be a positive integer",
		"webui.api.invalid_params":              "Invalid parameters: {error}",
		"webui.api.param_must_be_int":           "Parameter {name} must be an integer: {value}",
		"webui.api.param_must_be_number":        "Parameter {name} must be a number: {value}",
	})
	register(LangZH, map[string]string{
		"webui.api.before_must_be_unix_seconds": "before 必须是 Unix 秒时间戳",
		"webui.api.unsupported_timeframe":       "不支持的周期，可选值为 {timeframes}",
		"webui.api.limit_must_be_positive_int":  "limit 必须是正整数",
		"webui.api.invalid_params":              "参数非法：{error}",
		"webui.api.param_must_be_int":           "参数 {name} 必须是整数：{value}",
		"webui.api.param_must_be_number":        "参数 {name} 必须是数字：{value}",
	})
}
