package supportresistance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func newBuilder() *synth.Builder { return synth.New("BTCUSDT", types.TF1h, base) }

func decimalFromFloat(f float64) decimal.Decimal { return decimal.NewFromFloat(f) }

// Oscillates repeatedly between 100 and 110, building a support and
// resistance level that gets touched several times; the final candle
// decisively closes above 110 -> should be recognized as a breakout.
func TestBreakoutAboveResistance(t *testing.T) {
	b := newBuilder().Oscillate(64, 100, 110, 1000)
	b.AddBar(110, 113, 1500, 0.6) // closes at 113, clearly above 110

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"lookback": 200, "pivot_strength": 1, "tolerance": 0.005,
		"min_touches": 2, "breakout_confirm": 0.001, "proximity": 0.003,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Fatalf("Direction = %s, want LONG. Reason: %s, raw=%v", sig.Direction, sig.Reason.Key, sig.Raw)
	}
	if got := sig.Raw["event"]; got != eventBreakout {
		t.Errorf("event = %v, want %s", got, eventBreakout)
	}
	if sig.Confidence <= 0 || sig.Confidence > 1 {
		t.Errorf("confidence %v is outside (0,1]", sig.Confidence)
	}
	if sig.Reason.IsZero() {
		t.Error("Reason must not be empty; explainability requires every signal to state why it fired")
	}
	ws, ok := sig.Raw["window_start"].(string)
	if !ok || ws == "" {
		t.Fatalf("window_start should be a non-empty string for the chart to mark the analysis window's start, got raw=%v", sig.Raw)
	}
	if _, err := time.Parse(time.RFC3339, ws); err != nil {
		t.Errorf("window_start should be a valid RFC3339 time, got %q: %v", ws, err)
	}
}

// When there aren't enough key levels (min_touches set very high), window_start
// should still be present, so the chart knows what period the system looked at
// even if no key level was found.
func TestWindowStartPresentWhenNoLevelsFound(t *testing.T) {
	b := newBuilder().Oscillate(64, 100, 110, 1000)
	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"pivot_strength": 1, "min_touches": 10, // 64 candles of oscillation can't produce a level touched this many times
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ws, _ := sig.Raw["window_start"].(string); ws == "" {
		t.Errorf("window_start should still be present even when no key level is found, got raw=%v", sig.Raw)
	}
}

// Same oscillation range, but the final candle decisively breaks below 100 -> should be recognized as a breakdown.
func TestBreakdownBelowSupport(t *testing.T) {
	b := newBuilder().Oscillate(64, 100, 110, 1000)
	// The price isn't necessarily at 100 when Oscillate ends; pull it back near 100 first, then break below.
	b.AddBar(b.Build().Candles[b.Len()-1].Close.InexactFloat64(), 100, 1000, 0.45)
	b.AddBar(100, 97, 1500, 0.4)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"pivot_strength": 1, "min_touches": 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionShort {
		t.Fatalf("Direction = %s, want SHORT. Reason: %s, raw=%v", sig.Direction, sig.Reason.Key, sig.Raw)
	}
	if got := sig.Raw["event"]; got != eventBreakdown {
		t.Errorf("event = %v, want %s", got, eventBreakdown)
	}
}

// Price sits right at support without breaking below -> a retest, direction long, and confidence should be lower than a breakout's.
func TestTestSupportProducesLowerConfidenceThanBreakout(t *testing.T) {
	b := newBuilder().Oscillate(64, 100, 110, 1000)
	last := b.Build().Candles[b.Len()-1].Close.InexactFloat64()
	b.AddBar(last, 100.2, 1000, 0.5) // closes at 100.2, within 100's proximity range

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"pivot_strength": 1, "min_touches": 2, "proximity": 0.005,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Raw["event"] != eventTestSupp {
		t.Fatalf("event = %v, want %s (reason: %s)", sig.Raw["event"], eventTestSupp, sig.Reason.Key)
	}
	if sig.Direction != types.DirectionLong {
		t.Errorf("Direction = %s, want LONG", sig.Direction)
	}
	touches, ok := sig.Raw["level_touches"].(int)
	if !ok {
		t.Fatalf("raw.level_touches has type %T, want int", sig.Raw["level_touches"])
	}
	if breakout := confidenceFor(eventBreakout, touches); sig.Confidence >= breakout {
		t.Errorf("at the same %d touches, retest confidence %v should not reach the breakout confidence of %v", touches, sig.Confidence, breakout)
	}
}

// Edge case: when there aren't enough candles to identify a pivot, return a neutral signal + nil error, not an error.
func TestInsufficientDataReturnsNeutral(t *testing.T) {
	b := newBuilder()
	for i := 0; i < 4; i++ {
		b.AddFlat(100, 100)
	}
	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"pivot_strength": 3})
	if err != nil {
		t.Fatalf("insufficient data is a normal condition and should not error, got: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL", sig.Direction)
	}
	if sig.Confidence != 0 {
		t.Errorf("neutral signal confidence = %v, want 0", sig.Confidence)
	}
	if sig.Reason.IsZero() {
		t.Error("a neutral signal must also state its reason")
	}
}

// With no candles at all, also return neutral, never panic.
func TestEmptyCandles(t *testing.T) {
	sig, err := New().Evaluate(context.Background(), types.MarketData{Symbol: "BTCUSDT"}, nil)
	if err != nil {
		t.Fatalf("empty data should not error, got: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL", sig.Direction)
	}
}

// Invalid parameters must error (it's the caller's problem), not silently fall back to defaults.
func TestInvalidParamsRejected(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"lookback out of range", map[string]any{"lookback": 100000}},
		{"negative tolerance", map[string]any{"tolerance": -0.1}},
		{"unknown parameter", map[string]any{"magic": 1}},
		{"wrong type", map[string]any{"lookback": "200"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New().Evaluate(context.Background(), b.Build(), tc.params); err == nil {
				t.Fatalf("expected %v to be rejected, but it passed", tc.params)
			}
		})
	}
}

// A monotonically rising market shouldn't produce a repeatedly-touched key level; it should output neutral.
func TestMonotonicTrendHasNoRepeatedLevels(t *testing.T) {
	b := newBuilder().Trend(80, 100, 200, 1000, 0.6)
	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"min_touches": 3, "pivot_strength": 2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction in a monotonic trend = %s, want NEUTRAL (raw=%v)", sig.Direction, sig.Raw)
	}
}

func TestContextCancellationRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	if _, err := New().Evaluate(ctx, b.Build(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

// Clustering must use a relative ratio, not an absolute price difference, or the same parameter set would behave inconsistently across symbols at different price levels.
func TestClusteringIsRelativeNotAbsolute(t *testing.T) {
	tolerance := decimalFromFloat(0.01)

	// Low-priced symbol: 100 and 100.5 differ by 0.5%, should cluster into one.
	low := clusterLevels([]pivot{
		{Price: decimalFromFloat(100), Index: 0},
		{Price: decimalFromFloat(100.5), Index: 1},
	}, tolerance, "high")
	if len(low) != 1 {
		t.Errorf("100 and 100.5 should cluster into 1 group at 1%% tolerance, got %d groups", len(low))
	}

	// High-priced symbol: 50000 and 50250 also differ by 0.5%, should also cluster into one.
	high := clusterLevels([]pivot{
		{Price: decimalFromFloat(50000), Index: 0},
		{Price: decimalFromFloat(50250), Index: 1},
	}, tolerance, "high")
	if len(high) != 1 {
		t.Errorf("50000 and 50250 should cluster into 1 group at 1%% tolerance, got %d groups", len(high))
	}

	// A 5% difference must be split apart.
	far := clusterLevels([]pivot{
		{Price: decimalFromFloat(100), Index: 0},
		{Price: decimalFromFloat(105), Index: 1},
	}, tolerance, "high")
	if len(far) != 2 {
		t.Errorf("100 and 105 should split into 2 groups at 1%% tolerance, got %d groups", len(far))
	}
}

func TestFindPivotsIgnoresPlateaus(t *testing.T) {
	// Consecutive equal highs don't constitute a swing point, or clustering would artificially inflate the touch count.
	b := newBuilder()
	for _, p := range []float64{100, 101, 105, 105, 105, 101, 100} {
		b.AddFlat(p, 100)
	}
	highs, _ := findPivots(b.Build().Candles, 1)
	if len(highs) != 0 {
		t.Errorf("a plateau shape should not produce a swing high, got %d", len(highs))
	}
}
