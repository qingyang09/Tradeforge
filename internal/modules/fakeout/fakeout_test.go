package fakeout

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func newBuilder() *synth.Builder { return synth.New("BTCUSDT", types.TF1h, base) }

// Oscillates between 100 and 110 to form a consolidation range, then one
// candle decisively breaks above the range high, followed by one that
// reverses back -- should be judged a fakeout, bearish.
func TestFakeoutResistanceProducesShort(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	b.AddBar(110, 113, 1500, 0.6) // breakout: clearly above the range high
	b.AddBar(113, 105, 1500, 0.4) // reversal: falls back below the range high

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.12, "reversal_window": 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionShort {
		t.Fatalf("Direction = %s, want SHORT. Reason: %s, raw=%v", sig.Direction, sig.Reason, sig.Raw)
	}
	if got := sig.Raw["event"]; got != eventFakeoutResistance {
		t.Errorf("event = %v, want %s", got, eventFakeoutResistance)
	}
	if got := sig.Raw["range_found"]; got != true {
		t.Errorf("range_found = %v, want true", got)
	}
	if sig.Confidence <= 0 || sig.Confidence > 1 {
		t.Errorf("confidence %v is outside (0,1]", sig.Confidence)
	}
	if sig.Reason == "" {
		t.Error("Reason must not be empty")
	}
}

// Symmetric scenario: a false breakdown below the range low, followed by a reversal, should be judged bullish.
func TestFakeoutSupportProducesLong(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	last := b.Build().Candles[b.Len()-1].Close.InexactFloat64()
	b.AddBar(last, 100, 1000, 0.45) // pull back inside the range
	b.AddBar(100, 97, 1500, 0.4)    // breakdown: clearly below the range low
	b.AddBar(97, 103, 1500, 0.6)    // reversal: back above the range low

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.12, "reversal_window": 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Fatalf("Direction = %s, want LONG. Reason: %s, raw=%v", sig.Direction, sig.Reason, sig.Raw)
	}
	if got := sig.Raw["event"]; got != eventFakeoutSupport {
		t.Errorf("event = %v, want %s", got, eventFakeoutSupport)
	}
}

// After a breakout, if price never reverses back (stays outside the range) -- must not be judged a fakeout.
func TestNoReversalStaysNeutral(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	b.AddBar(110, 113, 1500, 0.6)
	b.AddBar(113, 114, 1500, 0.6) // still outside the range, no reversal

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.12, "reversal_window": 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL (no reversal, must not be judged a fakeout). raw=%v", sig.Direction, sig.Raw)
	}
}

// A breakout that happened outside reversal_window (too long ago) has already
// fallen into the history used to compute the range, and is no longer a
// breakout candidate within the "scan window" -- it must not count as this
// fakeout. Regardless of whether the range ends up recomputed wider because
// of this, the final result must never be judged a fakeout.
func TestBreakoutOutsideReversalWindowIgnored(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	b.AddBar(110, 112, 1500, 0.6) // breakout, later pushed out of the scan window (with reversal_window=1 the window is only 1 candle)
	b.AddBar(112, 108, 1500, 0.4) // the only candle in the scan window: already back inside the range, no breakout of its own
	b.AddBar(108, 105, 1500, 0.4) // current candle: still below the level, but this isn't a "reversal" -- it never broke out

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.15, "reversal_window": 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL (breakout happened outside the window). raw=%v", sig.Direction, sig.Raw)
	}
}

// A consolidation range should produce only a single high and a single low, not a pile of fragmented key levels.
func TestConsolidationRangeIsSingleHighLow(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	b.AddBar(110, 108, 1500, 0.5) // closes inside the range, not a breakout

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.12, "reversal_window": 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Raw["range_found"] != true {
		t.Fatalf("should have found a consolidation range, raw=%v", sig.Raw)
	}
	hi, err := decimal.NewFromString(sig.Raw["range_high"].(string))
	if err != nil {
		t.Fatalf("failed to parse range_high: %v", err)
	}
	lo, err := decimal.NewFromString(sig.Raw["range_low"].(string))
	if err != nil {
		t.Fatalf("failed to parse range_low: %v", err)
	}
	if !hi.GreaterThan(lo) {
		t.Errorf("range_high(%s) should be greater than range_low(%s)", hi, lo)
	}
	wantHi := decimal.NewFromFloat(110.2)
	if hi.Sub(wantHi).Abs().GreaterThan(decimal.NewFromFloat(0.5)) {
		t.Errorf("range_high = %s, want it close to %s", hi, wantHi)
	}
}

// window_start must reflect the "analysis window's start" and be present
// whether or not a range was found -- the chart relies on it to mark "this
// is the history the system is looking at".
func TestWindowStartReflectsAnalysisWindow(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.12, "reversal_window": 1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ws, ok := sig.Raw["window_start"].(string)
	if !ok || ws == "" {
		t.Fatalf("window_start should be a non-empty string, got raw=%v", sig.Raw)
	}
	got, err := time.Parse(time.RFC3339, ws)
	if err != nil {
		t.Fatalf("window_start should be a valid RFC3339 time, got %q: %v", ws, err)
	}
	// With reversal_window=1 the scan window takes up 2 candles, leaving 38
	// candles that all go into levelHistory (not exceeding the default
	// range_lookback=60), so the window start should be exactly candle 0's open time.
	if !got.Equal(base) {
		t.Errorf("window_start = %s, want it to equal the first candle's open time %s", got, base)
	}
}

// Once a range is found, window_start should point to the range's actual
// start, not the entire rangeLookback window's start -- this is a bug that
// really happened: a user reported the "prior high" being captured
// inaccurately, and on investigation it turned out range_high wasn't
// computed wrong (it genuinely was the range's highest price) -- rather, the
// "analysis window start" marked on the chart sat further back than the
// range itself, so the user assumed the prior high should cover all the way
// out to that earlier marker, but the history between the marker and the
// range's actual start (which was never counted into range_high) happened to
// hide a taller wick, making it look like the "prior high" had been missed.
func TestWindowStartMatchesFoundRangeNotFullLookback(t *testing.T) {
	b := newBuilder()
	// The candles at the start deliberately include one very tall wick
	// (130) -- if it got counted into the "prior high", the range high
	// would far exceed the genuinely tight consolidation that follows; this
	// verifies that wick really does get excluded.
	b.Add(100, 130, 99, 101, 1000, 0.5)
	for i := 0; i < 4; i++ {
		b.Add(101, 103, 99, 101, 1000, 0.5)
	}
	tightRangeStart := b.Len() // the genuinely tight consolidation starts at this candle
	for i := 0; i < 15; i++ {
		b.Add(101, 102, 100, 101, 1000, 0.5)
	}
	for i := 0; i < 6; i++ {
		b.Add(101, 101.5, 100.5, 101, 1000, 0.5) // scan window, not a breakout
	}

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.03, "reversal_window": 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Raw["range_found"] != true {
		t.Fatalf("should have found a consolidation range, raw=%v", sig.Raw)
	}

	hi, err := decimal.NewFromString(sig.Raw["range_high"].(string))
	if err != nil {
		t.Fatalf("failed to parse range_high: %v", err)
	}
	if hi.GreaterThan(decimal.NewFromFloat(103)) {
		t.Errorf("range_high = %s, should not have counted the earlier wick's high of 130 (that candle isn't in the tight consolidation)", hi)
	}

	ws, ok := sig.Raw["window_start"].(string)
	if !ok || ws == "" {
		t.Fatalf("window_start should be a non-empty string, got raw=%v", sig.Raw)
	}
	got, err := time.Parse(time.RFC3339, ws)
	if err != nil {
		t.Fatalf("window_start is not valid RFC3339: %v", err)
	}
	wantStart := base.Add(time.Duration(tightRangeStart) * time.Hour)
	if !got.Equal(wantStart) {
		t.Errorf("window_start = %s, want it to equal the tight consolidation's actual start %s (not the full lookback window's start %s)",
			got, wantStart, base)
	}
}

// range_mode=extreme doesn't test tightness at all, and should include the
// highest/lowest price across the whole lookback window -- including the
// wick that would be excluded in tight mode (using the same setup as the
// previous test, but asserting the opposite: this time the 130 wick should
// be counted into range_high).
func TestExtremeModeIncludesOutlierWick(t *testing.T) {
	b := newBuilder()
	b.Add(100, 130, 99, 101, 1000, 0.5)
	for i := 0; i < 4; i++ {
		b.Add(101, 103, 99, 101, 1000, 0.5)
	}
	for i := 0; i < 15; i++ {
		b.Add(101, 102, 100, 101, 1000, 0.5)
	}
	for i := 0; i < 6; i++ {
		b.Add(101, 101.5, 100.5, 101, 1000, 0.5)
	}

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"range_mode": RangeModeExtreme, "min_range_bars": 10, "reversal_window": 5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Raw["range_found"] != true {
		t.Fatalf("should have found a range, raw=%v", sig.Raw)
	}
	hi, err := decimal.NewFromString(sig.Raw["range_high"].(string))
	if err != nil {
		t.Fatalf("failed to parse range_high: %v", err)
	}
	if !hi.Equal(decimal.NewFromFloat(130)) {
		t.Errorf("range_high = %s, extreme mode should have counted the lookback window's highest price of 130", hi)
	}

	ws, _ := sig.Raw["window_start"].(string)
	got, err := time.Parse(time.RFC3339, ws)
	if err != nil {
		t.Fatalf("window_start is not valid RFC3339: %v", err)
	}
	if !got.Equal(base) {
		t.Errorf("window_start = %s, extreme mode uses the entire lookback window, should equal the first candle's open time %s", got, base)
	}
}

// In range_mode=extreme, even a one-directional trending market should
// dutifully report the range's highest/lowest price, unlike tight mode which
// would refuse to identify a range as "not tight enough" -- this is exactly
// the tradeoff of switching to this mode: choosing it means the user has
// already decided this period is a consolidation.
func TestExtremeModeAcceptsTrendingMarket(t *testing.T) {
	b := newBuilder().Trend(40, 100, 200, 1000, 0.5)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"range_mode": RangeModeExtreme, "min_range_bars": 10,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Raw["range_found"] != true {
		t.Errorf("extreme mode doesn't test tightness, a trending market should still yield a range, raw=%v", sig.Raw)
	}
}

// A one-directional trending market isn't a consolidation and should not yield any range.
func TestTrendingMarketHasNoConsolidationRange(t *testing.T) {
	b := newBuilder().Trend(40, 100, 200, 1000, 0.5)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.03,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Raw["range_found"] != false {
		t.Errorf("a trending market should not yield a consolidation range, raw=%v", sig.Raw)
	}
	if ws, _ := sig.Raw["window_start"].(string); ws == "" {
		t.Error("window_start should still be present even when no range was found, so the chart knows what period the system looked at")
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL", sig.Direction)
	}
}

func TestInsufficientDataReturnsNeutral(t *testing.T) {
	b := newBuilder().AddFlat(100, 1000).AddFlat(100, 1000)
	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("should return a neutral signal when there aren't enough candles, got %s", sig.Direction)
	}
}

func TestInvalidParamsRejected(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	m := New()
	if _, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"reversal_window": 100, // exceeds the [1,20] upper bound
	}); err == nil {
		t.Error("reversal_window out of range should error")
	}
	if _, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"bogus_param": 1,
	}); err == nil {
		t.Error("an unknown parameter should error")
	}
}

func TestContextCancellationRespected(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := New()
	if _, err := m.Evaluate(ctx, b.Build(), map[string]any{}); err == nil {
		t.Error("a canceled context should error")
	}
}
