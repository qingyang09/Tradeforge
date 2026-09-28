package newssentiment

import (
	"context"
	"strings"

	"tradeforge/pkg/types"
)

// SentimentProvider 是新闻情绪打分的来源抽象。
//
// 这是后续接入真实 NLP/LLM 情绪分析服务的扩展点：只要实现这个接口并在构造模块时
// 注入，模块算法本身（窗口过滤、新近度加权、阈值判定）完全不用改。
type SentimentProvider interface {
	// Name 返回数据源标识，会写进 Signal.Raw，让人一眼看出得分是怎么算的。
	Name() string
	// Score 给单条新闻打分，取值范围 [-1, 1]，正数偏多头、负数偏空头、0 为中性。
	Score(ctx context.Context, item types.NewsItem) (float64, error)
}

// KeywordSentimentProvider 用一份固定的正负面关键词表给标题打分。
//
// 得分 = (命中正面词数 - 命中负面词数) / 命中总词数，不命中任何关键词记为 0（中性）。
// 这只是让链路能在没有真实 NLP/LLM 情绪服务时跑起来的占位实现，精度远低于真实语义
// 理解（无法识别否定、讽刺、上下文），绝不能当作真实情绪信号用于实盘决策，因此它会
// 在信号里显式标注数据源，下游可据此拒绝放行。
type KeywordSentimentProvider struct{}

// Name 实现 SentimentProvider。
func (KeywordSentimentProvider) Name() string { return "keyword_heuristic" }

var positiveKeywords = []string{
	"surge", "rally", "bullish", "soar", "approval", "approved", "adoption",
	"partnership", "breakout", "record high", "all-time high", "inflow",
	"upgrade", "outperform", "rebound", "greenlight",
}

var negativeKeywords = []string{
	"crash", "plunge", "bearish", "hack", "hacked", "exploit", "lawsuit",
	"ban", "banned", "fraud", "collapse", "selloff", "sell-off", "outflow",
	"downgrade", "liquidation", "bankrupt", "bankruptcy", "default",
}

// Score 实现 SentimentProvider。
func (KeywordSentimentProvider) Score(_ context.Context, item types.NewsItem) (float64, error) {
	text := strings.ToLower(item.Headline)
	pos, neg := 0, 0
	for _, k := range positiveKeywords {
		if strings.Contains(text, k) {
			pos++
		}
	}
	for _, k := range negativeKeywords {
		if strings.Contains(text, k) {
			neg++
		}
	}
	if pos+neg == 0 {
		return 0, nil
	}
	return float64(pos-neg) / float64(pos+neg), nil
}

// IsHeuristic 报告某个数据源是否为关键词占位实现。执行层可据此拒绝让实盘策略使用。
func IsHeuristic(p SentimentProvider) bool {
	_, ok := p.(KeywordSentimentProvider)
	return ok
}
