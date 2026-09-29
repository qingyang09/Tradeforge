package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/internal/modules"
	"tradeforge/internal/modules/cvdorderflow"
	"tradeforge/internal/modules/supportresistance"
	"tradeforge/internal/modules/volumebreakout"
	"tradeforge/pkg/types"
)

// realStrategy constructs a strategy config that uses all three real modules.
func realStrategy(combine types.CombineMode, threshold float64) types.StrategyConfig {
	return types.StrategyConfig{
		ID:        "22222222-2222-4222-8222-222222222222",
		Name:      "three-module combined strategy",
		Symbol:    "BTCUSDT",
		Timeframe: types.TF1h,
		Combine:   combine,
		Threshold: threshold,
		Modules: []types.ModuleConfig{
			{
				Module: supportresistance.ModuleName, Weight: 0.4,
				Params: map[string]any{"pivot_strength": 1, "min_touches": 2},
			},
			{
				Module: volumebreakout.ModuleName, Weight: 0.35,
				Params: map[string]any{"window": 20, "multiplier": 2.0},
			},
			{
				Module: cvdorderflow.ModuleName, Weight: 0.25,
				Params: map[string]any{"window": 50, "imbalance_threshold": 0.2},
			},
		},
		Risk: types.RiskConfig{
			MaxPositionSizeQuote: decimal.NewFromInt(1000),
			MaxDailyLossQuote:    decimal.NewFromInt(100),
			MaxHoldingPeriod:     types.D(0),
			StopLossPct:          0.02,
		},
		State: types.StateDraft,
	}
}

// bullishBreakoutData constructs a stretch of market data where "all three
// modules' conditions hold at once":
//
//  1. First oscillate repeatedly in the 100~110 range, forming a key level
//     that gets touched multiple times (feeds support_resistance)
//  2. Then push back up to 110 with a stretch where buy-side pressure
//     dominates, driving up the imbalance within the CVD window (feeds
//     cvd_orderflow — during the oscillating segment buys and sells are
//     balanced, so on its own CVD would stay neutral the whole time)
//  3. The final candle carries 3x volume and closes at 113 (feeds
//     volume_breakout, while also confirming the breakout)
func bullishBreakoutData() types.MarketData {
	b := synth.New("BTCUSDT", types.TF1h, start)
	b.Oscillate(60, 100, 110, 1000)
	b.Trend(19, 100, 110, 1000, 0.85)
	b.AddBar(110, 113, 3000, 0.85)
	return b.Build()
}

// End-to-end: three real modules + ALL combine, should trigger when all three agree.
func TestIntegrationAllModulesAgree(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	cfg := realStrategy(types.CombineAll, 0)
	aud, pub := &recordingAuditor{}, &recordingPublisher{}

	e := New(reg, WithAuditor(aud), WithPublisher(pub), WithLogger(quietLogger()))
	d, err := e.Process(context.Background(), cfg, feedsFor(bullishBreakoutData()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Logf("decision: triggered=%v dir=%s score=%.3f\nreason: %s", d.Triggered, d.Direction, d.Score, d.Reason)
	for _, s := range d.Signals {
		t.Logf("  %-20s %-8s conf=%.3f  %s", s.Module, s.Direction, s.Confidence, s.Reason)
	}

	if len(d.Signals) != 3 {
		t.Fatalf("signal count = %d, want 3", len(d.Signals))
	}
	for _, s := range d.Signals {
		if s.Degraded {
			t.Errorf("module %s unexpectedly degraded: %s", s.Module, s.Err)
		}
		if s.Direction != types.DirectionLong {
			t.Errorf("module %s Direction = %s, want LONG (reason: %s)", s.Module, s.Direction, s.Reason)
		}
	}
	if !d.Triggered {
		t.Fatalf("ALL should trigger when all three modules agree: %s", d.Reason)
	}
	if d.Direction != types.DirectionLong {
		t.Errorf("decision Direction = %s, want LONG", d.Direction)
	}

	// Full-chain audit trail: one record and one publish, each carrying the full signal detail.
	if len(aud.decisions) != 1 || len(pub.decisions) != 1 {
		t.Fatalf("recorded %d, published %d, want 1 each", len(aud.decisions), len(pub.decisions))
	}
	if len(aud.decisions[0].Signals) != 3 {
		t.Error("the audit record must keep every module's signal, or this trade can't be explained after the fact")
	}
}

// Same market data, same modules, but with WEIGHTED combine — should also
// trigger, with an explainable score.
func TestIntegrationWeightedCombine(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	cfg := realStrategy(types.CombineWeighted, 0.5)

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(bullishBreakoutData()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("weighted decision: triggered=%v dir=%s score=%.3f\nreason: %s", d.Triggered, d.Direction, d.Score, d.Reason)

	if !d.Triggered || d.Direction != types.DirectionLong {
		t.Fatalf("expected trigger LONG, got triggered=%v dir=%s: %s", d.Triggered, d.Direction, d.Reason)
	}
	// Weights sum to 1.0, so Score is just the weighted confidence and must fall in [-1, 1].
	if d.Score < -1 || d.Score > 1 {
		t.Errorf("Score = %v out of [-1, 1]", d.Score)
	}
}

// In a quiet market all three modules should stay silent, and neither
// combine mode should trigger.
func TestIntegrationQuietMarketDoesNotTrigger(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	// Completely flat: no key levels, no volume spikes, buys and sells balanced.
	b := synth.New("BTCUSDT", types.TF1h, start)
	for i := 0; i < 120; i++ {
		b.AddBar(100, 100, 1000, 0.5)
	}
	md := b.Build()

	for _, tc := range []struct {
		name string
		cfg  types.StrategyConfig
	}{
		{"ALL", realStrategy(types.CombineAll, 0)},
		{"WEIGHTED", realStrategy(types.CombineWeighted, 0.5)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), tc.cfg, feedsFor(md))
			if err != nil {
				t.Fatal(err)
			}
			if d.Triggered {
				t.Errorf("should not trigger in a quiet market: %s", d.Reason)
			}
			for _, s := range d.Signals {
				if s.Direction != types.DirectionNeutral {
					t.Errorf("module %s gave %s in a quiet market: %s", s.Module, s.Direction, s.Reason)
				}
			}
		})
	}
}

// Different symbols can use completely different module combinations
// without interfering with each other — this is a first-class feature of the platform.
func TestIntegrationPerSymbolIndependentCombinations(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	e := New(reg, WithLogger(quietLogger()))

	// BTC: three-module ALL combine.
	btcCfg := realStrategy(types.CombineAll, 0)
	btcData := bullishBreakoutData()

	// ETH: only the volume module, WEIGHTED combine, with entirely different params.
	ethCfg := types.StrategyConfig{
		ID: "33333333-3333-4333-8333-333333333333", Name: "ETH single-module strategy",
		Symbol: "ETHUSDT", Timeframe: types.TF15m,
		Combine: types.CombineWeighted, Threshold: 0.5,
		Modules: []types.ModuleConfig{{
			Module: volumebreakout.ModuleName, Weight: 1.0,
			Params: map[string]any{"window": 10, "multiplier": 1.5},
		}},
		Risk:  types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(500)},
		State: types.StateDraft,
	}
	ethB := synth.New("ETHUSDT", types.TF15m, start)
	for i := 0; i < 10; i++ {
		ethB.AddBar(2000, 2000, 500, 0.5)
	}
	ethB.AddBar(2000, 2050, 1200, 0.8) // a bullish candle with 2.4x volume
	ethData := ethB.Build()

	btcDecision, err := e.Evaluate(context.Background(), btcCfg, feedsFor(btcData))
	if err != nil {
		t.Fatal(err)
	}
	ethDecision, err := e.Evaluate(context.Background(), ethCfg, feedsFor(ethData))
	if err != nil {
		t.Fatal(err)
	}

	if btcDecision.Symbol != "BTCUSDT" || ethDecision.Symbol != "ETHUSDT" {
		t.Fatalf("symbols got crossed: btc=%s eth=%s", btcDecision.Symbol, ethDecision.Symbol)
	}
	if len(btcDecision.Signals) != 3 {
		t.Errorf("BTC strategy signal count = %d, want 3", len(btcDecision.Signals))
	}
	if len(ethDecision.Signals) != 1 {
		t.Errorf("ETH strategy signal count = %d, want 1", len(ethDecision.Signals))
	}
	if !ethDecision.Triggered {
		t.Errorf("ETH strategy should trigger: %s", ethDecision.Reason)
	}
	// ETH's module list must not get mixed up with BTC's modules.
	for _, s := range ethDecision.Signals {
		if s.Module != volumebreakout.ModuleName {
			t.Errorf("ETH decision contains an unconfigured module %s", s.Module)
		}
	}
}

// Every decision must be able to answer "why" — neither Reason nor any
// module's Reason may be empty.
func TestIntegrationDecisionIsExplainable(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	cfg := realStrategy(types.CombineWeighted, 0.5)

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(bullishBreakoutData()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(d.Reason) == "" {
		t.Error("a decision must carry a reason")
	}
	for _, s := range d.Signals {
		if s.Reason.IsZero() {
			t.Errorf("module %s's signal has no explanation for why it triggered", s.Module)
		}
		if s.Timestamp.IsZero() {
			t.Errorf("module %s's signal is missing a timestamp", s.Module)
		}
	}
	if d.Timestamp.IsZero() || d.EvaluatedAt.IsZero() {
		t.Error("a decision must record both the market-data time and the evaluation time")
	}
	if !d.Price.IsPositive() {
		t.Errorf("decision Price = %s, want positive", d.Price)
	}
}
