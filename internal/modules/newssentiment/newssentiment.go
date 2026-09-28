// Package newssentiment 实现 news_sentiment 信号模块。
//
// 对回看窗口内与该标的相关的新闻标题打分，按新近程度线性衰减加权，聚合成一个
// [-1, 1] 的情绪得分；得分越过阈值时输出方向信号，方向跟随情绪（正面→看多，
// 负面→看空）。
//
// 情绪打分通过 SentimentProvider 接口注入，便于后续替换为真实的 NLP/LLM 打分服务；
// 当前默认实现是关键词占位打分，精度有限，信号里会显式标注 is_heuristic，
// 下游可据此拒绝其进入实盘。
package newssentiment

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName 是该模块在策略配置中的标识。
const ModuleName = "news_sentiment"

// Module 实现 news_sentiment 信号模块。
type Module struct {
	provider SentimentProvider
}

// New 用指定的情绪打分数据源构造模块。
func New(p SentimentProvider) *Module {
	if p == nil {
		p = KeywordSentimentProvider{}
	}
	return &Module{provider: p}
}

// NewDefault 用关键词占位打分构造模块。
func NewDefault() *Module { return New(KeywordSentimentProvider{}) }

// Provider 返回当前使用的情绪打分数据源。
func (m *Module) Provider() SentimentProvider { return m.provider }

// Name 实现 modules.SignalModule。
func (m *Module) Name() string { return ModuleName }

// Description 实现 modules.SignalModule。
func (m *Module) Description() string {
	return "对回看窗口内与该标的相关的新闻标题打分，按新近程度加权聚合成情绪得分，得分越过阈值时输出方向信号。"
}

// RequiredParams 实现 modules.SignalModule。
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "lookback_hours", Type: types.ParamInt, Default: 24,
			Min: types.F(1), Max: types.F(168),
			Description: "回看窗口（小时），只统计这段时间内发布、且与该标的相关的新闻。",
		},
		{
			Name: "sentiment_threshold", Type: types.ParamFloat, Default: 0.3,
			Min: types.F(0.05), Max: types.F(1.0),
			Description: "加权情绪得分的触发阈值，得分绝对值超过该阈值才输出方向信号。",
		},
		{
			Name: "min_news_count", Type: types.ParamInt, Default: 1,
			Min: types.F(1), Max: types.F(50),
			Description: "窗口内至少要有这么多条相关新闻才输出方向信号，不足则视为数据不足。",
		},
	}
}

// Evaluate 实现 modules.SignalModule。
func (m *Module) Evaluate(ctx context.Context, md types.MarketData, params map[string]any) (types.Signal, error) {
	p, err := types.ResolveParams(ModuleName, m.RequiredParams(), params)
	if err != nil {
		return types.Signal{}, err
	}
	if err := ctx.Err(); err != nil {
		return types.Signal{}, err
	}

	lookbackHours := types.MustInt(p, "lookback_hours")
	threshold := types.MustFloat(p, "sentiment_threshold")
	minCount := types.MustInt(p, "min_news_count")

	now := md.Time()
	price := decimal.Zero
	if last, ok := md.Last(); ok {
		price = last.Close
	}
	neutral := func(reason string) types.Signal {
		s := types.NeutralSignal(ModuleName, md.Symbol, reason, now)
		s.Price = price
		return s
	}

	if now.IsZero() {
		return neutral("没有行情数据，无法确定参考时间"), nil
	}

	window := time.Duration(lookbackHours) * time.Hour
	windowStart := now.Add(-window)

	type scoredItem struct {
		item   types.NewsItem
		score  float64
		weight float64
	}
	var items []scoredItem
	for _, n := range md.News {
		if !newsApplies(md.Symbol, n.Symbols) {
			continue
		}
		if n.PublishedAt.Before(windowStart) || n.PublishedAt.After(now) {
			continue
		}
		score, err := m.provider.Score(ctx, n)
		if err != nil {
			return types.Signal{}, fmt.Errorf("%s：情绪打分失败：%w", ModuleName, err)
		}
		weight := 1 - now.Sub(n.PublishedAt).Hours()/window.Hours()
		if weight < 0 {
			weight = 0
		}
		items = append(items, scoredItem{item: n, score: score, weight: weight})
	}

	raw := map[string]any{
		"provider":     m.provider.Name(),
		"is_heuristic": IsHeuristic(m.provider),
		"window_hours": lookbackHours,
		"news_count":   len(items),
	}

	if len(items) < minCount {
		s := neutral(fmt.Sprintf("窗口内相关新闻 %d 条，未达到最少 %d 条的要求", len(items), minCount))
		s.Raw = raw
		return s, nil
	}

	weightSum, scoreSum := 0.0, 0.0
	headlines := make([]map[string]any, 0, len(items))
	for _, it := range items {
		weightSum += it.weight
		scoreSum += it.weight * it.score
		headlines = append(headlines, map[string]any{
			"source": it.item.Source, "headline": it.item.Headline,
			"published_at": it.item.PublishedAt, "score": it.score, "weight": it.weight,
		})
	}
	sort.Slice(headlines, func(i, j int) bool {
		return headlines[i]["published_at"].(time.Time).After(headlines[j]["published_at"].(time.Time))
	})
	raw["headlines"] = truncate(headlines, 20)

	var weighted float64
	if weightSum > 0 {
		weighted = scoreSum / weightSum
	}
	raw["weighted_score"] = weighted

	switch {
	case weighted >= threshold:
		return types.Signal{
			Module: ModuleName, Symbol: md.Symbol, Direction: types.DirectionLong,
			Confidence: confidence(weighted, threshold, len(items)),
			Timestamp:  now, Price: price,
			Reason: fmt.Sprintf("过去 %d 小时内 %d 条相关新闻，加权情绪得分 %.2f，超过 %.2f 的阈值",
				lookbackHours, len(items), weighted, threshold),
			Raw: raw,
		}, nil
	case weighted <= -threshold:
		return types.Signal{
			Module: ModuleName, Symbol: md.Symbol, Direction: types.DirectionShort,
			Confidence: confidence(-weighted, threshold, len(items)),
			Timestamp:  now, Price: price,
			Reason: fmt.Sprintf("过去 %d 小时内 %d 条相关新闻，加权情绪得分 %.2f，低于 -%.2f 的阈值",
				lookbackHours, len(items), weighted, threshold),
			Raw: raw,
		}, nil
	}

	s := neutral(fmt.Sprintf("窗口内 %d 条相关新闻，加权情绪得分 %.2f，未达到 %.2f 的阈值",
		len(items), weighted, threshold))
	s.Raw = raw
	return s, nil
}

// newsApplies 判断一条新闻是否与该标的相关：未标注标的的新闻视为全市场新闻，
// 对所有标的都生效；标注了的新闻要求标签与标的完全匹配，或是标的的前缀
// （如标签 "BTC" 匹配标的 "BTCUSDT"）。
func newsApplies(symbol string, tags []string) bool {
	if len(tags) == 0 {
		return true
	}
	up := strings.ToUpper(symbol)
	for _, t := range tags {
		tu := strings.ToUpper(t)
		if tu == up || (tu != "" && strings.HasPrefix(up, tu)) {
			return true
		}
	}
	return false
}

// confidence 把情绪得分超过阈值的幅度映射到 [0.5, 0.95]。
// 只有一条新闻支撑时缺乏交叉印证，置信度打七折。
func confidence(score, threshold float64, count int) float64 {
	base := 0.5
	if threshold < 1 {
		excess := (score - threshold) / (1 - threshold)
		base = 0.5 + 0.45*clamp(excess, 0, 1)
	}
	if count == 1 {
		base = 0.5 + (base-0.5)*0.7
	}
	return clamp(base, 0.5, 0.95)
}

func clamp(v, lo, hi float64) float64 {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}

func truncate(items []map[string]any, n int) []map[string]any {
	if len(items) <= n {
		return items
	}
	return items[:n]
}
