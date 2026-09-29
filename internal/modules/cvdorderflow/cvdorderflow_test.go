package cvdorderflow

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func builder() *synth.Builder { return synth.New("BTCUSDT", types.TF1h, base) }

// Sustained aggressive buying dominance -> bullish imbalance.
func TestBullishImbalance(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 100.1, 1000, 0.8) // 80% taker buy, 20% taker sell, net +60%
	}

	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "imbalance_threshold": 0.25, "detect": DetectImbalance,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Fatalf("Direction = %s, want LONG (reason: %s)", sig.Direction, sig.Reason)
	}
	if sig.Raw["event"] != eventImbalance {
		t.Errorf("event = %v, want %s", sig.Raw["event"], eventImbalance)
	}
	if imb := sig.Raw["imbalance"].(float64); imb < 0.59 || imb > 0.61 {
		t.Errorf("imbalance = %v, want approximately 0.6", imb)
	}
}

func TestBearishImbalance(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 99.9, 1000, 0.2)
	}
	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "detect": DetectImbalance,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionShort {
		t.Fatalf("Direction = %s, want SHORT (reason: %s)", sig.Direction, sig.Reason)
	}
}

// Roughly balanced buying and selling should not trigger.
func TestBalancedFlowIsNeutral(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 100, 1000, 0.5)
	}
	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{"window": 50})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL (reason: %s)", sig.Direction, sig.Reason)
	}
}

// Price rising but taker buy volume net outflow -> bearish (top) divergence, direction follows CVD to short.
func TestBearishDivergence(t *testing.T) {
	b := builder().Trend(60, 100, 120, 1000, 0.35) // price rising, only 35% taker buy

	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "detect": DetectDivergence,
		"divergence_threshold": 0.15, "min_price_move": 0.005,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionShort {
		t.Fatalf("Direction = %s, want SHORT (reason: %s, raw=%v)", sig.Direction, sig.Reason, sig.Raw)
	}
	if sig.Raw["event"] != eventDivergence {
		t.Errorf("event = %v, want %s", sig.Raw["event"], eventDivergence)
	}
}

// Price falling but taker buy volume net inflow -> bullish (bottom) divergence, direction taken as long.
func TestBullishDivergence(t *testing.T) {
	b := builder().Trend(60, 120, 100, 1000, 0.65)

	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "detect": DetectDivergence,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Fatalf("Direction = %s, want LONG (reason: %s, raw=%v)", sig.Direction, sig.Reason, sig.Raw)
	}
}

// Price and order flow moving the same direction is not a divergence; detecting divergence only should give neutral.
func TestSameDirectionIsNotDivergence(t *testing.T) {
	b := builder().Trend(60, 100, 120, 1000, 0.8) // price rising + strong buy flow, same direction

	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "detect": DetectDivergence,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL (same direction isn't a divergence)", sig.Direction)
	}
}

// A "divergence" when price barely moved is noise; min_price_move should suppress it.
func TestFlatPriceSuppressesDivergence(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 100.001, 1000, 0.2) // price barely moves, very weak buy flow
	}
	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "detect": DetectDivergence, "min_price_move": 0.01,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sig.Raw["event"] == eventDivergence {
		t.Errorf("reported divergence despite price barely moving: %s", sig.Reason)
	}
}

func TestInsufficientDataReturnsNeutral(t *testing.T) {
	b := builder()
	for i := 0; i < 10; i++ {
		b.AddBar(100, 101, 1000, 0.8)
	}
	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{"window": 50})
	if err != nil {
		t.Fatalf("insufficient data should not error, got: %v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("Direction = %s, want NEUTRAL", sig.Direction)
	}
}

// Must error when market data has no taker buy volume field at all:
// computing anyway would leave the net difference always negative, fabricating a string of bearish signals out of nothing.
func TestMissingTakerDataIsAnError(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 101, 1000, 0.0)
	}
	_, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{"window": 50})
	if err == nil {
		t.Fatal("expected an error when taker buy volume is missing")
	}
	var dsErr *DataSourceError
	if !errors.As(err, &dsErr) {
		t.Fatalf("expected a *DataSourceError, got: %v (%T)", err, err)
	}
	const wantKey = "modules.cvd_orderflow.error.missing_taker_volume"
	if dsErr.Reason().Key != wantKey {
		t.Errorf("Reason().Key = %q, want %q", dsErr.Reason().Key, wantKey)
	}
}

// The placeholder data source lets the pipeline run without real order flow, but must be explicitly labeled in the signal.
func TestSyntheticProviderIsLabelled(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 101, 1000, 0.0)
	}
	m := New(SyntheticFlowProvider{})
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{"window": 50})
	if err != nil {
		t.Fatalf("should not error with the placeholder data source, got: %v", err)
	}
	if sig.Raw["is_synthetic"] != true {
		t.Error("raw.is_synthetic must be true when using the placeholder data source, otherwise downstream can't refuse to let it go live")
	}
	if sig.Raw["provider"] != "synthetic_from_candles" {
		t.Errorf("raw.provider = %v, expected it to be labeled as the placeholder data source", sig.Raw["provider"])
	}
}

// A length mismatch from the data source is a failure; it must not be silently computed against the shorter length.
func TestProviderLengthMismatchIsAnError(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 101, 1000, 0.6)
	}
	m := New(shortProvider{})
	if _, err := m.Evaluate(context.Background(), b.Build(), map[string]any{"window": 50}); err == nil {
		t.Fatal("expected an error on a data source length mismatch")
	}
}

type shortProvider struct{}

func (shortProvider) Name() string { return "short" }
func (shortProvider) Deltas(context.Context, types.MarketData) ([]Delta, error) {
	return []Delta{{}}, nil
}

// A data source error must propagate, not be swallowed into a neutral signal — that would mask a real failure.
func TestProviderErrorPropagates(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 101, 1000, 0.6)
	}
	m := New(failingProvider{})
	_, err := m.Evaluate(context.Background(), b.Build(), map[string]any{"window": 50})
	if !errors.Is(err, errProvider) {
		t.Fatalf("expected the data source error to propagate, got: %v", err)
	}
}

var errProvider = errors.New("data source unavailable")

type failingProvider struct{}

func (failingProvider) Name() string { return "failing" }
func (failingProvider) Deltas(context.Context, types.MarketData) ([]Delta, error) {
	return nil, errProvider
}

func TestInvalidParamsRejected(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 101, 1000, 0.6)
	}
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"imbalance threshold above 1", map[string]any{"imbalance_threshold": 1.5}},
		{"window below minimum", map[string]any{"window": 2}},
		{"invalid detect mode", map[string]any{"detect": "everything"}},
		{"unknown parameter", map[string]any{"lookback": 50}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewDefault().Evaluate(context.Background(), b.Build(), tc.params); err == nil {
				t.Fatalf("expected %v to be rejected, but it passed", tc.params)
			}
		})
	}
}

func TestContextCancellationRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 101, 1000, 0.6)
	}
	if _, err := NewDefault().Evaluate(ctx, b.Build(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

// Divergence carries more information than plain imbalance; in "both" mode divergence should take priority.
func TestDivergenceTakesPrecedenceOverImbalance(t *testing.T) {
	b := builder().Trend(60, 100, 120, 1000, 0.2) // price rising + strong sell pressure: both conditions hold at once

	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "detect": DetectBoth,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sig.Raw["event"] != eventDivergence {
		t.Errorf("event = %v, want %s to take priority", sig.Raw["event"], eventDivergence)
	}
}
