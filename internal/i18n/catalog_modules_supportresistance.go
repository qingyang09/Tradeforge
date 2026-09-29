package i18n

// Catalog entries for internal/modules/supportresistance.
func init() {
	register(LangEN, map[string]string{
		"modules.support_resistance.description": "Clusters recent swing highs/lows within the lookback window into support and resistance levels, and emits a signal when price breaks out above, breaks down below, or retests a key level.",

		"modules.support_resistance.param.lookback":         "Lookback window: the number of candles used to compute key levels.",
		"modules.support_resistance.param.pivot_strength":   "Swing point strength: a candle's high only counts as a swing high if it's higher than the N candles on each side of it (same for a swing low). A larger value finds fewer but more significant points.",
		"modules.support_resistance.param.tolerance":        "Clustering tolerance, as a relative fraction of price. 0.005 means swing points within 0.5% of each other are grouped into the same key level.",
		"modules.support_resistance.param.min_touches":      "Minimum number of times a key level must be touched; levels touched fewer times than this are discarded.",
		"modules.support_resistance.param.breakout_confirm": "Breakout confirmation margin: the close must clear the key level by this fraction to count as a breakout, filtering out brief wicks through the level.",
		"modules.support_resistance.param.proximity":        "Retest distance: the close is considered to be touching/testing a key level when its relative distance from it is within this fraction.",

		"modules.support_resistance.reason.insufficient_candles":       "Not enough candles: need at least {min}, got {actual}",
		"modules.support_resistance.reason.non_positive_close":         "The latest close is not positive; the data looks abnormal",
		"modules.support_resistance.reason.no_level_min_touches":       "No key level in the lookback window reached {min_touches} touches",
		"modules.support_resistance.reason.no_level_nearby":            "The close neither broke out through nor came near any key level",
		"modules.support_resistance.reason.breakout_above_resistance":  "Close {close} broke out above resistance {level} (touched {touches} times)",
		"modules.support_resistance.reason.breakdown_below_support":    "Close {close} broke down below support {level} (touched {touches} times)",
		"modules.support_resistance.reason.pullback_to_support":        "Close {close} pulled back to support {level} (touched {touches} times)",
		"modules.support_resistance.reason.test_resistance_from_below": "Close {close} tested resistance {level} from below (touched {touches} times)",
		"modules.support_resistance.reason.no_event":                   "No event",
	})
	register(LangZH, map[string]string{
		"modules.support_resistance.description": "基于回看窗口内摆动高低点的聚类，计算支撑位与阻力位，并在价格突破、跌破或回踩关键位时输出信号。",

		"modules.support_resistance.param.lookback":         "回看窗口，参与关键位计算的 K 线根数。",
		"modules.support_resistance.param.pivot_strength":   "摆动点强度：一根 K 线的高点要高于左右各 N 根才算摆动高点，低点同理。数值越大识别出的点越少但越显著。",
		"modules.support_resistance.param.tolerance":        "聚类容差，以价格的相对比例计。0.005 表示相差 0.5% 以内的摆动点归为同一个关键位。",
		"modules.support_resistance.param.min_touches":      "一个关键位至少要被触及的次数，低于该次数的位被丢弃。",
		"modules.support_resistance.param.breakout_confirm": "突破确认幅度：收盘价要越过关键位这个比例才算突破，用于过滤刺破。",
		"modules.support_resistance.param.proximity":        "回踩判定距离：收盘价与关键位的相对距离在该比例以内视为触及测试。",

		"modules.support_resistance.reason.insufficient_candles":       "K 线不足：需要至少 {min} 根，实际 {actual} 根",
		"modules.support_resistance.reason.non_positive_close":         "最新收盘价非正，数据异常",
		"modules.support_resistance.reason.no_level_min_touches":       "回看窗口内未找到触及次数达到 {min_touches} 次的关键位",
		"modules.support_resistance.reason.no_level_nearby":            "收盘价既未突破也未贴近任何关键位",
		"modules.support_resistance.reason.breakout_above_resistance":  "收盘价 {close} 向上突破阻力位 {level}（该位被触及 {touches} 次）",
		"modules.support_resistance.reason.breakdown_below_support":    "收盘价 {close} 向下跌破支撑位 {level}（该位被触及 {touches} 次）",
		"modules.support_resistance.reason.pullback_to_support":        "收盘价 {close} 回踩支撑位 {level}（该位被触及 {touches} 次）",
		"modules.support_resistance.reason.test_resistance_from_below": "收盘价 {close} 上测阻力位 {level}（该位被触及 {touches} 次）",
		"modules.support_resistance.reason.no_event":                   "无事件",
	})
}
