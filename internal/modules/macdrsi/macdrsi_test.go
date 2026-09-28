package macdrsi

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func d(f float64) decimal.Decimal { return decimal.NewFromFloat(f) }

// ---- Indicator correctness: verify the numbers directly, bypassing Evaluate, to avoid "looks like it works". ----

// emaSeries seeds with a simple average, so the first valid value must equal
// exactly the mean of the first `period` values, and roll forward using the
// standard EMA recurrence after that.
func TestEMASeriesKnownValues(t *testing.T) {
	values := []decimal.Decimal{d(1), d(2), d(3), d(4), d(5), d(6)}
	series, from := emaSeries(values, 3)
	if from != 2 {
		t.Fatalf("validFrom = %d, want 2", from)
	}
	// seed = (1+2+3)/3 = 2
	if !series[2].Equal(d(2)) {
		t.Errorf("series[2] = %s, want 2", series[2])
	}
	// k = 2/(3+1) = 0.5; series[3] = 4*0.5 + 2*0.5 = 3
	if !series[3].Equal(d(3)) {
		t.Errorf("series[3] = %s, want 3", series[3])
	}
	// series[4] = 5*0.5 + 3*0.5 = 4
	if !series[4].Equal(d(4)) {
		t.Errorf("series[4] = %s, want 4", series[4])
	}
}

func TestEMASeriesInsufficientData(t *testing.T) {
	values := []decimal.Decimal{d(1), d(2)}
	series, from := emaSeries(values, 5)
	if from != len(values) {
		t.Fatalf("validFrom = %d, want %d (entire series unusable when data is insufficient)", from, len(values))
	}
	if len(series) != len(values) {
		t.Fatalf("series length = %d, want it to equal len(values)", len(series))
	}
}

// With a sustained rally (every candle a new high), average loss is always zero, so RSI must be 100, not a divide-by-zero crash or misjudgment.
func TestRSISeriesAllGainsIsHundred(t *testing.T) {
	closes := make([]decimal.Decimal, 20)
	for i := range closes {
		closes[i] = d(100 + float64(i))
	}
	rsi, from := rsiSeries(closes, 14)
	if from != 14 {
		t.Fatalf("validFrom = %d, want 14", from)
	}
	for i := from; i < len(rsi); i++ {
		if rsi[i] != 100 {
			t.Errorf("rsi[%d] = %v, want 100 (sustained rally)", i, rsi[i])
		}
	}
}

// Symmetrically, a sustained decline must give 0.
func TestRSISeriesAllLossesIsZero(t *testing.T) {
	closes := make([]decimal.Decimal, 20)
	for i := range closes {
		closes[i] = d(100 - float64(i))
	}
	rsi, _ := rsiSeries(closes, 14)
	for i := 14; i < len(rsi); i++ {
		if rsi[i] != 0 {
			t.Errorf("rsi[%d] = %v, want 0 (sustained decline)", i, rsi[i])
		}
	}
}

// Price completely flat: no gains or losses, RSI should be a neutral 50, not error out on 0/0 or be misjudged as extreme.
func TestRSISeriesFlatIsFifty(t *testing.T) {
	closes := make([]decimal.Decimal, 20)
	for i := range closes {
		closes[i] = d(100)
	}
	rsi, _ := rsiSeries(closes, 14)
	if rsi[14] != 50 {
		t.Errorf("rsi[14] = %v, want 50 (no price movement)", rsi[14])
	}
}

// Alternating gains and losses of equal size: within the seed window (the
// first `period` candles), gains and losses are evenly split, RS=1, so the
// first valid value must be exactly 50.
//
// Only the seed point is asserted: Wilder smoothing is an asymmetric
// recurrence (each step blends in only one new gain/loss, unlike the
// symmetric equal-weighted average in the seed window), so even if the input
// keeps strictly alternating after the seed point, RSI will keep oscillating
// slightly around 50 rather than staying pinned there — this is correct
// behavior of the algorithm itself, not an error to fix.
func TestRSISeriesBalancedIsFiftyAtSeed(t *testing.T) {
	closes := make([]decimal.Decimal, 30)
	price := 100.0
	for i := range closes {
		closes[i] = d(price)
		if i%2 == 0 {
			price += 1
		} else {
			price -= 1
		}
	}
	rsi, from := rsiSeries(closes, 14)
	if from != 14 {
		t.Fatalf("validFrom = %d, want 14", from)
	}
	if rsi[from] < 49.99 || rsi[from] > 50.01 {
		t.Errorf("rsi[%d] = %v, want exactly 50 (gains and losses cancel out in the seed window)", from, rsi[from])
	}
}

// ---- decideDirection: the core of the confluence filter logic, verified directly with a truth table, no market data construction needed. ----

func TestDecideDirection(t *testing.T) {
	cases := []struct {
		name                                               string
		mode                                               string
		bullishCross, bearishCross, bullishRev, bearishRev bool
		rsiNow, oversold, overbought                       float64
		wantDir                                            types.Direction
		wantTriggered                                      bool
	}{
		{"macd_cross bullish cross always triggers, ignores RSI", ModeMACDCross,
			true, false, false, false, 90, 30, 70, types.DirectionLong, true},
		{"macd_cross bearish cross always triggers, ignores RSI", ModeMACDCross,
			false, true, false, false, 5, 30, 70, types.DirectionShort, true},
		{"macd_cross with no cross does not trigger", ModeMACDCross,
			false, false, false, false, 50, 30, 70, types.DirectionNeutral, false},
		{"rsi_reversal only looks at reversal, ignores MACD", ModeRSIReversal,
			false, false, true, false, 31, 30, 70, types.DirectionLong, true},
		{"rsi_reversal with no reversal does not trigger", ModeRSIReversal,
			true, true, false, false, 50, 30, 70, types.DirectionNeutral, false},
		{"confluence bullish cross with RSI not overbought -> triggers long", ModeConfluence,
			true, false, false, false, 60, 30, 70, types.DirectionLong, true},
		{"confluence bullish cross but RSI already overbought -> filtered out", ModeConfluence,
			true, false, false, false, 75, 30, 70, types.DirectionNeutral, false},
		{"confluence bearish cross with RSI not oversold -> triggers short", ModeConfluence,
			false, true, false, false, 40, 30, 70, types.DirectionShort, true},
		{"confluence bearish cross but RSI already oversold -> filtered out", ModeConfluence,
			false, true, false, false, 25, 30, 70, types.DirectionNeutral, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, triggered := decideDirection(tc.mode,
				tc.bullishCross, tc.bearishCross, tc.bullishRev, tc.bearishRev,
				tc.rsiNow, tc.oversold, tc.overbought)
			if triggered != tc.wantTriggered {
				t.Fatalf("triggered = %v, want %v", triggered, tc.wantTriggered)
			}
			if dir != tc.wantDir {
				t.Errorf("dir = %s, want %s", dir, tc.wantDir)
			}
		})
	}
}

// ---- End-to-end: replay Evaluate incrementally over synthetic market data, checking direction sanity and edge-case handling. ----

// driftWalk appends n candles using a drifting pseudo-random walk: each
// step's price change = drift + noise.
//
// A pure linear trend (synth.Builder.Trend) makes the MACD line converge to a
// constant within a few candles, with its own signal line catching up almost
// instantly — once the gap drops to floating-point noise level, even if price
// later reverses, the two lines just shift together without a genuine
// one-time cross. Adding noise makes indicator behavior closer to real market
// data, producing meaningful MACD crosses / RSI reversals to test against.
// The seed is fixed so results are reproducible.
func driftWalk(b *synth.Builder, n int, start, drift, noise, volume, takerBuyRatio float64, seed int64) (*synth.Builder, float64) {
	rng := rand.New(rand.NewSource(seed))
	price := start
	for i := 0; i < n; i++ {
		next := price + drift + (rng.Float64()-0.5)*2*noise
		b.AddBar(price, next, volume, takerBuyRatio)
		price = next
	}
	return b, price
}

// replay calls Evaluate with a window that expands one candle at a time
// starting from minCandles, collecting all non-neutral signals — simulating
// how backtest-runner replays candle by candle.
func replay(t *testing.T, full types.MarketData, params map[string]any) []types.Signal {
	t.Helper()
	m := New()
	var out []types.Signal
	for k := 16; k <= len(full.Candles); k++ {
		md := types.MarketData{Symbol: full.Symbol, Timeframe: full.Timeframe, Candles: full.Candles[:k]}
		sig, err := m.Evaluate(context.Background(), md, params)
		if err != nil {
			t.Fatalf("k=%d: unexpected error: %v", k, err)
		}
		if sig.Confidence != 0 && (sig.Confidence < 0.5 || sig.Confidence > 0.95) {
			t.Errorf("k=%d: confidence %v is outside [0.5, 0.95]", k, sig.Confidence)
		}
		if sig.Direction != types.DirectionNeutral {
			out = append(out, sig)
		}
	}
	return out
}

// In a V-shaped reversal (decline then rally), macd_cross mode should capture at least one bullish cross with direction long.
func TestMACDCrossCapturesBullishReversal(t *testing.T) {
	b := synth.New("BTCUSDT", types.TF1h, base)
	b, low := driftWalk(b, 80, 200, -1.2, 1.0, 1000, 0.4, 1)
	driftWalk(b, 80, low, 1.2, 1.0, 1000, 0.6, 2)
	full := b.Build()

	sigs := replay(t, full, map[string]any{"mode": ModeMACDCross})
	found := false
	for _, s := range sigs {
		if s.Raw["event"] == eventBullishCross {
			found = true
			if s.Direction != types.DirectionLong {
				t.Errorf("bullish_cross Direction = %s, want LONG", s.Direction)
			}
		}
	}
	if !found {
		t.Fatal("expected at least one MACD bullish cross in a V-shaped reversal")
	}
}

// In an inverted-V reversal (rally then decline), macd_cross mode should capture at least one bearish cross with direction short.
func TestMACDCrossCapturesBearishReversal(t *testing.T) {
	b := synth.New("BTCUSDT", types.TF1h, base)
	b, high := driftWalk(b, 80, 100, 1.2, 1.0, 1000, 0.6, 3)
	driftWalk(b, 80, high, -1.2, 1.0, 1000, 0.4, 4)
	full := b.Build()

	sigs := replay(t, full, map[string]any{"mode": ModeMACDCross})
	found := false
	for _, s := range sigs {
		if s.Raw["event"] == eventBearishCross {
			found = true
			if s.Direction != types.DirectionShort {
				t.Errorf("bearish_cross Direction = %s, want SHORT", s.Direction)
			}
		}
	}
	if !found {
		t.Fatal("expected at least one MACD bearish cross in an inverted-V reversal")
	}
}

// A sharp drop followed by a rebound strong enough to push RSI into oversold and back should be captured by rsi_reversal mode as a bullish reversal.
func TestRSIReversalCapturesBullishReversal(t *testing.T) {
	b := synth.New("BTCUSDT", types.TF1h, base)
	b, low := driftWalk(b, 30, 100, -2.0, 0.8, 1000, 0.3, 5)
	driftWalk(b, 30, low, 2.0, 0.8, 1000, 0.7, 6)
	full := b.Build()

	sigs := replay(t, full, map[string]any{"mode": ModeRSIReversal})
	found := false
	for _, s := range sigs {
		if s.Raw["event"] == eventBullishReversal {
			found = true
			if s.Direction != types.DirectionLong {
				t.Errorf("bullish_reversal Direction = %s, want LONG", s.Direction)
			}
		}
	}
	if !found {
		t.Fatal("expected at least one RSI oversold reversal in a sharp-drop-and-rebound market")
	}
}

func TestConfluenceModeOnlyEmitsAgreeingSignals(t *testing.T) {
	b := synth.New("BTCUSDT", types.TF1h, base)
	b, low := driftWalk(b, 80, 200, -1.2, 1.0, 1000, 0.4, 1)
	driftWalk(b, 80, low, 1.2, 1.0, 1000, 0.6, 2)
	full := b.Build()

	sigs := replay(t, full, map[string]any{"mode": ModeConfluence})
	if len(sigs) == 0 {
		t.Fatal("confluence mode should give at least one signal in an obvious V-shaped reversal")
	}
	for _, s := range sigs {
		rsiNow := s.Raw["rsi"].(float64)
		switch s.Direction {
		case types.DirectionLong:
			if rsiNow >= 70 {
				t.Errorf("confluence long signal RSI=%.1f, should not be in the overbought zone", rsiNow)
			}
		case types.DirectionShort:
			if rsiNow <= 30 {
				t.Errorf("confluence short signal RSI=%.1f, should not be in the oversold zone", rsiNow)
			}
		}
	}
}

func TestInsufficientDataReturnsNeutral(t *testing.T) {
	b := synth.New("BTCUSDT", types.TF1h, base)
	for i := 0; i < 10; i++ {
		b.AddFlat(100, 1000)
	}
	sig, err := New().Evaluate(context.Background(), b.Build(), nil)
	if err != nil {
		t.Fatalf("insufficient data should not error, got: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL", sig.Direction)
	}
}

func TestInvalidParamsRejected(t *testing.T) {
	b := synth.New("BTCUSDT", types.TF1h, base)
	b.Trend(60, 100, 150, 1000, 0.5)
	full := b.Build()

	cases := []struct {
		name   string
		params map[string]any
	}{
		{"slow period not greater than fast period", map[string]any{"fast_period": 26, "slow_period": 12}},
		{"oversold threshold not below overbought threshold", map[string]any{"rsi_oversold": 80, "rsi_overbought": 70}},
		{"fast period out of range", map[string]any{"fast_period": 999}},
		{"period not an integer", map[string]any{"rsi_period": 14.5}},
		{"mode outside the enum", map[string]any{"mode": "fibonacci"}},
		{"unknown parameter", map[string]any{"threshold": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New().Evaluate(context.Background(), full, tc.params); err == nil {
				t.Fatalf("expected %v to be rejected, but it passed", tc.params)
			}
		})
	}
}

func TestContextCancellationRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := synth.New("BTCUSDT", types.TF1h, base)
	b.Trend(60, 100, 150, 1000, 0.5)
	if _, err := New().Evaluate(ctx, b.Build(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}
