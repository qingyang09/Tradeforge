package i18n

// Catalog entries for internal/modules/fakeout's module description, param
// descriptions, and Evaluate's Signal.Reason messages.
func init() {
	register(LangEN, map[string]string{
		"modules.fakeout.description": "Detects fakeouts against a consolidation range: first identifies a range in the recent market, treats the range's high as the 'prior high' and its low as the 'prior low', then checks whether the last few candles broke above/below the range and quickly reversed back -- a reversal is judged a fakeout. How the range is defined is controlled by range_mode: tight (default) requires the high/low spread to stay compact enough, which automatically excludes trending markets, but a consolidation spanning a very long time is more likely to get cut off early by this fixed tolerance; extreme doesn't test compactness at all, it just takes the highest/lowest price across the whole lookback window, suited to a consolidation that spans a long time and isn't easy to quantify as a specific number of candles. Only tracks one range closest to the current point, rather than producing a pile of fragmented key levels.",

		"modules.fakeout.param.range_mode":       "tight: the high/low spread must stay within the range_tightness tolerance to count as a consolidation, which automatically excludes trending markets, but a long consolidation is more likely to get cut off early by this fixed tolerance; extreme: doesn't test compactness at all, just takes the highest/lowest price across the entire range_lookback window as the prior high/low, suited to a consolidation that spans a long time and isn't easy to quantify as a specific number of candles, at the cost of a trending market also being dutifully reported as a range.",
		"modules.fakeout.param.range_lookback":   "Maximum number of candles to search backward for a consolidation range (excluding the recent candles used to scan for a fakeout). In extreme mode this is the actual window used to take the highest/lowest price, and it is never narrowed further.",
		"modules.fakeout.param.min_range_bars":   "The minimum number of candles required to form a valid consolidation range; fewer than this doesn't count as a consolidation and produces no monitorable range high/low.",
		"modules.fakeout.param.range_tightness":  "How tight a 'consolidation' must be: the ratio of the range's high-minus-low spread to the range's midpoint price. Exceeding this ratio means it no longer counts as sideways chop (it's still trending); the smaller the value, the tighter the consolidation must be. Has no effect when range_mode is extreme.",
		"modules.fakeout.param.breakout_confirm": "Breakout confirmation margin: the close must clear the range high/low by this ratio to count as a genuine breakout, filtering out mere wicks.",
		"modules.fakeout.param.reversal_window":  "Fakeout detection window: the breakout must reverse back within this many candles to count as a fakeout; a reversal beyond this window doesn't count (at that point it looks more like a normal pullback after a continuing trend, not this breakout itself failing).",
		"modules.fakeout.param.reversal_confirm": "Reversal confirmation margin: the latest close must fall back/rise back to within this ratio of the range high/low to count as a confirmed reversal. This is a separate threshold from breakout_confirm, independently controlling how strict breakout confirmation and reversal confirmation each are.",

		"modules.fakeout.reason.insufficient_bars":  "Not enough candles: need at least {required}, got {actual}",
		"modules.fakeout.reason.invalid_close":      "Latest close is not positive; data anomaly",
		"modules.fakeout.reason.no_range_found":     "No valid consolidation range found within the lookback window (price swing or duration didn't meet requirements)",
		"modules.fakeout.reason.no_fakeout_pattern": "A consolidation range was found, but no 'breakout then reversal' fakeout pattern occurred recently",
		"modules.fakeout.reason.fakeout_resistance": "Identified a consolidation range made up of {bars} candles, whose high {level} was broken above by a close of {breakout_close}, then reversed back to {reclaim_close} -- judged a fakeout",
		"modules.fakeout.reason.fakeout_support":    "Identified a consolidation range made up of {bars} candles, whose low {level} was broken below by a close of {breakout_close}, then reversed back to {reclaim_close} -- judged a false breakdown",
		"modules.fakeout.reason.no_event":           "No event",
	})
	register(LangZH, map[string]string{
		"modules.fakeout.description": "检测盘整区间的假突破：先在最近的一段行情里识别出一个区间，把区间高点当" +
			"'前高'、低点当'前低'，再看最近几根 K 线是否突破了区间高/低点后又很快收回——" +
			"收回则判定为假突破。区间怎么定由 range_mode 决定：tight（默认）要求高低点" +
			"幅度够紧凑，自动排除趋势行情，但跨度很长的盘整容易被这个固定容差提前截断；" +
			"extreme 不判断紧凑度，直接取回看窗口内的最高/最低价，适合说不清该量化成" +
			"多少根K线、但确实拖了很久的盘整。只关注一个离当前最近的区间，不产出大量" +
			"细碎的关键位。",

		"modules.fakeout.param.range_mode": "tight：高低点幅度必须在 range_tightness 容差内才算盘整，自动排除" +
			"趋势行情，但很长的盘整容易被固定容差提前截断；extreme：不判断紧凑度，" +
			"直接取 range_lookback 整个回看窗口内的最高/最低价当前高/前低，适合跨度很长、" +
			"说不清该量化成多少根K线的盘整，代价是趋势行情也会被老实报出区间高低点。",
		"modules.fakeout.param.range_lookback": "向前搜索盘整区间的最大根数（不含用于扫描假突破的最近几根）。" +
			"extreme 模式下这就是实际用来取最高/最低价的窗口大小，不会再收窄。",
		"modules.fakeout.param.min_range_bars": "构成一次有效盘整区间最少需要多少根 K 线；不足这个数量不算盘整，" +
			"不会产生可监控的区间高低点。",
		"modules.fakeout.param.range_tightness": "判定'盘整'的松紧度：区间最高价与最低价之差相对区间中枢价格的比例，" +
			"超过这个比例就不算横盘（说明还在趋势里），值越小要求盘整得越紧。" +
			"range_mode 为 extreme 时这个参数不生效。",
		"modules.fakeout.param.breakout_confirm": "突破确认幅度：收盘价要越过区间高/低点这个比例才算真正突破，用于过滤刺破。",
		"modules.fakeout.param.reversal_window": "假突破判定窗口：突破发生后最多几根 K 线内收回才算假突破，超过这个窗口" +
			"再收回不算（此时更像是趋势延续后的正常回调，而不是这次突破本身失败了）。",
		"modules.fakeout.param.reversal_confirm": "收回确认幅度：最新收盘价要跌回/涨回区间高/低点这个比例以内才算确认收回，" +
			"跟 breakout_confirm 是两个独立的阈值，分别控制突破和收回各自的确认严格度。",

		"modules.fakeout.reason.insufficient_bars":  "K 线不足：需要至少 {required} 根，实际 {actual} 根",
		"modules.fakeout.reason.invalid_close":      "最新收盘价非正，数据异常",
		"modules.fakeout.reason.no_range_found":     "未在回看窗口内找到有效盘整区间（价格波动幅度或维持时间不满足要求）",
		"modules.fakeout.reason.no_fakeout_pattern": "找到盘整区间，但最近未出现'突破后又收回'的假突破模式",
		"modules.fakeout.reason.fakeout_resistance": "识别到 {bars} 根 K 线构成的盘整区间，其高点 {level} 曾被收盘价 {breakout_close} 突破，随后收回至 {reclaim_close}，判定为假突破",
		"modules.fakeout.reason.fakeout_support":    "识别到 {bars} 根 K 线构成的盘整区间，其低点 {level} 曾被收盘价 {breakout_close} 跌破，随后收回至 {reclaim_close}，判定为假跌破",
		"modules.fakeout.reason.no_event":           "无事件",
	})
}
