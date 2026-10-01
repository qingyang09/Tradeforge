// Package newssentiment implements the news_sentiment signal module.
//
// Scores news headlines relevant to the symbol within a lookback window,
// weights them by recency with linear decay, and aggregates them into a
// sentiment score in [-1, 1]; when the score crosses a threshold it emits a
// directional signal, with direction following sentiment (positive -> long,
// negative -> short).
//
// Sentiment scoring is injected via the SentimentProvider interface, so it
// can later be swapped for a real NLP/LLM scoring service; the current
// default implementation is a keyword-based placeholder scorer with limited
// accuracy — the signal explicitly tags is_heuristic so downstream code can
// refuse to let it go live.
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

// ModuleName is this module's identifier in strategy configs.
const ModuleName = "news_sentiment"

// Module implements the news_sentiment signal module.
type Module struct {
	provider SentimentProvider
}

// New builds the module with the given sentiment-scoring data source.
func New(p SentimentProvider) *Module {
	if p == nil {
		p = KeywordSentimentProvider{}
	}
	return &Module{provider: p}
}

// NewDefault builds the module using the keyword-based placeholder scorer.
func NewDefault() *Module { return New(KeywordSentimentProvider{}) }

// Provider returns the sentiment-scoring data source currently in use.
func (m *Module) Provider() SentimentProvider { return m.provider }

// Name implements modules.SignalModule.
func (m *Module) Name() string { return ModuleName }

// Description implements modules.SignalModule.
func (m *Module) Description() types.Message {
	return types.Msg("modules.news_sentiment.description")
}

// RequiredParams implements modules.SignalModule.
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "lookback_hours", Type: types.ParamInt, Default: 24,
			Min: types.F(1), Max: types.F(168),
			Description: types.Msg("modules.news_sentiment.param.lookback_hours"),
		},
		{
			Name: "sentiment_threshold", Type: types.ParamFloat, Default: 0.3,
			Min: types.F(0.05), Max: types.F(1.0),
			Description: types.Msg("modules.news_sentiment.param.sentiment_threshold"),
		},
		{
			Name: "min_news_count", Type: types.ParamInt, Default: 1,
			Min: types.F(1), Max: types.F(50),
			Description: types.Msg("modules.news_sentiment.param.min_news_count"),
		},
	}
}

// Evaluate implements modules.SignalModule.
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
	neutral := func(reason types.Message) types.Signal {
		s := types.NeutralSignal(ModuleName, md.Symbol, reason, now)
		s.Price = price
		return s
	}

	if now.IsZero() {
		return neutral(types.Msg("modules.news_sentiment.reason.no_market_data")), nil
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
			return types.Signal{}, &ScoringError{
				reason:  types.Msg("modules.news_sentiment.error.scoring_failed", "module", ModuleName, "error", err.Error()),
				wrapped: err,
			}
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
		s := neutral(types.Msg("modules.news_sentiment.reason.insufficient_news",
			"count", len(items), "min_count", minCount))
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
			Reason: types.Msg("modules.news_sentiment.reason.positive_threshold_exceeded",
				"hours", lookbackHours, "count", len(items), "score", fmt.Sprintf("%.2f", weighted),
				"threshold", fmt.Sprintf("%.2f", threshold)),
			Raw: raw,
		}, nil
	case weighted <= -threshold:
		return types.Signal{
			Module: ModuleName, Symbol: md.Symbol, Direction: types.DirectionShort,
			Confidence: confidence(-weighted, threshold, len(items)),
			Timestamp:  now, Price: price,
			Reason: types.Msg("modules.news_sentiment.reason.negative_threshold_exceeded",
				"hours", lookbackHours, "count", len(items), "score", fmt.Sprintf("%.2f", weighted),
				"threshold", fmt.Sprintf("%.2f", threshold)),
			Raw: raw,
		}, nil
	}

	s := neutral(types.Msg("modules.news_sentiment.reason.below_threshold",
		"count", len(items), "score", fmt.Sprintf("%.2f", weighted), "threshold", fmt.Sprintf("%.2f", threshold)))
	s.Raw = raw
	return s, nil
}

// newsApplies determines whether a news item is relevant to the symbol: an
// item with no symbol tags is treated as market-wide news and applies to
// every symbol; a tagged item requires a tag that exactly matches the symbol,
// or is a prefix of it (e.g. tag "BTC" matches symbol "BTCUSDT").
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

// confidence maps how far the sentiment score exceeds the threshold into [0.5, 0.95].
// With only one news item backing it there's no cross-confirmation, so confidence is scaled down by 30%.
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
