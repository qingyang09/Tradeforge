package i18n

// Catalog entries for internal/modules/cvdorderflow: the module's
// Description, its RequiredParams descriptions, the reasons attached to the
// Signals its Evaluate produces, and the CandleFlowProvider's data-source
// error.
func init() {
	register(LangEN, map[string]string{
		"modules.cvd_orderflow.description": "Computes cumulative volume delta (CVD) to detect taker buy/sell imbalance within the window, and divergence between price and CVD.",

		"modules.cvd_orderflow.param.window":               "Computation window: the number of candles used for CVD and imbalance statistics.",
		"modules.cvd_orderflow.param.imbalance_threshold":  "Imbalance threshold: triggers once the net buy/sell difference within the window, as a share of total volume, exceeds this value. Range 0~1.",
		"modules.cvd_orderflow.param.divergence_threshold": "Divergence threshold: the normalized magnitude of the CVD move must exceed this value to count as a valid divergence.",
		"modules.cvd_orderflow.param.min_price_move":       "Minimum price-move ratio required for a divergence, used to filter out false divergences when price has barely moved.",
		"modules.cvd_orderflow.param.detect":               "Detection mode: both detects imbalance and divergence together, or restrict to just one.",

		"modules.cvd_orderflow.reason.insufficient_candles": "Not enough candles: need at least {required}, got {actual}",
		"modules.cvd_orderflow.reason.zero_volume":          "Total volume within the window is zero; CVD imbalance cannot be computed",
		"modules.cvd_orderflow.reason.no_trigger":           "CVD imbalance within the window is {imbalance}, price moved {price_move}%; neither the imbalance nor the divergence condition triggered",
		"modules.cvd_orderflow.reason.divergence_bearish":   "CVD divergence: price rose but aggressive buying net flowed out (price moved {price_move}%, CVD imbalance {imbalance}, threshold {threshold})",
		"modules.cvd_orderflow.reason.divergence_bullish":   "CVD divergence: price fell but aggressive buying net flowed in (price moved {price_move}%, CVD imbalance {imbalance}, threshold {threshold})",
		"modules.cvd_orderflow.reason.imbalance_buy":        "CVD imbalance: net aggressive buying within the window is {pct}% of total volume (threshold {threshold_pct}%)",
		"modules.cvd_orderflow.reason.imbalance_sell":       "CVD imbalance: net aggressive selling within the window is {pct}% of total volume (threshold {threshold_pct}%)",

		"modules.cvd_orderflow.error.missing_taker_volume": "Market data has no taker-buy-volume field ({count} candles); CVD cannot be computed",
		"modules.cvd_orderflow.error.fetch_failed":         "{module}: failed to read order-flow data: {error}",
		"modules.cvd_orderflow.error.length_mismatch":      "{module}: the order-flow data source returned {got} entries, which doesn't match {want} candles",
	})
	register(LangZH, map[string]string{
		"modules.cvd_orderflow.description": "计算累计成交量差（CVD），检测窗口内的主动买卖失衡，以及价格与 CVD 之间的背离。",

		"modules.cvd_orderflow.param.window":               "计算窗口，参与 CVD 与失衡度统计的 K 线根数。",
		"modules.cvd_orderflow.param.imbalance_threshold":  "失衡阈值：窗口内净买卖差占总成交量的比例超过该值即触发。取值 0~1。",
		"modules.cvd_orderflow.param.divergence_threshold": "背离阈值：CVD 变动的归一化幅度超过该值才认定为有效背离。",
		"modules.cvd_orderflow.param.min_price_move":       "背离所需的最小价格变动比例，用于排除价格几乎没动时的伪背离。",
		"modules.cvd_orderflow.param.detect":               "检测模式：both 同时检测失衡与背离，或只检测其中一种。",

		"modules.cvd_orderflow.reason.insufficient_candles": "K 线不足：需要至少 {required} 根，实际 {actual} 根",
		"modules.cvd_orderflow.reason.zero_volume":          "窗口内总成交量为零，无法计算 CVD 失衡度",
		"modules.cvd_orderflow.reason.no_trigger":           "窗口内 CVD 失衡度 {imbalance}，价格变动 {price_move}%，未触发失衡或背离条件",
		"modules.cvd_orderflow.reason.divergence_bearish":   "CVD 背离：价格上涨但主动买盘净流出（价格变动 {price_move}%，CVD 失衡度 {imbalance}，阈值 {threshold}）",
		"modules.cvd_orderflow.reason.divergence_bullish":   "CVD 背离：价格下跌但主动买盘净流入（价格变动 {price_move}%，CVD 失衡度 {imbalance}，阈值 {threshold}）",
		"modules.cvd_orderflow.reason.imbalance_buy":        "CVD 失衡：窗口内主动买入净占总成交量的 {pct}%（阈值 {threshold_pct}%）",
		"modules.cvd_orderflow.reason.imbalance_sell":       "CVD 失衡：窗口内主动卖出净占总成交量的 {pct}%（阈值 {threshold_pct}%）",

		"modules.cvd_orderflow.error.missing_taker_volume": "行情数据缺少主动买入量字段（{count} 根 K 线），无法计算 CVD",
		"modules.cvd_orderflow.error.fetch_failed":         "{module}：读取订单流数据失败：{error}",
		"modules.cvd_orderflow.error.length_mismatch":      "{module}：订单流数据源返回 {got} 条，与 {want} 根 K 线不匹配",
	})
}
