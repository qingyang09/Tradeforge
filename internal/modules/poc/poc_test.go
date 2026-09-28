package poc

import (
	"context"
	"testing"
	"time"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func newBuilder() *synth.Builder { return synth.New("BTCUSDT", types.TF1h, base) }

// Builds 5 candles with prices spread across 100~108, where the candle at
// price 104 has volume far exceeding the sum of all the others — the
// computed POC should land near the bucket containing 104, not elsewhere.
func TestPOCLandsOnDominantVolumeBucket(t *testing.T) {
	b := newBuilder()
	b.Add(100, 100, 100, 100, 100, 0.5)
	b.Add(102, 102, 102, 102, 100, 0.5)
	b.Add(104, 104, 104, 104, 100000, 0.5) // volume far exceeds the sum of the others
	b.Add(106, 106, 106, 106, 100, 0.5)
	b.Add(108, 108, 108, 108, 100, 0.5)
	// Add one more candle with a close price right next to 104, to assert the proximity check.
	b.Add(104, 104, 104, 104.1, 100, 0.5)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"lookback": 100, "bucket_count": 8, "proximity": 0.05,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Raw["is_approximate"] != true {
		t.Error("POC must be tagged is_approximate=true, must not pretend to be an exact value")
	}
	pocPriceStr, _ := sig.Raw["poc_price"].(string)
	if pocPriceStr == "" {
		t.Fatal("Raw should contain poc_price")
	}
	// bucket width = (108-100)/8 = 1, POC should land near the bucket containing 104 ([104,105)).
	if pocPriceStr != "104.5" {
		t.Errorf("poc_price = %s, want it to land in the bucket containing 104 (104.5, the bucket midpoint)", pocPriceStr)
	}
}

// window_start must reflect the lookback window's starting point, so the chart can mark "this is the history the system is looking at".
func TestPOCWindowStartReflectsLookback(t *testing.T) {
	b := newBuilder()
	b.Add(100, 100, 100, 100, 100, 0.5)
	b.Add(102, 102, 102, 102, 100, 0.5)
	b.Add(104, 104, 104, 104, 100000, 0.5)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"lookback": 100, "bucket_count": 8, "proximity": 0.05,
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
		t.Fatalf("window_start should be a valid RFC3339 time: %v", err)
	}
	// Only 3 candles, all within lookback=100, so the window start is the first candle's open time.
	if !got.Equal(base) {
		t.Errorf("window_start = %s, want it to equal the first candle's open time %s", got, base)
	}
}

func TestPOCDirectionByProximity(t *testing.T) {
	b := newBuilder()
	b.Add(100, 100, 100, 100, 100, 0.5)
	b.Add(104, 104, 104, 104, 100000, 0.5)
	b.Add(108, 108, 108, 108, 100, 0.5)
	// Current close is well above the POC and outside the proximity range -> should be neutral.
	b.Add(120, 120, 120, 120, 100, 0.5)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"bucket_count": 5, "proximity": 0.01,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("should be neutral when far from the POC, got %s (raw=%v)", sig.Direction, sig.Raw)
	}
}

func TestPOCApproachingFromBelowIsLong(t *testing.T) {
	b := newBuilder()
	b.Add(100, 100, 100, 100, 100000, 0.5) // POC near 100
	b.Add(110, 110, 110, 110, 100, 0.5)
	b.Add(99.9, 99.9, 99.9, 99.9, 100, 0.5) // current close approaches the POC from below

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"bucket_count": 5, "proximity": 0.05,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Errorf("approaching the POC from below should be LONG, got %s (raw=%v)", sig.Direction, sig.Raw)
	}
}

func TestPOCNoWithinLookbackPriceMovementReturnsNeutral(t *testing.T) {
	b := newBuilder()
	b.Add(100, 100, 100, 100, 1000, 0.5) // single candle, high=low, no price movement
	b.Add(100, 100, 100, 100, 1000, 0.5)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("should return a neutral signal when price hasn't moved, got %s", sig.Direction)
	}
	if ws, _ := sig.Raw["window_start"].(string); ws == "" {
		t.Error("window_start should still be present even when the POC can't be computed, so the chart knows what period the system looked at")
	}
}

func TestPOCInsufficientDataReturnsNeutral(t *testing.T) {
	b := newBuilder().Add(100, 101, 99, 100, 1000, 0.5)
	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("should return a neutral signal when there aren't enough candles, got %s", sig.Direction)
	}
}

func TestPOCInvalidParamsRejected(t *testing.T) {
	b := newBuilder().Add(100, 101, 99, 100, 1000, 0.5).Add(100, 101, 99, 100, 1000, 0.5)
	m := New()
	if _, err := m.Evaluate(context.Background(), b.Build(), map[string]any{"bucket_count": 1000}); err == nil {
		t.Error("bucket_count out of range should error")
	}
	if _, err := m.Evaluate(context.Background(), b.Build(), map[string]any{"unknown": 1}); err == nil {
		t.Error("an unknown parameter should error")
	}
}

func TestPOCContextCancellationRespected(t *testing.T) {
	b := newBuilder().Add(100, 101, 99, 100, 1000, 0.5).Add(100, 101, 99, 100, 1000, 0.5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := New()
	if _, err := m.Evaluate(ctx, b.Build(), map[string]any{}); err == nil {
		t.Error("a canceled context should error")
	}
}
