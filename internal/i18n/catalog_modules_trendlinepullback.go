package i18n

// Catalog entries for internal/modules/trendlinepullback's module
// description, param descriptions, and Evaluate's Signal.Reason messages.
func init() {
	register(LangEN, map[string]string{
		"modules.trendline_pullback.description": "Detects a pullback to a rising trendline that holds a higher low (long), and its mirror, a rally to a falling trendline that holds a lower high (short). Confirms the trend first from the two most recent confirmed swing lows/highs plus the swing extremum of the opposite kind between and after them (a higher-high-and-higher-low structure, not just two higher lows, which can happen inside an ordinary trading range) -- only then checks whether the current pullback/rally has come within tolerance of the trendline connecting those two swing points, held above/below the prior swing point, and already bounced/rejected away from the test by the confirmation margin.",

		"modules.trendline_pullback.param.swing_lookback":      "How many candles of history (excluding the latest candle) to search for swing highs/lows in.",
		"modules.trendline_pullback.param.pivot_strength":      "How many candles on each side a candle's high/low must exceed to count as a confirmed swing point; a larger value filters out more noise but confirms pivots later.",
		"modules.trendline_pullback.param.trendline_tolerance": "How close the pullback/rally's own extreme must come to the trendline (relative to price) to count as a genuine test of it.",
		"modules.trendline_pullback.param.bounce_confirm":      "How far the current close must have already moved away from the tested extreme (relative to price) to count as a confirmed bounce/rejection, not a test still underway.",

		"modules.trendline_pullback.reason.insufficient_candles":        "Not enough candles: need at least {min}, got {actual}",
		"modules.trendline_pullback.reason.non_positive_close":          "Latest close is not positive; data anomaly",
		"modules.trendline_pullback.reason.not_enough_lows":             "Fewer than two confirmed swing lows found; can't assess an uptrend pullback",
		"modules.trendline_pullback.reason.lows_not_ascending":          "The most recent confirmed swing low isn't higher than the one before it; no higher-low structure",
		"modules.trendline_pullback.reason.no_high_before_lows":         "No confirmed swing high found before the older of the two swing lows; can't assess whether the trend was already rising",
		"modules.trendline_pullback.reason.no_high_between_lows":        "No confirmed swing high found between the two swing lows; can't assess whether this leg made a higher high",
		"modules.trendline_pullback.reason.structure_not_uptrend":       "The swing high between the two swing lows isn't higher than the one before them; the low-low ascent isn't confirmed as an uptrend",
		"modules.trendline_pullback.reason.no_rally_after_higher_low":   "No confirmed swing high found after the higher low; the leg following it hasn't completed yet",
		"modules.trendline_pullback.reason.no_pullback_yet":             "No candles yet after the confirmed swing high that would mark a pullback",
		"modules.trendline_pullback.reason.pullback_broke_structure":    "The current pullback's low has fallen to or below the prior confirmed higher low; the uptrend structure is broken, not holding",
		"modules.trendline_pullback.reason.pullback_not_near_trendline": "The pullback's low isn't close enough to the rising trendline to count as a genuine test of it",
		"modules.trendline_pullback.reason.bounce_not_confirmed":        "The pullback reached the trendline, but the close hasn't yet bounced far enough off the low to confirm it",
		"modules.trendline_pullback.reason.uptrend_pullback_confirmed":  "Price pulled back to {test_low}, held above the prior higher low at {trend_low}, and has bounced -- an uptrend pullback holding a higher low",

		"modules.trendline_pullback.reason.not_enough_highs":          "Fewer than two confirmed swing highs found; can't assess a downtrend rally",
		"modules.trendline_pullback.reason.highs_not_descending":      "The most recent confirmed swing high isn't lower than the one before it; no lower-high structure",
		"modules.trendline_pullback.reason.no_low_before_highs":       "No confirmed swing low found before the older of the two swing highs; can't assess whether the trend was already falling",
		"modules.trendline_pullback.reason.no_low_between_highs":      "No confirmed swing low found between the two swing highs; can't assess whether this leg made a lower low",
		"modules.trendline_pullback.reason.structure_not_downtrend":   "The swing low between the two swing highs isn't lower than the one before them; the high-high descent isn't confirmed as a downtrend",
		"modules.trendline_pullback.reason.no_drop_after_lower_high":  "No confirmed swing low found after the lower high; the leg following it hasn't completed yet",
		"modules.trendline_pullback.reason.no_rally_yet":              "No candles yet after the confirmed swing low that would mark a rally",
		"modules.trendline_pullback.reason.rally_broke_structure":     "The current rally's high has risen to or above the prior confirmed lower high; the downtrend structure is broken, not holding",
		"modules.trendline_pullback.reason.rally_not_near_trendline":  "The rally's high isn't close enough to the falling trendline to count as a genuine test of it",
		"modules.trendline_pullback.reason.rejection_not_confirmed":   "The rally reached the trendline, but the close hasn't yet dropped far enough off the high to confirm rejection",
		"modules.trendline_pullback.reason.downtrend_rally_confirmed": "Price rallied to {test_high}, held below the prior lower high at {trend_high}, and has been rejected -- a downtrend rally holding a lower high",
	})
	register(LangZH, map[string]string{
		"modules.trendline_pullback.description": "检测价格回调到一条上升趋势线附近、并且回调低点高于前一个波段低点（看多），及其镜像——反抽到一条下降趋势线附近、且反抽高点低于前一个波段高点（看空）。先从最近两个确认的波段低点/高点、加上它们之间及之后相反类型的波段极值点确认趋势结构（要求'更高的高点+更高的低点'，而不只是两个低点抬高——后者在普通的区间震荡里也会出现）；结构确认后，才判断当前这次回调/反抽的极值点是否已经靠近连接那两个波段点的趋势线、是否仍然守住了前一个波段点之上/之下，以及收盘价是否已经从测试点反弹/回落超过确认幅度。",

		"modules.trendline_pullback.param.swing_lookback":      "往前搜索波段高低点的K线根数（不含最新这一根）。",
		"modules.trendline_pullback.param.pivot_strength":      "确认一个波段点需要左右各几根K线更低/更高；值越大越能过滤噪音，但确认得越晚。",
		"modules.trendline_pullback.param.trendline_tolerance": "回调/反抽的极值点要离趋势线多近（相对价格的比例）才算真正测试到了这条线。",
		"modules.trendline_pullback.param.bounce_confirm":      "当前收盘价要比被测试的极值点反弹/回落多少（相对价格的比例）才算确认反弹/回落，而不是还在测试途中。",

		"modules.trendline_pullback.reason.insufficient_candles":        "K 线不足：需要至少 {min} 根，实际 {actual} 根",
		"modules.trendline_pullback.reason.non_positive_close":          "最新收盘价非正，数据异常",
		"modules.trendline_pullback.reason.not_enough_lows":             "确认的波段低点不足两个，无法评估上升趋势回调",
		"modules.trendline_pullback.reason.lows_not_ascending":          "最近确认的波段低点没有比前一个更高，不构成低点抬高的结构",
		"modules.trendline_pullback.reason.no_high_before_lows":         "在两个波段低点中较早的那个之前，没有找到确认的波段高点，无法判断当时是否已处于上升趋势",
		"modules.trendline_pullback.reason.no_high_between_lows":        "两个波段低点之间没有找到确认的波段高点，无法判断这一段行情是否创出了更高的高点",
		"modules.trendline_pullback.reason.structure_not_uptrend":       "两个波段低点之间的波段高点，并不比它们之前的那个高点更高，低点抬高尚不能确认为上升趋势",
		"modules.trendline_pullback.reason.no_rally_after_higher_low":   "在这个更高的低点之后，没有找到确认的波段高点，说明随后的这一段上涨行情还没走完",
		"modules.trendline_pullback.reason.no_pullback_yet":             "确认的波段高点之后还没有新的K线，谈不上回调",
		"modules.trendline_pullback.reason.pullback_broke_structure":    "当前这次回调的低点已经跌到或跌破了前一个确认的更高低点，上升趋势结构已被破坏，没有守住",
		"modules.trendline_pullback.reason.pullback_not_near_trendline": "回调低点离上升趋势线还不够近，算不上真正测试到了这条线",
		"modules.trendline_pullback.reason.bounce_not_confirmed":        "回调已经到了趋势线附近，但收盘价还没有从低点反弹足够的幅度来确认",
		"modules.trendline_pullback.reason.uptrend_pullback_confirmed":  "价格回调至 {test_low}，守住了此前更高的低点 {trend_low} 之上，并已反弹——上升趋势中守住更高低点的回调",

		"modules.trendline_pullback.reason.not_enough_highs":          "确认的波段高点不足两个，无法评估下降趋势反抽",
		"modules.trendline_pullback.reason.highs_not_descending":      "最近确认的波段高点没有比前一个更低，不构成高点走低的结构",
		"modules.trendline_pullback.reason.no_low_before_highs":       "在两个波段高点中较早的那个之前，没有找到确认的波段低点，无法判断当时是否已处于下降趋势",
		"modules.trendline_pullback.reason.no_low_between_highs":      "两个波段高点之间没有找到确认的波段低点，无法判断这一段行情是否创出了更低的低点",
		"modules.trendline_pullback.reason.structure_not_downtrend":   "两个波段高点之间的波段低点，并不比它们之前的那个低点更低，高点走低尚不能确认为下降趋势",
		"modules.trendline_pullback.reason.no_drop_after_lower_high":  "在这个更低的高点之后，没有找到确认的波段低点，说明随后的这一段下跌行情还没走完",
		"modules.trendline_pullback.reason.no_rally_yet":              "确认的波段低点之后还没有新的K线，谈不上反抽",
		"modules.trendline_pullback.reason.rally_broke_structure":     "当前这次反抽的高点已经涨到或涨破了前一个确认的更低高点，下降趋势结构已被破坏，没有守住",
		"modules.trendline_pullback.reason.rally_not_near_trendline":  "反抽高点离下降趋势线还不够近，算不上真正测试到了这条线",
		"modules.trendline_pullback.reason.rejection_not_confirmed":   "反抽已经到了趋势线附近，但收盘价还没有从高点回落足够的幅度来确认",
		"modules.trendline_pullback.reason.downtrend_rally_confirmed": "价格反抽至 {test_high}，守住了此前更低的高点 {trend_high} 之下，并已回落——下降趋势中守住更低高点的反抽",
	})
}
