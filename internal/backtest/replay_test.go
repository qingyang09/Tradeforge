package backtest

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testStrategy() types.StrategyConfig {
	return types.StrategyConfig{
		ID: "11111111-1111-4111-8111-111111111111", Name: "test",
		Symbol: "BTCUSDT", Timeframe: types.TF1h, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{
			{Module: "support_resistance", Params: map[string]any{"pivot_strength": 1, "min_touches": 2}},
			{Module: "volume_breakout", Params: map[string]any{"window": 20, "multiplier": 2.0}},
			{Module: "cvd_orderflow", Params: map[string]any{"window": 50, "imbalance_threshold": 0.2}},
		},
		Risk:  types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State: types.StateDraft,
	}
}

// structuredData replicates gen-testdata's first shape: candle 219 is the designed trigger point.
func structuredData() types.MarketData {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	return synth.New("BTCUSDT", types.TF1h, start).
		Oscillate(200, 100, 110, 1000).
		Trend(19, 100, 110, 1000, 0.85).
		AddBar(110, 113, 3000, 0.85).
		RandomWalk(60, 113, 0.008, 1000, 20250101).
		Build()
}

// Every candle must produce exactly one decision, in the same order as the
// candles — downstream backtesting relies on this correspondence to align
// signals with prices.
func TestReplayEmitsOneDecisionPerCandle(t *testing.T) {
	md := structuredData()
	meta, decisions, err := Replay(context.Background(), testStrategy(), md, nil, DefaultWindow, quietLogger())
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	if meta.BarCount != len(md.Candles) {
		t.Errorf("meta.bar_count = %d, want %d", meta.BarCount, len(md.Candles))
	}
	if len(decisions) != len(md.Candles) {
		t.Fatalf("decision count = %d, want %d", len(decisions), len(md.Candles))
	}
	for i, d := range decisions {
		if d.Index != i {
			t.Fatalf("decision %d has index = %d, out of order", i, d.Index)
		}
		if !d.BarTime.Equal(md.Candles[i].CloseTime) {
			t.Fatalf("decision %d's time %s doesn't match the candle's close time %s",
				i, d.BarTime, md.Candles[i].CloseTime)
		}
	}
}

// The designed trigger point must trigger, and it must be the only one —
// this is the key assertion for the end-to-end chain.
func TestReplayTriggersAtDesignedPoint(t *testing.T) {
	_, decisions, err := Replay(context.Background(), testStrategy(), structuredData(), nil, DefaultWindow, quietLogger())
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	var triggered []int
	for _, d := range decisions {
		if d.Triggered {
			triggered = append(triggered, d.Index)
		}
	}
	if len(triggered) != 1 || triggered[0] != 219 {
		t.Fatalf("trigger points = %v, want only candle 219 (the constructed breakout+volume+orderflow-agreeing one)", triggered)
	}

	d := decisions[219]
	if d.Direction != "LONG" {
		t.Errorf("Direction = %s, want LONG", d.Direction)
	}
	if len(d.Signals) != 3 {
		t.Fatalf("signal count = %d, want 3", len(d.Signals))
	}
	// Explainability: every module must state why it gave this direction.
	for _, s := range d.Signals {
		if s.Direction != types.DirectionLong {
			t.Errorf("module %s Direction = %s, want LONG", s.Module, s.Direction)
		}
		if strings.TrimSpace(s.Reason) == "" {
			t.Errorf("module %s has no explanation for why it triggered", s.Module)
		}
	}
	t.Logf("trigger point signal detail:")
	for _, s := range d.Signals {
		t.Logf("  %-20s conf=%.3f  %s", s.Module, s.Confidence, s.Reason)
	}
}

// Look-ahead bias is the most damning mistake a backtest can make: candle
// i's decision must never be affected by data from candle i+1 onward.
//
// Verification method: swap out the entire stretch of data after a given
// candle, and the replayed decisions up to that candle must be byte-for-byte identical.
func TestReplayHasNoLookAheadBias(t *testing.T) {
	full := structuredData()
	cut := 260

	// Build a version of the market data that's identical for the first
	// `cut` candles and completely different afterward.
	altered := types.MarketData{Symbol: full.Symbol, Timeframe: full.Timeframe}
	altered.Candles = append(altered.Candles, full.Candles[:cut]...)
	tail := synth.New("BTCUSDT", types.TF1h, full.Candles[cut].OpenTime).
		Trend(len(full.Candles)-cut, 113, 500, 9999, 0.99).
		Build()
	altered.Candles = append(altered.Candles, tail.Candles...)

	cfg := testStrategy()
	_, base, err := Replay(context.Background(), cfg, full, nil, DefaultWindow, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := Replay(context.Background(), cfg, altered, nil, DefaultWindow, quietLogger())
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < cut; i++ {
		if base[i].Triggered != other[i].Triggered ||
			base[i].Direction != other[i].Direction ||
			base[i].Score != other[i].Score {
			t.Fatalf("decision for candle %d changed due to future data, look-ahead bias present:\n  original=%+v\n  altered=%+v",
				i, base[i], other[i])
		}
	}
}

// ---------- multi-timeframe: a context timeframe must not leak unclosed data early ----------

// contextHourly1h builds a stretch of 1-hour candles: a flat run averaging
// around 1000 volume, except for the spikeAt candle (when spike=true) which
// carries 6x volume on a bullish close — if volume_breakout ever actually
// sees this candle, it flips the signal from neutral to LONG. Used to check
// that "before this candle has truly closed, the trigger timeframe's
// decision must never change because of it."
func contextHourly1h(hours, spikeAt int, spike bool) types.MarketData {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	b := synth.New("BTCUSDT", types.TF1h, start)
	for i := 0; i < hours; i++ {
		vol := 1000.0
		if spike && i == spikeAt {
			vol = 6000.0
		}
		b.Add(100, 106, 99, 105, vol, 0.5) // fixed small bullish candle, direction always LONG, only volume varies
	}
	return b.Build()
}

// triggerQuarterHourly builds 15-minute candles spanning the same time
// range as contextHourly1h; their content doesn't itself feed into the
// signal computation (the strategy's only module runs on the 1h context
// timeframe) — it only provides the trigger cadence.
func triggerQuarterHourly(hours int) types.MarketData {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	b := synth.New("BTCUSDT", types.TF15m, start)
	for i := 0; i < hours*4; i++ {
		b.Add(100, 100.5, 99.5, 100, 250, 0.5)
	}
	return b.Build()
}

func multiTFStrategy() types.StrategyConfig {
	return types.StrategyConfig{
		ID: "44444444-4444-4444-8444-444444444444", Name: "multi-timeframe test",
		Symbol: "BTCUSDT", Timeframe: types.TF15m, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{{
			Module: "volume_breakout", Timeframe: types.TF1h,
			Params: map[string]any{"window": 20, "multiplier": 2.0},
		}},
		Risk:  types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State: types.StateDraft,
	}
}

// The fundamental precondition for a backtest to be trustworthy: the
// trigger timeframe's decisions, before the context timeframe's candle has
// actually closed, must never change based on how that candle ends up looking.
func TestReplayContextTimeframeHasNoLookAheadBias(t *testing.T) {
	const hours = 40
	const spikeAt = 25 // 0-indexed, the 26th 1h candle

	trig := triggerQuarterHourly(hours)
	cfg := multiTFStrategy()

	runWithContext := func(ctxMD types.MarketData) []DecisionLine {
		contextFeeds := map[types.Timeframe]types.MarketData{types.TF1h: ctxMD}
		_, decisions, err := Replay(context.Background(), cfg, trig, contextFeeds, DefaultWindow, quietLogger())
		if err != nil {
			t.Fatalf("replay failed: %v", err)
		}
		return decisions
	}

	spikeDecisions := runWithContext(contextHourly1h(hours, spikeAt, true))
	noSpikeDecisions := runWithContext(contextHourly1h(hours, spikeAt, false))

	if len(spikeDecisions) != len(noSpikeDecisions) || len(spikeDecisions) != len(trig.Candles) {
		t.Fatalf("decision counts don't match: spike=%d noSpike=%d trigger candles=%d",
			len(spikeDecisions), len(noSpikeDecisions), len(trig.Candles))
	}

	// The close time of the spikeAt 1h candle is the first moment it's
	// genuinely closed on the shared 15-minute timeline — corresponding to
	// index (spikeAt+1)*4-1 in the 15-minute series (4 15-minute candles per hour).
	visibleFromIdx := (spikeAt+1)*4 - 1

	for i := 0; i < visibleFromIdx; i++ {
		if spikeDecisions[i].Triggered != noSpikeDecisions[i].Triggered ||
			spikeDecisions[i].Direction != noSpikeDecisions[i].Direction ||
			spikeDecisions[i].Score != noSpikeDecisions[i].Score {
			t.Fatalf("trigger candle %d's decision already differed before the volume-spike 1h candle closed, "+
				"cross-timeframe look-ahead bias present:\n  spike=%+v\n  noSpike=%+v",
				i, spikeDecisions[i], noSpikeDecisions[i])
		}
	}

	// Reverse sanity check: after the candle genuinely closes, the two runs
	// must diverge somewhere — otherwise the context timeframe's data was
	// never actually aligned in, and this test is only validating an empty shell.
	differed := false
	for i := visibleFromIdx; i < len(spikeDecisions); i++ {
		if spikeDecisions[i].Triggered != noSpikeDecisions[i].Triggered ||
			spikeDecisions[i].Direction != noSpikeDecisions[i].Direction {
			differed = true
			break
		}
	}
	if !differed {
		t.Fatal("after the volume-spike 1h candle closes, the two runs should diverge; " +
			"staying identical means the context timeframe's data was never aligned in, so this test isn't exercising real behavior")
	}
}

// A sliding window should not change the decision results — the window is
// only a performance optimization, not part of the semantics.
func TestWindowSizeDoesNotChangeDecisions(t *testing.T) {
	md := structuredData()
	cfg := testStrategy()

	_, wide, err := Replay(context.Background(), cfg, md, nil, 0, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	_, narrow, err := Replay(context.Background(), cfg, md, nil, DefaultWindow, quietLogger())
	if err != nil {
		t.Fatal(err)
	}

	if len(wide) != len(narrow) {
		t.Fatalf("decision counts don't match: %d vs %d", len(wide), len(narrow))
	}
	for i := range wide {
		if !reflect.DeepEqual(wide[i], narrow[i]) {
			t.Fatalf("decision %d doesn't match, window size affected the decision result:\n  full=%+v\n  windowed=%+v",
				i, wide[i], narrow[i])
		}
	}
}
