package newssentiment

import (
	"context"
	"strings"

	"tradeforge/pkg/types"
)

// SentimentProvider abstracts the source of news sentiment scoring.
//
// This is the extension point for later plugging in a real NLP/LLM sentiment
// analysis service: implement this interface and inject it when constructing
// the module, and the module's own algorithm (window filtering, recency
// weighting, threshold decision) needs no changes at all.
type SentimentProvider interface {
	// Name returns the data source's identifier; it's written into
	// Signal.Raw so it's immediately clear how a score was computed.
	Name() string
	// Score scores a single news item, in the range [-1, 1]: positive leans
	// bullish, negative leans bearish, 0 is neutral.
	Score(ctx context.Context, item types.NewsItem) (float64, error)
}

// KeywordSentimentProvider scores headlines using a fixed list of positive and negative keywords.
//
// Score = (positive keyword hits - negative keyword hits) / total hits; no
// hits at all scores 0 (neutral). This is only a placeholder implementation
// to keep the pipeline running without a real NLP/LLM sentiment service — its
// accuracy is far below genuine semantic understanding (it can't recognize
// negation, sarcasm, or context) and must never be used as a real sentiment
// signal for live decisions, so it explicitly tags its data source in the
// signal, letting downstream code refuse to let it through.
type KeywordSentimentProvider struct{}

// Name implements SentimentProvider.
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

// Score implements SentimentProvider.
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

// IsHeuristic reports whether a data source is the keyword placeholder
// implementation. The execution layer can use this to refuse to let a live
// strategy use it.
func IsHeuristic(p SentimentProvider) bool {
	_, ok := p.(KeywordSentimentProvider)
	return ok
}
