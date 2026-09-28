package i18n

// Catalog entries for internal/webui/describe_fallback.go's deterministic
// no-LLM restatement. Each entry is one sentence fragment; describePlain
// concatenates them in sequence, same shape as the original hand-written
// Chinese version, just resolved per-fragment through the catalog instead
// of fmt.Sprintf'd directly.
func init() {
	register(LangEN, map[string]string{
		"webui.describe.symbol_timeframe": "Symbol {symbol}, {timeframe} timeframe. ",
		"webui.describe.combine_weighted": "Combination: WEIGHTED (weighted confidence, triggers once it exceeds a threshold of {threshold}). ",
		"webui.describe.combine_all":      "Combination: ALL (triggers only when every module agrees on direction). ",
		"webui.describe.modules_included": "Modules included: {names}. ",
		"webui.describe.list_sep":         ", ",
		"webui.describe.module_params":    "{module}'s parameters: {params}. ",
		"webui.describe.default_params":   "all defaults",
		"webui.describe.param_sep":        ", ",

		"webui.describe.risk.sizing_risk_pct":                "Risk control: sized by risk percentage -- account equity {equity}, {pct}% risk per trade, hard position cap {cap} (a computed size larger than this is rejected outright, never silently shrunk). ",
		"webui.describe.risk.sizing_fixed":                   "Risk control: max position size per trade {cap}. ",
		"webui.describe.risk.max_daily_loss":                 "Max daily loss {value}. ",
		"webui.describe.risk.stop_loss_support_resistance":   "Stop loss: the nearest support level as of entry. ",
		"webui.describe.risk.stop_loss_poc":                  "Stop loss: back through the volume point of control (POC) as of entry. ",
		"webui.describe.risk.stop_loss_pct":                  "Stop loss {pct}%. ",
		"webui.describe.risk.take_profit_support_resistance": "Take profit: the nearest resistance level as of entry. ",
		"webui.describe.risk.take_profit_poc":                "Take profit: touching the volume point of control (POC) as of entry. ",
		"webui.describe.risk.take_profit_pct":                "Take profit {pct}%. ",
		"webui.describe.risk.max_holding":                    "Max holding period {value}. ",
	})
	register(LangZH, map[string]string{
		"webui.describe.symbol_timeframe": "标的 {symbol}，{timeframe} 周期。",
		"webui.describe.combine_weighted": "组合方式为 WEIGHTED（按权重加权置信度，超过阈值 {threshold} 才触发）。",
		"webui.describe.combine_all":      "组合方式为 ALL（所有模块同方向才触发）。",
		"webui.describe.modules_included": "包含以下模块：{names}。",
		"webui.describe.list_sep":         "、",
		"webui.describe.module_params":    " {module} 的参数：{params}。",
		"webui.describe.default_params":   "全部使用系统默认值",
		"webui.describe.param_sep":        "，",

		"webui.describe.risk.sizing_risk_pct":                " 风控：按风险百分比开仓——账户权益 {equity}，单笔风险 {pct}%，仓位硬上限 {cap}（算出来的仓位超过它会被直接拒绝，不会自动缩小）。",
		"webui.describe.risk.sizing_fixed":                   " 风控：单笔最大仓位 {cap}。",
		"webui.describe.risk.max_daily_loss":                 "单日最大亏损 {value}。",
		"webui.describe.risk.stop_loss_support_resistance":   "止损：跌破开仓时最近的支撑位。",
		"webui.describe.risk.stop_loss_poc":                  "止损：收回开仓时的成交量分布重心（POC）。",
		"webui.describe.risk.stop_loss_pct":                  "止损 {pct}%。",
		"webui.describe.risk.take_profit_support_resistance": "止盈：触及开仓时最近的阻力位。",
		"webui.describe.risk.take_profit_poc":                "止盈：触及开仓时的成交量分布重心（POC）。",
		"webui.describe.risk.take_profit_pct":                "止盈 {pct}%。",
		"webui.describe.risk.max_holding":                    "最长持仓 {value}。",
	})
}
