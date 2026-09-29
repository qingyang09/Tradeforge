// Package fakeout implements the fakeout signal module: first identify a
// "consolidation range", treat the range's high as "prior high" and its low
// as "prior low", then detect whether price broke above/below the range and
// quickly reversed back — that's a fakeout.
//
// This doesn't use point-by-point pivot clustering (that would report a pile
// of fragmented key levels on any fluctuation, too noisy for a "prior-high
// fakeout" description) — it only tracks one clear range closest to the
// current point. How the range is defined is controlled by range_mode, with
// two mutually incompatible semantics:
//   - tight (default): requires the high/low spread relative to the midpoint
//     price to stay within range_tightness, expanding backward from the
//     current point and stopping the moment it first exceeds tolerance —
//     this is the test for "genuine sideways chop" and automatically
//     excludes trending markets. Limitation: the tolerance is a fixed ratio,
//     and the longer the sample, the more likely it is that one extreme wick
//     stretches the high/low spread — so a consolidation spanning a very
//     long time is actually more likely to get cut off early by this fixed
//     threshold, missing the true prior high/low.
//   - extreme: doesn't test "tight enough" at all — it just takes the
//     highest/lowest price across the entire range_lookback window as the
//     prior high/low, regardless of whether this period was genuinely
//     sideways. Choosing this mode is the user telling the system "I've
//     already decided this period is a consolidation, just report me the
//     extremes." Suited to a consolidation that spans a long time and isn't
//     easy to quantify as a specific number of candles. The tradeoff is it
//     no longer distinguishes "consolidation" from "trend": a one-directional
//     trending market fed into it will still dutifully report the range's
//     highest/lowest price.
package fakeout

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName is this module's identifier in strategy configs.
const ModuleName = "fakeout"

// Range-determination modes; see the package doc.
const (
	RangeModeTight   = "tight"
	RangeModeExtreme = "extreme"
)

// Module implements the fakeout signal module. The zero value is usable.
type Module struct{}

// New returns a module instance.
func New() *Module { return &Module{} }

// Name implements modules.SignalModule.
func (m *Module) Name() string { return ModuleName }

// Description implements modules.SignalModule.
func (m *Module) Description() types.Message {
	return types.Msg("modules.fakeout.description")
}

// RequiredParams implements modules.SignalModule.
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "range_mode", Type: types.ParamString, Default: RangeModeTight,
			Enum:        []string{RangeModeTight, RangeModeExtreme},
			Description: types.Msg("modules.fakeout.param.range_mode"),
		},
		{
			Name: "range_lookback", Type: types.ParamInt, Default: 60,
			Min: types.F(10), Max: types.F(300),
			Description: types.Msg("modules.fakeout.param.range_lookback"),
		},
		{
			Name: "min_range_bars", Type: types.ParamInt, Default: 10,
			Min: types.F(3), Max: types.F(200),
			Description: types.Msg("modules.fakeout.param.min_range_bars"),
		},
		{
			Name: "range_tightness", Type: types.ParamFloat, Default: 0.03,
			Min: types.F(0.002), Max: types.F(0.2),
			Description: types.Msg("modules.fakeout.param.range_tightness"),
		},
		{
			Name: "breakout_confirm", Type: types.ParamFloat, Default: 0.001,
			Min: types.F(0), Max: types.F(0.05),
			Description: types.Msg("modules.fakeout.param.breakout_confirm"),
		},
		{
			Name: "reversal_window", Type: types.ParamInt, Default: 5,
			Min: types.F(1), Max: types.F(20),
			Description: types.Msg("modules.fakeout.param.reversal_window"),
		},
		{
			Name: "reversal_confirm", Type: types.ParamFloat, Default: 0.001,
			Min: types.F(0), Max: types.F(0.05),
			Description: types.Msg("modules.fakeout.param.reversal_confirm"),
		},
	}
}

// Event types, written to Signal.Raw["event"].
const (
	eventFakeoutResistance = "fakeout_resistance" // false breakout above the range high, then reversed -> bearish
	eventFakeoutSupport    = "fakeout_support"    // false breakdown below the range low, then reversed -> bullish
	eventNone              = "none"
)

// consolidationRange is an identified consolidation range.
type consolidationRange struct {
	High decimal.Decimal
	Low  decimal.Decimal
	Bars int
}

// Evaluate implements modules.SignalModule.
func (m *Module) Evaluate(ctx context.Context, md types.MarketData, params map[string]any) (types.Signal, error) {
	p, err := types.ResolveParams(ModuleName, m.RequiredParams(), params)
	if err != nil {
		return types.Signal{}, err
	}
	if err := ctx.Err(); err != nil {
		return types.Signal{}, err
	}

	rangeMode := types.MustString(p, "range_mode")
	rangeLookback := types.MustInt(p, "range_lookback")
	minRangeBars := types.MustInt(p, "min_range_bars")
	rangeTightness := decimal.NewFromFloat(types.MustFloat(p, "range_tightness"))
	breakoutConfirm := decimal.NewFromFloat(types.MustFloat(p, "breakout_confirm"))
	reversalWindow := types.MustInt(p, "reversal_window")
	reversalConfirm := decimal.NewFromFloat(types.MustFloat(p, "reversal_confirm"))

	neutral := func(reason types.Message, raw map[string]any) types.Signal {
		s := types.NeutralSignal(ModuleName, md.Symbol, reason, md.Time())
		if last, ok := md.Last(); ok {
			s.Price = last.Close
		}
		s.Raw = raw
		return s
	}
	notFound := map[string]any{"event": eventNone, "range_found": false}

	// The scan window is "the candles where a breakout might have happened" +
	// the current candle (used to determine whether it has already reversed back).
	scanSize := reversalWindow + 1
	if len(md.Candles) < minRangeBars+scanSize {
		return neutral(types.Msg("modules.fakeout.reason.insufficient_bars",
			"required", minRangeBars+scanSize, "actual", len(md.Candles)), notFound), nil
	}

	n := len(md.Candles)
	cur := md.Candles[n-1]
	if !cur.Close.IsPositive() {
		return neutral(types.Msg("modules.fakeout.reason.invalid_close"), notFound), nil
	}
	// The candles within the scan window where a breakout might have happened (excluding the current candle).
	scanCandles := md.Candles[n-scanSize : n-1]

	// The consolidation range is computed only from history before the scan
	// window, to keep the breakout event we're currently trying to detect
	// from leaking into the range calculation.
	levelHistory := md.Candles[:n-scanSize]
	if len(levelHistory) > rangeLookback {
		levelHistory = levelHistory[len(levelHistory)-rangeLookback:]
	}
	// Carry out "how far back this actually looked" even when no range was
	// found — the chart uses it to mark the analysis window's start, so the
	// user can see "the system looked at this much history but found
	// nothing", rather than assuming it didn't look at all.
	windowStart := map[string]any{"window_start": levelHistory[0].OpenTime.Format(time.RFC3339)}

	var rng consolidationRange
	var found bool
	if rangeMode == RangeModeExtreme {
		rng, found = extremeRange(levelHistory, minRangeBars)
	} else {
		rng, found = findConsolidationRange(levelHistory, minRangeBars, rangeTightness)
	}
	if !found {
		raw := map[string]any{"event": eventNone, "range_found": false}
		for k, v := range windowStart {
			raw[k] = v
		}
		return neutral(types.Msg("modules.fakeout.reason.no_range_found"), raw), nil
	}

	// Once a range is found, window_start is switched to point at that
	// range's actual start (the last rng.Bars candles of levelHistory)
	// instead of the entire rangeLookback window's start — rng.High/rng.Low
	// were only computed over those candles within the range, so if we kept
	// reporting "looked back rangeLookback candles", the analysis-window
	// marker drawn on the chart would be wider than the range itself, and the
	// user would assume the "prior high" should cover all the way out to
	// where the marker sits — but the history outside the marker, before the
	// range's actual start (even if it contains a taller wick), was never
	// actually counted into the "prior high" at all. This is exactly a bug
	// that really happened before: "the prior high didn't capture the true
	// max of the whole consolidation zone" — the root cause was this marker
	// not matching the actual computation range, not rng.High being computed wrong.
	rangeStart := levelHistory[len(levelHistory)-rng.Bars]
	windowStart = map[string]any{"window_start": rangeStart.OpenTime.Format(time.RFC3339)}

	rangeInfo := map[string]any{
		"range_high": rng.High.String(),
		"range_low":  rng.Low.String(),
		"range_bars": rng.Bars,
	}
	for k, v := range windowStart {
		rangeInfo[k] = v
	}

	one := decimal.NewFromInt(1)
	upBreakout := rng.High.Mul(one.Add(breakoutConfirm))
	downReclaim := rng.High.Mul(one.Sub(reversalConfirm))
	downBreakout := rng.Low.Mul(one.Sub(breakoutConfirm))
	upReclaim := rng.Low.Mul(one.Add(reversalConfirm))

	var event string
	var dir types.Direction
	var levelPrice, breakoutClose decimal.Decimal

	if maxClose, ok := maxCloseAbove(scanCandles, upBreakout); ok && cur.Close.LessThan(downReclaim) {
		event, dir, levelPrice, breakoutClose = eventFakeoutResistance, types.DirectionShort, rng.High, maxClose
	} else if minClose, ok := minCloseBelow(scanCandles, downBreakout); ok && cur.Close.GreaterThan(upReclaim) {
		event, dir, levelPrice, breakoutClose = eventFakeoutSupport, types.DirectionLong, rng.Low, minClose
	}

	if event == "" {
		raw := map[string]any{"event": eventNone, "range_found": true}
		for k, v := range rangeInfo {
			raw[k] = v
		}
		return neutral(types.Msg("modules.fakeout.reason.no_fakeout_pattern"), raw), nil
	}

	raw := map[string]any{
		"event":          event,
		"level_price":    levelPrice.String(),
		"breakout_close": breakoutClose.String(),
		"reclaim_close":  cur.Close.String(),
		"range_found":    true,
	}
	for k, v := range rangeInfo {
		raw[k] = v
	}

	return types.Signal{
		Module:     ModuleName,
		Symbol:     md.Symbol,
		Direction:  dir,
		Confidence: confidenceFor(rng.Bars, minRangeBars),
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason:     reasonFor(event, levelPrice, rng.Bars, breakoutClose, cur.Close),
		Raw:        raw,
	}, nil
}

// findConsolidationRange expands the window backward from the end of
// history as far as possible, finding the longest segment closest to the
// current point whose spread still stays within tightness.
//
// As the window expands, the range high can only get higher and the low can
// only get lower, so their difference is monotonically non-decreasing; as
// long as price stays positive, the spread's ratio to the midpoint price is
// therefore also monotonically non-decreasing (provable: holding the low
// fixed and raising the high, the sign of the ratio's derivative with
// respect to the new high equals the low itself, always positive; holding
// the high fixed and lowering the low is symmetric). So "stop the first time
// the ratio exceeds the threshold" is guaranteed to find the longest valid
// window, with no need to backtrack and retry.
func findConsolidationRange(history []types.Candle, minBars int, tightness decimal.Decimal) (consolidationRange, bool) {
	n := len(history)
	if n < minBars {
		return consolidationRange{}, false
	}

	var best consolidationRange
	found := false
	var hi, lo decimal.Decimal

	for bars := 1; bars <= n; bars++ {
		c := history[n-bars]
		if bars == 1 || c.High.GreaterThan(hi) {
			hi = c.High
		}
		if bars == 1 || c.Low.LessThan(lo) {
			lo = c.Low
		}
		if bars < minBars {
			continue
		}
		mid := hi.Add(lo).Div(decimal.NewFromInt(2))
		if !mid.IsPositive() {
			break
		}
		if hi.Sub(lo).Div(mid).GreaterThan(tightness) {
			break
		}
		best = consolidationRange{High: hi, Low: lo, Bars: bars}
		found = true
	}
	return best, found
}

// extremeRange is the range definition used when range_mode=extreme: it
// doesn't test "tight enough" at all, it just takes the highest/lowest price
// across the entire history (range_lookback candles long) as the prior high/low.
//
// The key difference from findConsolidationRange: the latter stops expanding
// the window early wherever the spread breaks tolerance, so a longer window
// is more likely to get cut off by a single wick; extreme skips that test
// entirely — the user choosing this mode has already decided this whole
// period is a consolidation, so the system just dutifully reports the
// extremes within it.
func extremeRange(history []types.Candle, minBars int) (consolidationRange, bool) {
	n := len(history)
	if n < minBars {
		return consolidationRange{}, false
	}
	hi, lo := history[0].High, history[0].Low
	for _, c := range history[1:] {
		if c.High.GreaterThan(hi) {
			hi = c.High
		}
		if c.Low.LessThan(lo) {
			lo = c.Low
		}
	}
	return consolidationRange{High: hi, Low: lo, Bars: n}, true
}

// maxCloseAbove reports whether any candle in candles closed above threshold, and returns the highest such close.
func maxCloseAbove(candles []types.Candle, threshold decimal.Decimal) (decimal.Decimal, bool) {
	var max decimal.Decimal
	found := false
	for _, c := range candles {
		if c.Close.GreaterThan(threshold) && (!found || c.Close.GreaterThan(max)) {
			max, found = c.Close, true
		}
	}
	return max, found
}

// minCloseBelow reports whether any candle in candles closed below threshold, and returns the lowest such close.
func minCloseBelow(candles []types.Candle, threshold decimal.Decimal) (decimal.Decimal, bool) {
	var min decimal.Decimal
	found := false
	for _, c := range candles {
		if c.Close.LessThan(threshold) && (!found || c.Close.LessThan(min)) {
			min, found = c.Close, true
		}
	}
	return min, found
}

// confidenceFor maps the number of candles the range held for into a [0,1]
// confidence: the longer it held, the more solid the range, and the more
// credible the fakeout determination. Baseline 0.55, with a full 0.3 added
// for every 20 candles beyond min_range_bars, capped at 0.95.
func confidenceFor(bars, minBars int) float64 {
	extra := float64(bars-minBars) / 20.0
	c := 0.55 + 0.3*extra
	if c > 0.95 {
		c = 0.95
	}
	if c < 0.55 {
		c = 0.55
	}
	return c
}

func reasonFor(event string, levelPrice decimal.Decimal, rangeBars int, breakoutClose, reclaimClose decimal.Decimal) types.Message {
	switch event {
	case eventFakeoutResistance:
		return types.Msg("modules.fakeout.reason.fakeout_resistance",
			"bars", rangeBars, "level", levelPrice.String(),
			"breakout_close", breakoutClose.String(), "reclaim_close", reclaimClose.String())
	case eventFakeoutSupport:
		return types.Msg("modules.fakeout.reason.fakeout_support",
			"bars", rangeBars, "level", levelPrice.String(),
			"breakout_close", breakoutClose.String(), "reclaim_close", reclaimClose.String())
	default:
		return types.Msg("modules.fakeout.reason.no_event")
	}
}
