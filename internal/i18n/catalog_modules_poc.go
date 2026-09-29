package i18n

// Catalog entries for internal/modules/poc (Point of Control / volume-distribution center of mass).
func init() {
	register(LangEN, map[string]string{
		"modules.poc.description":               "Computes the Point of Control (volume-distribution center of mass): each candle's volume within the lookback window is attributed to the price bucket containing its typical price (average of high, low, and close), and the bucket with the most volume is the POC. Since only OHLCV data is available, this is a coarse approximation of the true tick-level volume distribution, not an exact value.",
		"modules.poc.param.lookback":            "Lookback window: the number of candles included in the volume-distribution calculation.",
		"modules.poc.param.bucket_count":        "How many buckets to split the lookback window's price range into. More buckets give finer price resolution, but each bucket then gets fewer samples.",
		"modules.poc.param.proximity":           "Touch-detection distance: the close price is considered to have touched the POC when their relative distance is within this ratio. Same meaning as the identically-named parameter in support_resistance.",
		"modules.poc.reason.insufficient_data":  "Not enough candles: at least {required} required, got {actual}",
		"modules.poc.reason.no_price_movement":  "No price movement within the lookback window, so a volume distribution can't be computed",
		"modules.poc.reason.non_positive_close": "Latest close price is not positive, data anomaly",
		"modules.poc.reason.not_near_poc":       "Close price hasn't touched the vicinity of the volume distribution's Point of Control (POC)",
		"modules.poc.reason.touched_poc":        "Close price {close} touched the approximate POC {poc} (lookback {bars} candles, {buckets} price buckets)",
	})
	register(LangZH, map[string]string{
		"modules.poc.description":               "计算成交量分布重心（Point of Control）：把回看窗口内每根 K 线的成交量记到其典型价格（最高+最低+收盘取平均）所在的价格桶，成交量最大的桶即为 POC。只有 OHLCV 数据，是对真实逐笔成交量分布的粗粒度近似，不是精确值。",
		"modules.poc.param.lookback":            "回看窗口，参与成交量分布统计的 K 线根数。",
		"modules.poc.param.bucket_count":        "把回看窗口内的价格区间均分成多少个桶，桶越多价格分辨率越高，但每个桶落入的样本也越少。",
		"modules.poc.param.proximity":           "触及判定距离：收盘价与 POC 的相对距离在该比例以内视为触及，含义与 support_resistance 的同名参数一致。",
		"modules.poc.reason.insufficient_data":  "K 线不足：至少需要 {required} 根，实际 {actual} 根",
		"modules.poc.reason.no_price_movement":  "回看窗口内价格没有波动，无法计算成交量分布",
		"modules.poc.reason.non_positive_close": "最新收盘价非正，数据异常",
		"modules.poc.reason.not_near_poc":       "收盘价未触及成交量分布重心（POC）附近",
		"modules.poc.reason.touched_poc":        "收盘价 {close} 触及近似 POC {poc}（回看 {bars} 根 K 线，{buckets} 个价格桶）",
	})
}
