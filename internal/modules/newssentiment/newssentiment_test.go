package newssentiment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

// flatMarketData builds a market with the last candle's close time equal to
// base, making it easy to build news items "published N hours ago" using a
// relative offset.
func flatMarketData(symbol string, news ...types.NewsItem) types.MarketData {
	c := types.Candle{
		OpenTime: base.Add(-time.Hour), CloseTime: base,
		Open: decimal.NewFromInt(100), High: decimal.NewFromInt(100),
		Low: decimal.NewFromInt(100), Close: decimal.NewFromInt(100),
		Volume: decimal.NewFromInt(1000),
	}
	return types.MarketData{Symbol: symbol, Timeframe: types.TF1h, Candles: []types.Candle{c}, News: news}
}

func item(hoursAgo float64, headline string, symbols ...string) types.NewsItem {
	return types.NewsItem{
		Source: "test", Headline: headline,
		PublishedAt: base.Add(-time.Duration(hoursAgo * float64(time.Hour))),
		Symbols:     symbols,
	}
}

// ---- Correctness of KeywordSentimentProvider's scoring itself. ----

func TestKeywordProviderScoresKnownHeadlines(t *testing.T) {
	p := KeywordSentimentProvider{}
	cases := []struct {
		headline string
		want     float64
	}{
		{"Bitcoin ETF approval sparks massive rally", 1},
		{"Exchange hacked, users report huge losses amid lawsuit", -1},
		{"Bitcoin price steady ahead of Fed meeting", 0},
		{"Rally fades as SEC files lawsuit over alleged fraud", -1.0 / 3},
	}
	for _, tc := range cases {
		t.Run(tc.headline, func(t *testing.T) {
			got, err := p.Score(context.Background(), types.NewsItem{Headline: tc.headline})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("Score(%q) = %v, want %v", tc.headline, got, tc.want)
			}
		})
	}
}

// ---- newsApplies: symbol relevance filtering. ----

func TestNewsAppliesMatching(t *testing.T) {
	cases := []struct {
		name   string
		symbol string
		tags   []string
		want   bool
	}{
		{"no tags treated as market-wide news", "BTCUSDT", nil, true},
		{"exact match", "BTCUSDT", []string{"BTCUSDT"}, true},
		{"base asset prefix match", "BTCUSDT", []string{"BTC"}, true},
		{"case insensitive", "btcusdt", []string{"BTC"}, true},
		{"unrelated symbol", "BTCUSDT", []string{"ETH"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newsApplies(tc.symbol, tc.tags); got != tc.want {
				t.Errorf("newsApplies(%q, %v) = %v, want %v", tc.symbol, tc.tags, got, tc.want)
			}
		})
	}
}

// ---- Evaluate: end-to-end behavior. ----

func TestBullishNewsProducesLong(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(1, "Bitcoin ETF approval sparks massive rally", "BTC"),
		item(2, "Analysts note strong adoption and inflow into BTC funds", "BTC"),
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Fatalf("Direction = %s, want LONG (reason: %s)", sig.Direction, sig.Reason.Key)
	}
	if sig.Raw["news_count"] != 2 {
		t.Errorf("news_count = %v, want 2", sig.Raw["news_count"])
	}
	if sig.Raw["is_heuristic"] != true {
		t.Error("the default data source should be tagged is_heuristic=true")
	}
}

func TestBearishNewsProducesShort(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(1, "Exchange hacked, funds stolen amid exploit", "BTC"),
		item(2, "Regulators file lawsuit alleging fraud and ban trading", "BTC"),
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionShort {
		t.Fatalf("Direction = %s, want SHORT (reason: %s)", sig.Direction, sig.Reason.Key)
	}
}

// Neutral/contradictory headlines should cancel each other out and not trigger a directional signal.
func TestNeutralHeadlinesStayNeutral(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(1, "Bitcoin price steady ahead of Fed meeting", "BTC"),
		item(2, "Market awaits macro data release", "BTC"),
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL", sig.Direction)
	}
}

// News about an unrelated symbol must be filtered out, even with an extremely charged headline.
func TestUnrelatedSymbolNewsIgnored(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(1, "Ethereum ETF approval sparks massive rally", "ETH"),
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL (news is unrelated to the symbol)", sig.Direction)
	}
	if sig.Raw["news_count"] != 0 {
		t.Errorf("news_count = %v, want 0", sig.Raw["news_count"])
	}
}

// News outside the window (published before lookback_hours) must be excluded.
func TestNewsOutsideWindowIgnored(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(48, "Bitcoin ETF approval sparks massive rally", "BTC"), // 48 hours ago, outside the default 24-hour window
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, map[string]any{"lookback_hours": 24})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL (news is outside the lookback window)", sig.Direction)
	}
}

// News with a future timestamp (bad data or clock skew) should not be counted, to avoid using "future information".
func TestFutureDatedNewsIgnored(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(-1, "Bitcoin ETF approval sparks massive rally", "BTC"), // published after md.Time()
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL (future-dated news should not count)", sig.Direction)
	}
}

func TestMinNewsCountEnforced(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(1, "Bitcoin ETF approval sparks massive rally", "BTC"),
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, map[string]any{"min_news_count": 2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL (min_news_count not reached)", sig.Direction)
	}
}

// A signal backed by a single news item should have lower confidence than one backed by two agreeing items.
func TestConfidenceLowerWithSingleHeadline(t *testing.T) {
	single := flatMarketData("BTCUSDT", item(1, "Bitcoin ETF approval sparks massive rally", "BTC"))
	double := flatMarketData("BTCUSDT",
		item(1, "Bitcoin ETF approval sparks massive rally", "BTC"),
		item(1, "Major partnership announcement drives adoption breakout", "BTC"),
	)
	sigSingle, err := NewDefault().Evaluate(context.Background(), single, nil)
	if err != nil {
		t.Fatal(err)
	}
	sigDouble, err := NewDefault().Evaluate(context.Background(), double, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sigSingle.Direction != types.DirectionLong || sigDouble.Direction != types.DirectionLong {
		t.Fatalf("expected both to be LONG, got single=%s double=%s", sigSingle.Direction, sigDouble.Direction)
	}
	if sigSingle.Confidence >= sigDouble.Confidence {
		t.Errorf("single-headline confidence %v should be lower than the two-agreeing-headlines confidence %v", sigSingle.Confidence, sigDouble.Confidence)
	}
}

func TestNoNewsIsNeutral(t *testing.T) {
	md := flatMarketData("BTCUSDT")
	sig, err := NewDefault().Evaluate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("no news should not error, got: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL", sig.Direction)
	}
	if !sig.Price.Equal(decimal.NewFromInt(100)) {
		t.Errorf("Price = %s, want it to carry the reference close price of 100", sig.Price)
	}
	if sig.Timestamp.IsZero() {
		t.Error("Timestamp must not be zero")
	}
}

func TestEmptyMarketDataIsNeutral(t *testing.T) {
	sig, err := NewDefault().Evaluate(context.Background(), types.MarketData{Symbol: "BTCUSDT"}, nil)
	if err != nil {
		t.Fatalf("empty market data should not error, got: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL", sig.Direction)
	}
}

func TestInvalidParamsRejected(t *testing.T) {
	md := flatMarketData("BTCUSDT", item(1, "Bitcoin rally", "BTC"))
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"lookback window out of range", map[string]any{"lookback_hours": 9999}},
		{"threshold out of range", map[string]any{"sentiment_threshold": 1.5}},
		{"min news count not an integer", map[string]any{"min_news_count": 2.5}},
		{"unknown parameter", map[string]any{"decay": "linear"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewDefault().Evaluate(context.Background(), md, tc.params); err == nil {
				t.Fatalf("expected %v to be rejected, but it passed", tc.params)
			}
		})
	}
}

func TestContextCancellationRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	md := flatMarketData("BTCUSDT")
	if _, err := NewDefault().Evaluate(ctx, md, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

// erroringProvider verifies that when the scoring data source errors, the
// module propagates it faithfully instead of swallowing it or faking a
// neutral signal.
type erroringProvider struct{ err error }

func (erroringProvider) Name() string { return "erroring" }
func (p erroringProvider) Score(context.Context, types.NewsItem) (float64, error) {
	return 0, p.err
}

func TestProviderErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	m := New(erroringProvider{err: boom})
	md := flatMarketData("BTCUSDT", item(1, "Bitcoin rally", "BTC"))
	_, err := m.Evaluate(context.Background(), md, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the error to propagate as %v, got %v", boom, err)
	}
}
