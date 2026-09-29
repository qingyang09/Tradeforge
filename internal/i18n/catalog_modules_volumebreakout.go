package i18n

// Catalog entries for internal/modules/volumebreakout's Description,
// ParamSpec descriptions, and Signal.Reason messages.
func init() {
	register(LangEN, map[string]string{
		"modules.volume_breakout.description": "Computes the latest candle's volume as a multiple of its recent average, emitting a signal when the multiple exceeds a threshold; direction comes from the candle's close vs. open or from net taker buy/sell volume.",

		"modules.volume_breakout.param.window":           "Averaging window: number of candles used to compute the baseline volume (excludes the current candle).",
		"modules.volume_breakout.param.multiplier":       "Volume multiple threshold: the signal only triggers once the latest volume reaches this multiple of the average.",
		"modules.volume_breakout.param.direction_source": `Direction source: "candle" uses the candle's bullish/bearish close, "taker" uses net taker buy/sell volume.`,
		"modules.volume_breakout.param.min_body_ratio":   "Minimum body ratio: when the candle body's share of its full range falls below this value, direction is treated as unclear and the signal is neutral. 0 disables the filter.",

		"modules.volume_breakout.reason.insufficient_candles": "Not enough candles: need at least {required}, got {actual}",
		"modules.volume_breakout.reason.zero_average_volume":  "Average volume over the baseline window is zero; the volume multiple can't be computed",
		"modules.volume_breakout.reason.below_threshold":      "Volume is {ratio}x the average, below the {multiplier}x threshold",
		"modules.volume_breakout.reason.zero_range":           "Candle range is zero; direction can't be determined",
		"modules.volume_breakout.reason.ambiguous_body":       "Volume surged {ratio}x, but the candle body is only {body_ratio} of its range (below {min_body_ratio}); direction is unclear",

		"modules.volume_breakout.reason.direction_unclear_flat_close":     "Volume surged {ratio}x, but the close equals the open; direction is unclear",
		"modules.volume_breakout.reason.direction_unclear_zero_volume":    "Volume surged {ratio}x, but volume is zero; direction is unclear",
		"modules.volume_breakout.reason.direction_unclear_no_taker_data":  "Volume surged {ratio}x, but the data source doesn't supply taker buy volume; direction is unclear",
		"modules.volume_breakout.reason.direction_unclear_taker_balanced": "Volume surged {ratio}x, but taker buy and sell volume are equal; direction is unclear",

		"modules.volume_breakout.reason.triggered_bullish_candle":      "Volume is {ratio}x the {window}-candle average (threshold {multiplier}x), and the candle closed bullish",
		"modules.volume_breakout.reason.triggered_bearish_candle":      "Volume is {ratio}x the {window}-candle average (threshold {multiplier}x), and the candle closed bearish",
		"modules.volume_breakout.reason.triggered_taker_buy_dominant":  "Volume is {ratio}x the {window}-candle average (threshold {multiplier}x); taker buy volume {buy} exceeds taker sell volume {sell}",
		"modules.volume_breakout.reason.triggered_taker_sell_dominant": "Volume is {ratio}x the {window}-candle average (threshold {multiplier}x); taker sell volume {sell} exceeds taker buy volume {buy}",
	})
	register(LangZH, map[string]string{
		"modules.volume_breakout.description": "计算最新一根 K 线成交量相对近期均量的倍数，倍数超过阈值时输出信号，方向由 K 线涨跌或主动买卖量净差决定。",

		"modules.volume_breakout.param.window":           "均量窗口，用于计算基准成交量的 K 线根数（不含最新一根）。",
		"modules.volume_breakout.param.multiplier":       "放量倍数阈值：最新成交量达到均量的该倍数才触发信号。",
		"modules.volume_breakout.param.direction_source": "方向判定来源：candle 按 K 线收阳/收阴，taker 按主动买卖量净差。",
		"modules.volume_breakout.param.min_body_ratio":   "最小实体占比：K 线实体长度与全幅之比低于该值时视为方向不明，输出中性。0 表示不过滤。",

		"modules.volume_breakout.reason.insufficient_candles": "K 线不足：需要至少 {required} 根，实际 {actual} 根",
		"modules.volume_breakout.reason.zero_average_volume":  "基准窗口内均量为零，无法计算放量倍数",
		"modules.volume_breakout.reason.below_threshold":      "成交量为均量的 {ratio} 倍，未达到 {multiplier} 倍阈值",
		"modules.volume_breakout.reason.zero_range":           "K 线全幅为零，无法判定方向",
		"modules.volume_breakout.reason.ambiguous_body":       "放量 {ratio} 倍，但实体占比 {body_ratio} 低于 {min_body_ratio}，方向不明",

		"modules.volume_breakout.reason.direction_unclear_flat_close":     "放量 {ratio} 倍，但收盘价等于开盘价，方向不明",
		"modules.volume_breakout.reason.direction_unclear_zero_volume":    "放量 {ratio} 倍，但成交量为零，方向不明",
		"modules.volume_breakout.reason.direction_unclear_no_taker_data":  "放量 {ratio} 倍，但数据源未提供主动买入量，方向不明",
		"modules.volume_breakout.reason.direction_unclear_taker_balanced": "放量 {ratio} 倍，但主动买卖量相等，方向不明",

		"modules.volume_breakout.reason.triggered_bullish_candle":      "成交量为近 {window} 根均量的 {ratio} 倍（阈值 {multiplier} 倍），该 K 线收阳",
		"modules.volume_breakout.reason.triggered_bearish_candle":      "成交量为近 {window} 根均量的 {ratio} 倍（阈值 {multiplier} 倍），该 K 线收阴",
		"modules.volume_breakout.reason.triggered_taker_buy_dominant":  "成交量为近 {window} 根均量的 {ratio} 倍（阈值 {multiplier} 倍），主动买入量 {buy} 大于主动卖出量 {sell}",
		"modules.volume_breakout.reason.triggered_taker_sell_dominant": "成交量为近 {window} 根均量的 {ratio} 倍（阈值 {multiplier} 倍），主动卖出量 {sell} 大于主动买入量 {buy}",
	})
}
