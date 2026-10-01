package i18n

// Catalog entries for internal/modules/newssentiment: the module's
// Description, its RequiredParams descriptions, and the Reason messages its
// Evaluate attaches to emitted signals.
func init() {
	register(LangEN, map[string]string{
		"modules.news_sentiment.description": "Scores news headlines relevant to the symbol within a lookback window, aggregates them into a recency-weighted sentiment score, and emits a directional signal once the score crosses a threshold.",

		"modules.news_sentiment.param.lookback_hours":      "Lookback window in hours; only news published within this window and relevant to the symbol is counted.",
		"modules.news_sentiment.param.sentiment_threshold": "Trigger threshold for the weighted sentiment score; a directional signal is only emitted once the score's absolute value exceeds this threshold.",
		"modules.news_sentiment.param.min_news_count":      "Minimum number of relevant news items required within the window to emit a directional signal; fewer than this is treated as insufficient data.",

		"modules.news_sentiment.reason.no_market_data":              "No market data available, so a reference time could not be determined.",
		"modules.news_sentiment.reason.insufficient_news":           "{count} relevant news item(s) in the window, short of the required minimum of {min_count}.",
		"modules.news_sentiment.reason.positive_threshold_exceeded": "{count} relevant news item(s) in the past {hours} hours; weighted sentiment score {score} exceeds the threshold of {threshold}.",
		"modules.news_sentiment.reason.negative_threshold_exceeded": "{count} relevant news item(s) in the past {hours} hours; weighted sentiment score {score} is below the threshold of -{threshold}.",
		"modules.news_sentiment.reason.below_threshold":             "{count} relevant news item(s) in the window; weighted sentiment score {score} did not reach the threshold of {threshold}.",

		"modules.news_sentiment.error.scoring_failed": "{module}: sentiment scoring failed: {error}",
	})
	register(LangZH, map[string]string{
		"modules.news_sentiment.description": "对回看窗口内与该标的相关的新闻标题打分，按新近程度加权聚合成情绪得分，得分越过阈值时输出方向信号。",

		"modules.news_sentiment.param.lookback_hours":      "回看窗口（小时），只统计这段时间内发布、且与该标的相关的新闻。",
		"modules.news_sentiment.param.sentiment_threshold": "加权情绪得分的触发阈值，得分绝对值超过该阈值才输出方向信号。",
		"modules.news_sentiment.param.min_news_count":      "窗口内至少要有这么多条相关新闻才输出方向信号，不足则视为数据不足。",

		"modules.news_sentiment.reason.no_market_data":              "没有行情数据，无法确定参考时间",
		"modules.news_sentiment.reason.insufficient_news":           "窗口内相关新闻 {count} 条，未达到最少 {min_count} 条的要求",
		"modules.news_sentiment.reason.positive_threshold_exceeded": "过去 {hours} 小时内 {count} 条相关新闻，加权情绪得分 {score}，超过 {threshold} 的阈值",
		"modules.news_sentiment.reason.negative_threshold_exceeded": "过去 {hours} 小时内 {count} 条相关新闻，加权情绪得分 {score}，低于 -{threshold} 的阈值",
		"modules.news_sentiment.reason.below_threshold":             "窗口内 {count} 条相关新闻，加权情绪得分 {score}，未达到 {threshold} 的阈值",

		"modules.news_sentiment.error.scoring_failed": "{module}：情绪打分失败：{error}",
	})
}
