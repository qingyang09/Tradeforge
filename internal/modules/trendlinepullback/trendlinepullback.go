// Package trendlinepullback implements the trendline_pullback signal
// module: "pullback to a rising trendline, holding a higher low" (long) and
// its mirror, "rally to a falling trendline, holding a lower high" (short).
//
// The algorithm has five steps:
//
//  1. Identify swing highs/lows (pivots) the same way support_resistance
//     does: a candle whose high/low exceeds every one of pivot_strength
//     candles on both sides.
//  2. Confirm trend structure from the two most recent confirmed swing lows
//     (long) or highs (short): the more recent one must favor the trend
//     more than the one before it (a higher low / lower high). That alone
//     isn't enough -- two higher lows can happen inside an ordinary trading
//     range -- so the swing extremum of the opposite kind between them must
//     also favor the trend more than the one before that (a higher high /
//     lower high), which is what actually distinguishes a trend from a range.
//  3. Require a confirmed swing extremum of the opposite kind after the
//     more recent of the two trend-confirming points. This marks the top
//     (long) or bottom (short) of the leg that's now pulling back, and is
//     what keeps the still-forming pullback itself from being mistaken for
//     a new, even-more-recent trend-confirming point -- without it, the
//     pullback's own low/high would sometimes get picked up by step 2 as
//     "the newest confirmed swing low/high" instead of what it actually is.
//  4. The trendline connects the two trend-confirming points, by candle
//     index rather than wall-clock time -- candles within one MarketData
//     are always evenly spaced, so index position is already a valid proxy
//     for time, and working in index units avoids decimal-from-duration
//     conversions. Extrapolated forward, it gives "where the trendline
//     should be" at any later candle.
//  5. Scan every candle since the step-3 extremum through the current
//     candle for the pullback's own extremum (the lowest low for long, the
//     highest high for short). This candle deliberately doesn't need to be
//     a confirmed pivot itself -- the whole point is detecting the test
//     while it's still happening, not waiting for it to become history.
//     Confirm the pattern: this extremum must still favor the trend
//     relative to the more recent trend-confirming point (structure
//     intact), sit within trendline_tolerance of the trendline's value at
//     that candle (a genuine test of the line, not just any old pullback),
//     and the current candle's close must have already moved away from
//     that extremum by at least bounce_confirm (the test has actually been
//     defended, not still underway).
package trendlinepullback

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName is this module's identifier in strategy configs.
const ModuleName = "trendline_pullback"

// Module implements the trendline_pullback signal module. The zero value is usable.
type Module struct{}

// New returns a module instance.
func New() *Module { return &Module{} }

// Name implements modules.SignalModule.
func (m *Module) Name() string { return ModuleName }

// Description implements modules.SignalModule.
func (m *Module) Description() types.Message {
	return types.Msg("modules.trendline_pullback.description")
}

// RequiredParams implements modules.SignalModule.
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "swing_lookback", Type: types.ParamInt, Default: 150,
			Min: types.F(30), Max: types.F(1000),
			Description: types.Msg("modules.trendline_pullback.param.swing_lookback"),
		},
		{
			Name: "pivot_strength", Type: types.ParamInt, Default: 3,
			Min: types.F(1), Max: types.F(10),
			Description: types.Msg("modules.trendline_pullback.param.pivot_strength"),
		},
		{
			Name: "trendline_tolerance", Type: types.ParamFloat, Default: 0.01,
			Min: types.F(0.001), Max: types.F(0.05),
			Description: types.Msg("modules.trendline_pullback.param.trendline_tolerance"),
		},
		{
			Name: "bounce_confirm", Type: types.ParamFloat, Default: 0.003,
			Min: types.F(0), Max: types.F(0.05),
			Description: types.Msg("modules.trendline_pullback.param.bounce_confirm"),
		},
	}
}

// pivot is a single confirmed swing high or swing low.
type pivot struct {
	Price decimal.Decimal
	Index int
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

	swingLookback := types.MustInt(p, "swing_lookback")
	pivotStrength := types.MustInt(p, "pivot_strength")
	tolerance := decimal.NewFromFloat(types.MustFloat(p, "trendline_tolerance"))
	bounceConfirm := decimal.NewFromFloat(types.MustFloat(p, "bounce_confirm"))

	neutral := func(reason types.Message) types.Signal {
		s := types.NeutralSignal(ModuleName, md.Symbol, reason, md.Time())
		if last, ok := md.Last(); ok {
			s.Price = last.Close
		}
		return s
	}

	// Need enough candles to plausibly contain five alternating confirmed
	// pivots (high, low, high, low, high) plus room for a pullback after the
	// last of them -- a loose floor, not a tight bound; findPivots/the
	// structural checks below report a far more specific reason whenever
	// the actual history doesn't contain that shape.
	minCandles := 4*pivotStrength + 9
	if len(md.Candles) < minCandles {
		return neutral(types.Msg("modules.trendline_pullback.reason.insufficient_candles",
			"min", minCandles, "actual", len(md.Candles))), nil
	}
	cur := md.Candles[len(md.Candles)-1]
	if !cur.Close.IsPositive() {
		return neutral(types.Msg("modules.trendline_pullback.reason.non_positive_close")), nil
	}

	// Pivots are found from history that excludes only the latest candle --
	// the same guard as support_resistance, for the same reason (the
	// current candle must never define its own pivot). The pullback-extreme
	// scan in detectBullish/detectBearish works over the full candle slice
	// instead, deliberately including the latest candle: that's the thing
	// actually being tested for a still-forming pullback, not history.
	history := md.Candles[:len(md.Candles)-1]
	if len(history) > swingLookback {
		history = history[len(history)-swingLookback:]
	}
	offset := len(md.Candles) - 1 - len(history)
	highs, lows := findPivots(history, pivotStrength, offset)

	if sig, reason, ok := detectBullish(md.Candles, highs, lows, tolerance, bounceConfirm); ok {
		sig.Symbol = md.Symbol
		return sig, nil
	} else if sig, bearReason, bearOk := detectBearish(md.Candles, highs, lows, tolerance, bounceConfirm); bearOk {
		sig.Symbol = md.Symbol
		return sig, nil
	} else {
		s := neutral(reason)
		s.Raw = map[string]any{
			"bullish_reason": reason.Key,
			"bearish_reason": bearReason.Key,
			"pivot_highs":    len(highs),
			"pivot_lows":     len(lows),
		}
		return s, nil
	}
}

// findPivots identifies swing highs and swing lows within candles, whose
// global (within md.Candles) index is candles' own index plus offset -- this
// lets Evaluate pass in a lookback-truncated slice while every pivot's Index
// still lines up with positions in the original, untruncated candle slice
// that detectBullish/detectBearish actually index into.
//
// A candle is a swing high if and only if its high is strictly greater than
// the high of each of the `strength` candles on both sides (the same
// definition, and the same reasoning for strictness, as
// support_resistance.findPivots -- duplicated rather than imported, per this
// package's constraint of depending only on pkg/types).
func findPivots(candles []types.Candle, strength, offset int) (highs, lows []pivot) {
	for i := strength; i < len(candles)-strength; i++ {
		isHigh, isLow := true, true
		for j := i - strength; j <= i+strength; j++ {
			if j == i {
				continue
			}
			if candles[j].High.GreaterThanOrEqual(candles[i].High) {
				isHigh = false
			}
			if candles[j].Low.LessThanOrEqual(candles[i].Low) {
				isLow = false
			}
			if !isHigh && !isLow {
				break
			}
		}
		if isHigh {
			highs = append(highs, pivot{Price: candles[i].High, Index: i + offset})
		}
		if isLow {
			lows = append(lows, pivot{Price: candles[i].Low, Index: i + offset})
		}
	}
	return highs, lows
}

// mostRecentBefore returns the pivot with the largest Index that is still
// strictly less than beforeIndex.
func mostRecentBefore(pivots []pivot, beforeIndex int) (pivot, bool) {
	var best pivot
	found := false
	for _, p := range pivots {
		if p.Index < beforeIndex && (!found || p.Index > best.Index) {
			best, found = p, true
		}
	}
	return best, found
}

// mostRecentAfter returns the pivot with the smallest Index that is still
// strictly greater than afterIndex.
func mostRecentAfter(pivots []pivot, afterIndex int) (pivot, bool) {
	var best pivot
	found := false
	for _, p := range pivots {
		if p.Index > afterIndex && (!found || p.Index < best.Index) {
			best, found = p, true
		}
	}
	return best, found
}

// mostExtremeBetween finds, among pivots whose Index is strictly between
// fromIndex and toIndex, the one whose Price better ranks highest -- used to
// find the actual peak/trough of a leg, not just any confirmed point within it.
func mostExtremeBetween(pivots []pivot, fromIndex, toIndex int, better func(a, b decimal.Decimal) bool) (pivot, bool) {
	var best pivot
	found := false
	for _, p := range pivots {
		if p.Index > fromIndex && p.Index < toIndex && (!found || better(p.Price, best.Price)) {
			best, found = p, true
		}
	}
	return best, found
}

// pullbackExtreme scans candles[fromIndex:] (inclusive of both ends) for the
// index whose val is most extreme per worse -- the lowest low (long) or
// highest high (short) reached since the leg's opposite-kind pivot,
// deliberately including the very last candle, since that is the thing
// actually being tested right now.
func pullbackExtreme(
	candles []types.Candle, fromIndex int,
	val func(types.Candle) decimal.Decimal, worse func(a, b decimal.Decimal) bool,
) (idx int, price decimal.Decimal) {
	idx = fromIndex
	price = val(candles[fromIndex])
	for i := fromIndex + 1; i < len(candles); i++ {
		v := val(candles[i])
		if worse(v, price) {
			idx, price = i, v
		}
	}
	return idx, price
}

// trendlineValueAt extrapolates the line through a and b (by candle index)
// to the given index.
func trendlineValueAt(a, b pivot, index int) decimal.Decimal {
	steps := decimal.NewFromInt(int64(b.Index - a.Index))
	if steps.IsZero() {
		return a.Price
	}
	slope := b.Price.Sub(a.Price).Div(steps)
	return a.Price.Add(slope.Mul(decimal.NewFromInt(int64(index - a.Index))))
}

// relDist returns the relative distance |a-b|/b between two prices. When b is
// zero or negative, it returns a large value guaranteed to exceed any tolerance.
func relDist(a, b decimal.Decimal) decimal.Decimal {
	if !b.IsPositive() {
		return decimal.NewFromInt(1 << 30)
	}
	return a.Sub(b).Abs().Div(b)
}

// confidenceFor maps how close the test came to the trendline, and how
// strong the bounce away from it was, to a [0,1] confidence. Both terms
// saturate once they clear their own threshold by 3x, so a textbook-clean
// test doesn't score unboundedly higher than one that merely cleared the bar.
func confidenceFor(dist, tolerance, bounce, bounceConfirm decimal.Decimal) float64 {
	toleranceScore := 1.0
	if tolerance.IsPositive() {
		if f, ok := dist.Div(tolerance).Float64(); ok {
			toleranceScore = 1 - f
		}
	}
	if toleranceScore < 0 {
		toleranceScore = 0
	}
	bounceScore := 1.0
	if bounceConfirm.IsPositive() {
		if f, ok := bounce.Div(bounceConfirm.Mul(decimal.NewFromInt(3))).Float64(); ok {
			bounceScore = f
		}
		if bounceScore > 1 {
			bounceScore = 1
		}
		if bounceScore < 0 {
			bounceScore = 0
		}
	}
	c := 0.5 + 0.25*toleranceScore + 0.2*bounceScore
	if c > 0.95 {
		c = 0.95
	}
	if c < 0.5 {
		c = 0.5
	}
	return c
}

func pivotRaw(p pivot) map[string]any {
	return map[string]any{"index": p.Index, "price": p.Price.String()}
}

// detectBullish checks for "pullback to a rising trendline, holding a
// higher low" -- see the package doc for the five-step algorithm. reason is
// only meaningful when ok is false.
func detectBullish(
	candles []types.Candle, highs, lows []pivot, tolerance, bounceConfirm decimal.Decimal,
) (sig types.Signal, reason types.Message, ok bool) {
	if len(lows) < 2 {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.not_enough_lows"), false
	}
	lLast := lows[len(lows)-1]
	lPrev := lows[len(lows)-2]
	if !lLast.Price.GreaterThan(lPrev.Price) {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.lows_not_ascending"), false
	}

	hBefore, okBefore := mostRecentBefore(highs, lPrev.Index)
	if !okBefore {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.no_high_before_lows"), false
	}
	hMid, okMid := mostExtremeBetween(highs, lPrev.Index, lLast.Index, decimal.Decimal.GreaterThan)
	if !okMid {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.no_high_between_lows"), false
	}
	if !hMid.Price.GreaterThan(hBefore.Price) {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.structure_not_uptrend"), false
	}

	hAfter, okAfter := mostRecentAfter(highs, lLast.Index)
	if !okAfter {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.no_rally_after_higher_low"), false
	}
	if hAfter.Index+1 >= len(candles) {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.no_pullback_yet"), false
	}

	testIdx, testLow := pullbackExtreme(candles, hAfter.Index+1,
		func(c types.Candle) decimal.Decimal { return c.Low }, decimal.Decimal.LessThan)

	if !testLow.GreaterThan(lLast.Price) {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.pullback_broke_structure"), false
	}

	trendlinePrice := trendlineValueAt(lPrev, lLast, testIdx)
	dist := relDist(testLow, trendlinePrice)
	if dist.GreaterThan(tolerance) {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.pullback_not_near_trendline"), false
	}

	cur := candles[len(candles)-1]
	bounce := cur.Close.Sub(testLow).Div(testLow)
	if bounce.LessThan(bounceConfirm) {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.bounce_not_confirmed"), false
	}

	raw := map[string]any{
		"pattern":                 "uptrend_pullback",
		"trend_low_prev":          pivotRaw(lPrev),
		"trend_low_last":          pivotRaw(lLast),
		"trend_high_before":       pivotRaw(hBefore),
		"trend_high_mid":          pivotRaw(hMid),
		"leg_high_after":          pivotRaw(hAfter),
		"tested_low":              map[string]any{"index": testIdx, "price": testLow.String(), "time": candles[testIdx].OpenTime.Format(time.RFC3339)},
		"trendline_price_at_test": trendlinePrice.String(),
		"distance_ratio":          dist.String(),
		"bounce_ratio":            bounce.String(),
	}

	return types.Signal{
		Module:     ModuleName,
		Direction:  types.DirectionLong,
		Confidence: confidenceFor(dist, tolerance, bounce, bounceConfirm),
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason: types.Msg("modules.trendline_pullback.reason.uptrend_pullback_confirmed",
			"test_low", testLow.String(), "trend_low", lLast.Price.String()),
		Raw: raw,
	}, types.Message{}, true
}

// detectBearish is detectBullish's mirror: "rally to a falling trendline,
// holding a lower high". Every comparison is flipped (ascending structure ->
// descending structure, bounce up off a low -> rejection down off a high).
func detectBearish(
	candles []types.Candle, highs, lows []pivot, tolerance, bounceConfirm decimal.Decimal,
) (sig types.Signal, reason types.Message, ok bool) {
	if len(highs) < 2 {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.not_enough_highs"), false
	}
	hLast := highs[len(highs)-1]
	hPrev := highs[len(highs)-2]
	if !hLast.Price.LessThan(hPrev.Price) {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.highs_not_descending"), false
	}

	lBefore, okBefore := mostRecentBefore(lows, hPrev.Index)
	if !okBefore {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.no_low_before_highs"), false
	}
	lMid, okMid := mostExtremeBetween(lows, hPrev.Index, hLast.Index, decimal.Decimal.LessThan)
	if !okMid {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.no_low_between_highs"), false
	}
	if !lMid.Price.LessThan(lBefore.Price) {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.structure_not_downtrend"), false
	}

	lAfter, okAfter := mostRecentAfter(lows, hLast.Index)
	if !okAfter {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.no_drop_after_lower_high"), false
	}
	if lAfter.Index+1 >= len(candles) {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.no_rally_yet"), false
	}

	testIdx, testHigh := pullbackExtreme(candles, lAfter.Index+1,
		func(c types.Candle) decimal.Decimal { return c.High }, decimal.Decimal.GreaterThan)

	if !testHigh.LessThan(hLast.Price) {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.rally_broke_structure"), false
	}

	trendlinePrice := trendlineValueAt(hPrev, hLast, testIdx)
	dist := relDist(testHigh, trendlinePrice)
	if dist.GreaterThan(tolerance) {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.rally_not_near_trendline"), false
	}

	cur := candles[len(candles)-1]
	if !testHigh.IsPositive() {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.non_positive_close"), false
	}
	rejection := testHigh.Sub(cur.Close).Div(testHigh)
	if rejection.LessThan(bounceConfirm) {
		return types.Signal{}, types.Msg("modules.trendline_pullback.reason.rejection_not_confirmed"), false
	}

	raw := map[string]any{
		"pattern":                 "downtrend_rally",
		"trend_high_prev":         pivotRaw(hPrev),
		"trend_high_last":         pivotRaw(hLast),
		"trend_low_before":        pivotRaw(lBefore),
		"trend_low_mid":           pivotRaw(lMid),
		"leg_low_after":           pivotRaw(lAfter),
		"tested_high":             map[string]any{"index": testIdx, "price": testHigh.String(), "time": candles[testIdx].OpenTime.Format(time.RFC3339)},
		"trendline_price_at_test": trendlinePrice.String(),
		"distance_ratio":          dist.String(),
		"rejection_ratio":         rejection.String(),
	}

	return types.Signal{
		Module:     ModuleName,
		Direction:  types.DirectionShort,
		Confidence: confidenceFor(dist, tolerance, rejection, bounceConfirm),
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason: types.Msg("modules.trendline_pullback.reason.downtrend_rally_confirmed",
			"test_high", testHigh.String(), "trend_high", hLast.Price.String()),
		Raw: raw,
	}, types.Message{}, true
}
