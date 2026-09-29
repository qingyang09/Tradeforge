package i18n

// Catalog entries for internal/modules/macdrsi: the module's Description,
// its RequiredParams descriptions, and the reasons attached to the Signals
// its Evaluate produces.
func init() {
	register(LangEN, map[string]string{
		"modules.macd_rsi.description": "Combines MACD golden/death crosses with RSI overbought/oversold reversals to detect turns in trend momentum.",

		"modules.macd_rsi.param.fast_period":    "MACD fast EMA period.",
		"modules.macd_rsi.param.slow_period":    "MACD slow EMA period. Must be greater than fast_period.",
		"modules.macd_rsi.param.signal_period":  "EMA period for the MACD signal line (DEA).",
		"modules.macd_rsi.param.rsi_period":     "RSI calculation period (Wilder-smoothed).",
		"modules.macd_rsi.param.rsi_overbought": "RSI overbought threshold.",
		"modules.macd_rsi.param.rsi_oversold":   "RSI oversold threshold. Must be less than rsi_overbought.",
		"modules.macd_rsi.param.mode": "macd_cross only looks at MACD golden/death crosses; rsi_reversal only looks at RSI extreme-zone reversals; " +
			"confluence requires that when a golden/death cross happens, RSI is not yet in the same-direction extreme zone.",

		"modules.macd_rsi.reason.insufficient_candles": "Not enough candles: need at least {required}, got {actual}",
		"modules.macd_rsi.reason.no_trigger":           "No signal triggered on this candle under {mode} mode (MACD {macd}/{macd_signal}, RSI {rsi})",
		"modules.macd_rsi.reason.bullish_cross":        "MACD golden cross: MACD line {macd}, signal line {signal}",
		"modules.macd_rsi.reason.bearish_cross":        "MACD death cross: MACD line {macd}, signal line {signal}",
		"modules.macd_rsi.reason.bullish_reversal":     "RSI oversold bounce: previous value {prev_rsi} crossed back into range",
		"modules.macd_rsi.reason.bearish_reversal":     "RSI overbought pullback: previous value {prev_rsi} crossed back into range",
	})
	register(LangZH, map[string]string{
		"modules.macd_rsi.description": "结合 MACD 金叉/死叉与 RSI 超买超卖反转，检测趋势动量的转向。",

		"modules.macd_rsi.param.fast_period":    "MACD 快线 EMA 周期。",
		"modules.macd_rsi.param.slow_period":    "MACD 慢线 EMA 周期，必须大于 fast_period。",
		"modules.macd_rsi.param.signal_period":  "MACD 信号线（DEA）的 EMA 周期。",
		"modules.macd_rsi.param.rsi_period":     "RSI 计算周期（Wilder 平滑）。",
		"modules.macd_rsi.param.rsi_overbought": "RSI 超买阈值。",
		"modules.macd_rsi.param.rsi_oversold":   "RSI 超卖阈值，必须小于 rsi_overbought。",
		"modules.macd_rsi.param.mode": "macd_cross 只看 MACD 金叉死叉；rsi_reversal 只看 RSI 极值反转；" +
			"confluence 要求金叉死叉发生时 RSI 未处于同向极值区间。",

		"modules.macd_rsi.reason.insufficient_candles": "K 线不足：需要至少 {required} 根，实际 {actual} 根",
		"modules.macd_rsi.reason.no_trigger":           "模式 {mode} 下本根未触发信号（MACD {macd}/{macd_signal}，RSI {rsi}）",
		"modules.macd_rsi.reason.bullish_cross":        "MACD 金叉：MACD 线 {macd}，信号线 {signal}",
		"modules.macd_rsi.reason.bearish_cross":        "MACD 死叉：MACD 线 {macd}，信号线 {signal}",
		"modules.macd_rsi.reason.bullish_reversal":     "RSI 超卖反弹：前值 {prev_rsi} 穿回阈值区间",
		"modules.macd_rsi.reason.bearish_reversal":     "RSI 超买回落：前值 {prev_rsi} 穿回阈值区间",
	})
}
