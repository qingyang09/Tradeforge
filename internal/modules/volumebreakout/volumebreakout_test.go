package volumebreakout

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// flat builds n baseline candles of equal size, with volume fixed at `volume`.
func flat(n int, price, volume float64) *synth.Builder {
	b := synth.New("BTCUSDT", types.TF1h, base)
	for i := 0; i < n; i++ {
		b.AddBar(price, price, volume, 0.5)
	}
	return b
}

func TestBullishBreakoutOnVolumeSurge(t *testing.T) {
	b := flat(20, 100, 1000)
	b.AddBar(100, 105, 3000, 0.7) // 3x volume and bullish close

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 20, "multiplier": 2.0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Fatalf("Direction = %s, want LONG (reason: %s)", sig.Direction, sig.Reason)
	}
	if ratio := sig.Raw["ratio"].(float64); ratio < 2.99 || ratio > 3.01 {
		t.Errorf("ratio = %v, want approximately 3.0", ratio)
	}
	if sig.Confidence < 0.5 || sig.Confidence > 0.95 {
		t.Errorf("confidence %v is outside [0.5, 0.95]", sig.Confidence)
	}
}

func TestBearishBreakoutOnVolumeSurge(t *testing.T) {
	b := flat(20, 100, 1000)
	b.AddBar(100, 95, 5000, 0.3) // 5x volume and bearish close

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"window": 20})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionShort {
		t.Fatalf("Direction = %s, want SHORT (reason: %s)", sig.Direction, sig.Reason)
	}
}

// A volume ratio below the threshold must be neutral no matter how sharply price moved.
func TestBelowThresholdIsNeutral(t *testing.T) {
	b := flat(20, 100, 1000)
	b.AddBar(100, 120, 1500, 0.9) // only 1.5x volume

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"multiplier": 2.0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL", sig.Direction)
	}
	if sig.Confidence != 0 {
		t.Errorf("Confidence = %v, want 0", sig.Confidence)
	}
}

// Confidence must be monotonically non-decreasing with the ratio; otherwise a
// counterintuitive "bigger surge, weaker signal" behavior would mislead
// downstream weighting.
func TestConfidenceIncreasesWithRatio(t *testing.T) {
	prev := 0.0
	for _, vol := range []float64{2000, 3000, 5000, 10000, 50000} {
		b := flat(20, 100, 1000)
		b.AddBar(100, 105, vol, 0.7)
		sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"multiplier": 2.0})
		if err != nil {
			t.Fatal(err)
		}
		if sig.Confidence < prev {
			t.Errorf("at volume %v, confidence %v is below the previous tier's %v", vol, sig.Confidence, prev)
		}
		if sig.Confidence > 0.95 {
			t.Errorf("confidence %v exceeds the 0.95 cap", sig.Confidence)
		}
		prev = sig.Confidence
	}
}

// When the average volume is zero, any ratio would be infinite; this must be
// treated as uncomputable rather than triggering a signal.
func TestZeroAverageVolumeIsNeutral(t *testing.T) {
	b := flat(20, 100, 0)
	b.AddBar(100, 105, 5000, 0.7)

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"window": 20})
	if err != nil {
		t.Fatalf("zero average volume is a data issue and should return neutral rather than an error, got: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL", sig.Direction)
	}
}

func TestInsufficientDataReturnsNeutral(t *testing.T) {
	b := flat(5, 100, 1000)
	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"window": 20})
	if err != nil {
		t.Fatalf("insufficient data should not error, got: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL", sig.Direction)
	}
}

// A doji-style volume surge has an unclear direction; min_body_ratio should filter it out.
func TestDojiFilteredByBodyRatio(t *testing.T) {
	b := flat(20, 100, 1000)
	// Long upper and lower wicks, tiny body: open 100, close 100.05, full range 10.
	b.Add(100, 105, 95, 100.05, 5000, 0.5)

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 20, "multiplier": 2.0, "min_body_ratio": 0.3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL (the body-ratio filter should apply)", sig.Direction)
	}

	// With the filter turned off, the same candle should yield a direction.
	sig2, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 20, "multiplier": 2.0, "min_body_ratio": 0.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sig2.Direction != types.DirectionLong {
		t.Errorf("with the filter off, Direction = %s, want LONG", sig2.Direction)
	}
}

func TestTakerDirectionSource(t *testing.T) {
	// Bullish close but taker sell volume dominates: the two direction sources
	// should give opposite conclusions, proving the parameter actually takes effect.
	b := flat(20, 100, 1000)
	b.AddBar(100, 105, 4000, 0.2)

	byCandle, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 20, "direction_source": SourceCandle,
	})
	if err != nil {
		t.Fatal(err)
	}
	byTaker, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 20, "direction_source": SourceTaker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if byCandle.Direction != types.DirectionLong {
		t.Errorf("direction by candle = %s, want LONG", byCandle.Direction)
	}
	if byTaker.Direction != types.DirectionShort {
		t.Errorf("direction by taker volume = %s, want SHORT", byTaker.Direction)
	}
}

// When the data source doesn't supply taker buy volume, a net difference that
// is always negative would fabricate a bogus short signal — this must be detected.
func TestTakerSourceWithMissingDataIsNeutral(t *testing.T) {
	b := flat(20, 100, 1000)
	b.AddBar(100, 105, 4000, 0.0) // TakerBuyVolume is 0, simulating a missing field

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 20, "direction_source": SourceTaker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL (must not fabricate a short with no taker buy data)", sig.Direction)
	}
}

func TestInvalidParamsRejected(t *testing.T) {
	b := flat(30, 100, 1000)
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"multiplier below minimum", map[string]any{"multiplier": 0.5}},
		{"window out of range", map[string]any{"window": 99999}},
		{"window not an integer", map[string]any{"window": 20.5}},
		{"direction source outside the enum", map[string]any{"direction_source": "orderbook"}},
		{"unknown parameter", map[string]any{"threshold": 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New().Evaluate(context.Background(), b.Build(), tc.params); err == nil {
				t.Fatalf("expected %v to be rejected, but it passed", tc.params)
			}
		})
	}
}

func TestContextCancellationRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := flat(30, 100, 1000)
	if _, err := New().Evaluate(ctx, b.Build(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

// The average must exclude the current candle: otherwise the smaller window
// is, the more the current candle dilutes its own ratio.
func TestAverageExcludesCurrentCandle(t *testing.T) {
	b := flat(10, 100, 1000)
	b.AddBar(100, 105, 10000, 0.7)

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"window": 10})
	if err != nil {
		t.Fatal(err)
	}
	// Including the current candle, the average is (10*1000+10000)/11 ≈ 1818,
	// giving a ratio of about 5.5; excluding it, the average is 1000 and the
	// ratio is exactly 10.
	if ratio := sig.Raw["ratio"].(float64); ratio < 9.99 || ratio > 10.01 {
		t.Errorf("ratio = %v, want 10.0 (indicates the average folded in the current candle)", ratio)
	}
}
