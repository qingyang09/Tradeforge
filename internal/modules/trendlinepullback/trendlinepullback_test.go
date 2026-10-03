package trendlinepullback

import (
	"context"
	"testing"
	"time"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func newBuilder() *synth.Builder { return synth.New("BTCUSDT", types.TF1h, base) }

type bar struct{ o, h, l, c float64 }

// bullishBars is the following confirmed-pivot structure (pivot_strength=1,
// so each pivot only needs its immediate left/right neighbor to be strictly
// lower/higher): a swing high at 110 (idx1), a swing low at 90 (idx2), a
// higher swing high at 120 (idx4), a higher swing low at 100 (idx6) -- an
// uptrend confirmed by both a higher high and a higher low. A rally then
// makes a swing high at 125 (idx8), after which price pulls back, bottoming
// at 112 (idx11) -- within 1% of the trendline connecting the two swing lows
// (90@idx2, 100@idx6) extrapolated to idx11 (112.5) -- then bounces to close
// at 115 (idx12), 2.7% above the tested low. Every condition the module
// checks is satisfied by construction; each edge-case test below mutates
// exactly one of these bars to isolate exactly one failing condition.
var bullishBars = []bar{
	{100, 101, 99, 100},  // idx0 filler
	{100, 110, 100, 108}, // idx1 swing high 110
	{108, 109, 90, 95},   // idx2 swing low 90
	{95, 105, 94, 103},   // idx3 filler
	{103, 120, 102, 115}, // idx4 swing high 120 (higher high)
	{115, 116, 101, 103}, // idx5 filler
	{103, 104, 100, 101}, // idx6 swing low 100 (higher low)
	{101, 112, 101, 110}, // idx7 rising leg
	{110, 125, 109, 120}, // idx8 swing high 125 (leg top)
	{120, 121, 116, 118}, // idx9 pullback begins
	{118, 119, 114, 115}, // idx10 pullback continues
	{115, 116, 112, 113}, // idx11 test low 112 (near the 112.5 trendline value)
	{113, 116, 113, 115}, // idx12 current candle, bounced to close 115
}

func buildBars(bars []bar) types.MarketData {
	b := newBuilder()
	for _, x := range bars {
		b.Add(x.o, x.h, x.l, x.c, 1000, 0.5)
	}
	return b.Build()
}

// withBar returns a copy of bars with index idx replaced by mutated, so each
// edge-case test below can isolate exactly one changed condition without
// retyping the whole sequence.
func withBar(bars []bar, idx int, mutated bar) []bar {
	out := make([]bar, len(bars))
	copy(out, bars)
	out[idx] = mutated
	return out
}

// bearishBars is bullishBars' exact mirror image: every OHLC value v is
// replaced by 220-v, which flips highs into lows and vice versa while
// preserving every strict pivot inequality (since x -> 220-x is strictly
// order-reversing) -- so the same structural reasoning applies with "higher"
// and "lower" swapped: a lower swing high at 110 (idx1), a swing low at 130
// (idx2), a lower swing high at 100 (idx4), a lower swing low at 120 (idx6)
// -- a downtrend confirmed by both a lower high and a lower low. A drop then
// makes a swing low at 95 (idx8), after which price rallies, topping at 108
// (idx11) -- within 1% of the trendline connecting the two swing highs
// (130@idx2, 120@idx6) extrapolated to idx11 (107.5) -- then gets rejected
// to close at 105 (idx12), 2.7% below the tested high.
var bearishBars = []bar{
	{120, 121, 119, 120},
	{120, 120, 110, 112},
	{112, 130, 111, 125},
	{125, 126, 115, 117},
	{117, 118, 100, 105},
	{105, 119, 104, 117},
	{117, 120, 116, 119},
	{119, 119, 108, 110},
	{110, 111, 95, 100},
	{100, 104, 99, 102},
	{102, 106, 101, 105},
	{105, 108, 104, 107},
	{107, 107, 104, 105},
}

func evalWithUnitStrength(t *testing.T, md types.MarketData) types.Signal {
	t.Helper()
	sig, err := New().Evaluate(context.Background(), md, map[string]any{"pivot_strength": 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return sig
}

func TestUptrendPullbackHoldingHigherLowProducesLong(t *testing.T) {
	sig := evalWithUnitStrength(t, buildBars(bullishBars))
	if sig.Direction != types.DirectionLong {
		t.Fatalf("Direction = %s, want LONG. Reason: %s, raw=%v", sig.Direction, sig.Reason, sig.Raw)
	}
	if sig.Raw["pattern"] != "uptrend_pullback" {
		t.Errorf("pattern = %v, want uptrend_pullback", sig.Raw["pattern"])
	}
	if sig.Confidence <= 0.5 || sig.Confidence > 1 {
		t.Errorf("confidence %v is outside (0.5,1]", sig.Confidence)
	}
	if sig.Reason.IsZero() {
		t.Error("Reason must not be empty")
	}
	if sig.Timestamp.IsZero() {
		t.Error("signal must carry a timestamp")
	}
}

func TestDowntrendRallyHoldingLowerHighProducesShort(t *testing.T) {
	sig := evalWithUnitStrength(t, buildBars(bearishBars))
	if sig.Direction != types.DirectionShort {
		t.Fatalf("Direction = %s, want SHORT. Reason: %s, raw=%v", sig.Direction, sig.Reason, sig.Raw)
	}
	if sig.Raw["pattern"] != "downtrend_rally" {
		t.Errorf("pattern = %v, want downtrend_rally", sig.Raw["pattern"])
	}
	if sig.Confidence <= 0.5 || sig.Confidence > 1 {
		t.Errorf("confidence %v is outside (0.5,1]", sig.Confidence)
	}
	if sig.Reason.IsZero() {
		t.Error("Reason must not be empty")
	}
}

// TestStructureNotAscendingStaysNeutral lowers idx4's high from 120 to 109
// (below idx1's swing high of 110) and idx5's high to 107 so idx4 still
// confirms as a swing high -- the two swing lows still ascend (90 -> 100),
// but the swing high between them no longer does, so this isn't a confirmed
// uptrend, just two higher lows inside what could be an ordinary range.
func TestStructureNotAscendingStaysNeutral(t *testing.T) {
	bars := withBar(bullishBars, 4, bar{103, 109, 102, 106})
	bars = withBar(bars, 5, bar{106, 107, 101, 103})
	sig := evalWithUnitStrength(t, buildBars(bars))
	if sig.Direction != types.DirectionNeutral {
		t.Fatalf("Direction = %s, want NEUTRAL. raw=%v", sig.Direction, sig.Raw)
	}
	if got := sig.Raw["bullish_reason"]; got != "modules.trendline_pullback.reason.structure_not_uptrend" {
		t.Errorf("bullish_reason = %v, want structure_not_uptrend", got)
	}
}

// TestPullbackBrokeStructureStaysNeutral lowers the test candle's (idx11)
// low from 112 to 95, below the higher low at idx6 (100) -- the pullback has
// broken the uptrend structure instead of holding it, so this must not be
// read as "testing the trendline".
func TestPullbackBrokeStructureStaysNeutral(t *testing.T) {
	bars := withBar(bullishBars, 11, bar{97, 99, 95, 96})
	sig := evalWithUnitStrength(t, buildBars(bars))
	if sig.Direction != types.DirectionNeutral {
		t.Fatalf("Direction = %s, want NEUTRAL. raw=%v", sig.Direction, sig.Raw)
	}
	if got := sig.Raw["bullish_reason"]; got != "modules.trendline_pullback.reason.pullback_broke_structure" {
		t.Errorf("bullish_reason = %v, want pullback_broke_structure", got)
	}
}

// TestPullbackNotNearTrendlineStaysNeutral lowers the test candle's (idx11)
// low from 112 to 105 -- still the window minimum and still above the
// higher low at 100, but 6.7% away from the trendline's value at that
// candle (112.5), well outside the default 1% tolerance, so this pullback
// never actually reached the trendline.
func TestPullbackNotNearTrendlineStaysNeutral(t *testing.T) {
	bars := withBar(bullishBars, 11, bar{106, 108, 105, 106})
	sig := evalWithUnitStrength(t, buildBars(bars))
	if sig.Direction != types.DirectionNeutral {
		t.Fatalf("Direction = %s, want NEUTRAL. raw=%v", sig.Direction, sig.Raw)
	}
	if got := sig.Raw["bullish_reason"]; got != "modules.trendline_pullback.reason.pullback_not_near_trendline" {
		t.Errorf("bullish_reason = %v, want pullback_not_near_trendline", got)
	}
}

// TestBounceNotConfirmedStaysNeutral keeps the test low at idx11 (112,
// correctly near the trendline) but changes the current candle (idx12) so
// its close has barely moved off that low -- 0.18%, under the default 0.3%
// bounce_confirm margin -- so the test hasn't been confirmed as defended yet.
func TestBounceNotConfirmedStaysNeutral(t *testing.T) {
	bars := withBar(bullishBars, 12, bar{112, 113, 112, 112.2})
	sig := evalWithUnitStrength(t, buildBars(bars))
	if sig.Direction != types.DirectionNeutral {
		t.Fatalf("Direction = %s, want NEUTRAL. raw=%v", sig.Direction, sig.Raw)
	}
	if got := sig.Raw["bullish_reason"]; got != "modules.trendline_pullback.reason.bounce_not_confirmed" {
		t.Errorf("bullish_reason = %v, want bounce_not_confirmed", got)
	}
}

// TestNoRallyAfterHigherLowStaysNeutral flattens idx7-idx11's highs into a
// strictly increasing run, so none of them ever gets confirmed as a swing
// high (confirmation requires the right neighbor's high to be lower) --
// there is no completed leg after the higher low at idx6 yet, so there's
// nothing yet to call a "pullback" relative to.
func TestNoRallyAfterHigherLowStaysNeutral(t *testing.T) {
	bars := withBar(bullishBars, 7, bar{101, 105, 101, 103})
	bars = withBar(bars, 8, bar{103, 106, 102, 104})
	bars = withBar(bars, 9, bar{104, 107, 103, 105})
	bars = withBar(bars, 10, bar{105, 108, 104, 106})
	bars = withBar(bars, 11, bar{106, 109, 105, 107})
	sig := evalWithUnitStrength(t, buildBars(bars))
	if sig.Direction != types.DirectionNeutral {
		t.Fatalf("Direction = %s, want NEUTRAL. raw=%v", sig.Direction, sig.Raw)
	}
	if got := sig.Raw["bullish_reason"]; got != "modules.trendline_pullback.reason.no_rally_after_higher_low" {
		t.Errorf("bullish_reason = %v, want no_rally_after_higher_low", got)
	}
}

// TestInsufficientCandlesReturnsNeutral checks the floor the module applies
// before even attempting pivot detection.
func TestInsufficientCandlesReturnsNeutral(t *testing.T) {
	md := buildBars(bullishBars[:5])
	sig := evalWithUnitStrength(t, md)
	if sig.Direction != types.DirectionNeutral {
		t.Fatalf("Direction = %s, want NEUTRAL. Reason: %s", sig.Direction, sig.Reason)
	}
	if sig.Reason.Key != "modules.trendline_pullback.reason.insufficient_candles" {
		t.Errorf("Reason.Key = %q, want insufficient_candles", sig.Reason.Key)
	}
}

func TestEmptyMarketDataReturnsNeutral(t *testing.T) {
	sig, err := New().Evaluate(context.Background(), types.MarketData{Symbol: "BTCUSDT"}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Fatalf("Direction = %s, want NEUTRAL", sig.Direction)
	}
}

func TestInvalidParamsRejected(t *testing.T) {
	cases := map[string]map[string]any{
		"tolerance_out_of_range": {"trendline_tolerance": 1.0},
		"negative_bounce":        {"bounce_confirm": -0.01},
		"pivot_strength_zero":    {"pivot_strength": 0},
		"unknown_parameter":      {"not_a_real_param": 1},
	}
	md := buildBars(bullishBars)
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New().Evaluate(context.Background(), md, params); err == nil {
				t.Error("expected an error for invalid params, got nil")
			}
		})
	}
}

func TestContextCancellationRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().Evaluate(ctx, buildBars(bullishBars), nil)
	if err == nil {
		t.Error("expected an error for an already-cancelled context")
	}
}
