// Package macdrsi implements the macd_rsi signal module.
//
// Combines two classic momentum indicators:
//   - MACD (the difference between a fast and slow EMA, plus its own EMA):
//     captures turns in trend momentum. The MACD line crossing above/below
//     the signal line counts as a bullish/bearish cross.
//   - RSI (Relative Strength Index, Wilder-smoothed): captures mean reversion
//     after overbought/oversold conditions. RSI crossing back out of an
//     extreme zone counts as a reversal.
//
// Three modes:
//   - macd_cross: only looks at MACD bullish/bearish crosses
//   - rsi_reversal: only looks at RSI crossing back from overbought/oversold
//   - confluence (default): requires that when a MACD cross happens, RSI is
//     not yet in the same-direction extreme zone, to avoid chasing an entry
//     when the move is already overextended
package macdrsi

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName is this module's identifier in strategy configs.
const ModuleName = "macd_rsi"

// Detection modes.
const (
	ModeMACDCross   = "macd_cross"
	ModeRSIReversal = "rsi_reversal"
	ModeConfluence  = "confluence"
)

// Event types, written to Signal.Raw["event"].
const (
	eventBullishCross    = "bullish_cross"
	eventBearishCross    = "bearish_cross"
	eventBullishReversal = "bullish_reversal"
	eventBearishReversal = "bearish_reversal"
	eventNone            = "none"
)

// Module implements the macd_rsi signal module. The zero value is usable.
type Module struct{}

// New returns a module instance.
func New() *Module { return &Module{} }

// Name implements modules.SignalModule.
func (m *Module) Name() string { return ModuleName }

// Description implements modules.SignalModule.
func (m *Module) Description() types.Message {
	return types.Msg("modules.macd_rsi.description")
}

// RequiredParams implements modules.SignalModule.
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "fast_period", Type: types.ParamInt, Default: 12,
			Min: types.F(2), Max: types.F(100),
			Description: types.Msg("modules.macd_rsi.param.fast_period"),
		},
		{
			Name: "slow_period", Type: types.ParamInt, Default: 26,
			Min: types.F(3), Max: types.F(200),
			Description: types.Msg("modules.macd_rsi.param.slow_period"),
		},
		{
			Name: "signal_period", Type: types.ParamInt, Default: 9,
			Min: types.F(2), Max: types.F(50),
			Description: types.Msg("modules.macd_rsi.param.signal_period"),
		},
		{
			Name: "rsi_period", Type: types.ParamInt, Default: 14,
			Min: types.F(2), Max: types.F(100),
			Description: types.Msg("modules.macd_rsi.param.rsi_period"),
		},
		{
			Name: "rsi_overbought", Type: types.ParamFloat, Default: 70.0,
			Min: types.F(50), Max: types.F(95),
			Description: types.Msg("modules.macd_rsi.param.rsi_overbought"),
		},
		{
			Name: "rsi_oversold", Type: types.ParamFloat, Default: 30.0,
			Min: types.F(5), Max: types.F(50),
			Description: types.Msg("modules.macd_rsi.param.rsi_oversold"),
		},
		{
			Name: "mode", Type: types.ParamString, Default: ModeConfluence,
			Enum:        []string{ModeMACDCross, ModeRSIReversal, ModeConfluence},
			Description: types.Msg("modules.macd_rsi.param.mode"),
		},
	}
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

	fastPeriod := types.MustInt(p, "fast_period")
	slowPeriod := types.MustInt(p, "slow_period")
	signalPeriod := types.MustInt(p, "signal_period")
	rsiPeriod := types.MustInt(p, "rsi_period")
	overbought := types.MustFloat(p, "rsi_overbought")
	oversold := types.MustFloat(p, "rsi_oversold")
	mode := types.MustString(p, "mode")

	if slowPeriod <= fastPeriod {
		return types.Signal{}, &types.ParamError{
			Module: ModuleName, Param: "slow_period",
			Reason: types.Msg("modules.macdrsi.slow_period_too_small", "fast_period", fastPeriod), Given: slowPeriod,
		}
	}
	if oversold >= overbought {
		return types.Signal{}, &types.ParamError{
			Module: ModuleName, Param: "rsi_oversold",
			Reason: types.Msg("modules.macdrsi.oversold_too_large", "rsi_overbought", overbought), Given: oversold,
		}
	}

	neutral := func(reason types.Message) types.Signal {
		s := types.NeutralSignal(ModuleName, md.Symbol, reason, md.Time())
		if last, ok := md.Last(); ok {
			s.Price = last.Close
		}
		return s
	}

	// Need at least two valid signal-line/RSI values to detect a cross (this candle vs. the previous one).
	minForMACD := slowPeriod + signalPeriod
	minForRSI := rsiPeriod + 2
	minCandles := minForMACD
	if minForRSI > minCandles {
		minCandles = minForRSI
	}
	if len(md.Candles) < minCandles {
		return neutral(types.Msg("modules.macd_rsi.reason.insufficient_candles",
			"required", minCandles, "actual", len(md.Candles))), nil
	}

	closes := make([]decimal.Decimal, len(md.Candles))
	for i, c := range md.Candles {
		closes[i] = c.Close
	}

	fastEMA, _ := emaSeries(closes, fastPeriod)
	slowEMA, slowFrom := emaSeries(closes, slowPeriod)

	macdLine := make([]decimal.Decimal, len(closes))
	for i := slowFrom; i < len(closes); i++ {
		macdLine[i] = fastEMA[i].Sub(slowEMA[i])
	}

	sigEMA, sigFromRel := emaSeries(macdLine[slowFrom:], signalPeriod)
	signalFrom := slowFrom + sigFromRel
	signalLine := make([]decimal.Decimal, len(closes))
	for i, v := range sigEMA {
		signalLine[slowFrom+i] = v
	}

	rsi, _ := rsiSeries(closes, rsiPeriod)

	i := len(closes) - 1
	prev := i - 1

	cur := md.Candles[i]
	raw := map[string]any{"mode": mode, "event": eventNone}

	macdNow, macdPrev := macdLine[i], macdLine[prev]
	sigNow, sigPrev := signalLine[i], signalLine[prev]
	rsiNow, rsiPrev := rsi[i], rsi[prev]

	raw["macd"] = macdNow.InexactFloat64()
	raw["macd_signal"] = sigNow.InexactFloat64()
	raw["macd_histogram"] = macdNow.Sub(sigNow).InexactFloat64()
	raw["rsi"] = rsiNow
	raw["rsi_prev"] = rsiPrev

	bullishCross := macdPrev.LessThanOrEqual(sigPrev) && macdNow.GreaterThan(sigNow)
	bearishCross := macdPrev.GreaterThanOrEqual(sigPrev) && macdNow.LessThan(sigNow)
	bullishReversal := rsiPrev < oversold && rsiNow >= oversold
	bearishReversal := rsiPrev > overbought && rsiNow <= overbought

	dir, event, triggered := decideDirection(mode,
		bullishCross, bearishCross, bullishReversal, bearishReversal, rsiNow, oversold, overbought)
	if !triggered {
		s := neutral(types.Msg("modules.macd_rsi.reason.no_trigger",
			"mode", mode,
			"macd", fmt.Sprintf("%.4f", macdNow.InexactFloat64()),
			"macd_signal", fmt.Sprintf("%.4f", sigNow.InexactFloat64()),
			"rsi", fmt.Sprintf("%.1f", rsiNow)))
		s.Raw = raw
		return s, nil
	}

	switch event {
	case eventBullishCross, eventBearishCross:
		macdNorm := avgAbsMACD(macdLine, signalFrom, i)
		return crossSignal(md.Symbol, cur, dir, event, macdNow, sigNow, macdNorm, raw), nil
	default: // eventBullishReversal, eventBearishReversal
		return reversalSignal(md.Symbol, cur, dir, event, rsiPrev, oversold, overbought, raw), nil
	}
}

// decideDirection consolidates the trigger conditions for all three modes
// into one pure function, so it's easy to test in isolation — in particular
// the confluence mode's filter ("is RSI already in the same-direction
// extreme zone when the cross happens") can be verified directly without
// relying on carefully constructed market data.
func decideDirection(
	mode string,
	bullishCross, bearishCross, bullishReversal, bearishReversal bool,
	rsiNow, oversold, overbought float64,
) (dir types.Direction, event string, triggered bool) {
	switch mode {
	case ModeMACDCross:
		switch {
		case bullishCross:
			return types.DirectionLong, eventBullishCross, true
		case bearishCross:
			return types.DirectionShort, eventBearishCross, true
		}

	case ModeRSIReversal:
		switch {
		case bullishReversal:
			return types.DirectionLong, eventBullishReversal, true
		case bearishReversal:
			return types.DirectionShort, eventBearishReversal, true
		}

	case ModeConfluence:
		switch {
		case bullishCross && rsiNow < overbought:
			return types.DirectionLong, eventBullishCross, true
		case bearishCross && rsiNow > oversold:
			return types.DirectionShort, eventBearishCross, true
		}
	}
	return types.DirectionNeutral, eventNone, false
}

// emaSeries computes the exponential moving average, seeding the starting
// point with a simple average.
//
// The returned series is the same length as values; values in [0, validFrom)
// are undefined (zero value) and the caller must not read them. If values is
// shorter than period, validFrom equals len(values) (i.e. the whole series is unusable).
func emaSeries(values []decimal.Decimal, period int) (series []decimal.Decimal, validFrom int) {
	n := len(values)
	series = make([]decimal.Decimal, n)
	if n < period {
		return series, n
	}

	sum := decimal.Zero
	for i := 0; i < period; i++ {
		sum = sum.Add(values[i])
	}
	series[period-1] = sum.Div(decimal.NewFromInt(int64(period)))

	k := decimal.NewFromInt(2).Div(decimal.NewFromInt(int64(period + 1)))
	oneMinusK := decimal.NewFromInt(1).Sub(k)
	for i := period; i < n; i++ {
		series[i] = values[i].Mul(k).Add(series[i-1].Mul(oneMinusK))
	}
	return series, period - 1
}

// rsiSeries computes RSI using Wilder's smoothing method, ranging over [0, 100].
//
// RSI is a dimensionless oscillator: internally it uses decimal arithmetic
// for precision (the gains/losses are ultimately price differences), and is
// exposed externally as float64, consistent with how confidence, Sharpe, and
// other statistics are handled.
func rsiSeries(closes []decimal.Decimal, period int) (rsi []float64, validFrom int) {
	n := len(closes)
	rsi = make([]float64, n)
	if n < period+1 {
		return rsi, n
	}

	gains := make([]decimal.Decimal, n)
	losses := make([]decimal.Decimal, n)
	for i := 1; i < n; i++ {
		delta := closes[i].Sub(closes[i-1])
		if delta.IsPositive() {
			gains[i] = delta
		} else {
			losses[i] = delta.Neg()
		}
	}

	pd := decimal.NewFromInt(int64(period))
	sumGain, sumLoss := decimal.Zero, decimal.Zero
	for i := 1; i <= period; i++ {
		sumGain = sumGain.Add(gains[i])
		sumLoss = sumLoss.Add(losses[i])
	}
	avgGain := sumGain.Div(pd)
	avgLoss := sumLoss.Div(pd)
	rsi[period] = rsiFromAvg(avgGain, avgLoss)

	pMinus1 := decimal.NewFromInt(int64(period - 1))
	for i := period + 1; i < n; i++ {
		avgGain = avgGain.Mul(pMinus1).Add(gains[i]).Div(pd)
		avgLoss = avgLoss.Mul(pMinus1).Add(losses[i]).Div(pd)
		rsi[i] = rsiFromAvg(avgGain, avgLoss)
	}
	return rsi, period
}

func rsiFromAvg(avgGain, avgLoss decimal.Decimal) float64 {
	switch {
	case avgLoss.IsZero() && avgGain.IsZero():
		return 50 // no movement at all, treated as neutral
	case avgLoss.IsZero():
		return 100
	case avgGain.IsZero():
		return 0
	}
	rs := avgGain.Div(avgLoss)
	hundred := decimal.NewFromInt(100)
	return hundred.Sub(hundred.Div(rs.Add(decimal.NewFromInt(1)))).InexactFloat64()
}

// avgAbsMACD computes the average absolute value of the MACD line over
// [from, to] (at most the most recent 50 candles), used as the normalization
// scale for confidence so the same parameter set behaves consistently across
// symbols with different price magnitudes.
func avgAbsMACD(macdLine []decimal.Decimal, from, to int) decimal.Decimal {
	if to < from {
		return decimal.Zero
	}
	start := from
	if to-start > 50 {
		start = to - 50
	}
	sum := decimal.Zero
	count := 0
	for i := start; i <= to; i++ {
		sum = sum.Add(macdLine[i].Abs())
		count++
	}
	if count == 0 {
		return decimal.Zero
	}
	return sum.Div(decimal.NewFromInt(int64(count)))
}

// crossSignal builds a MACD bullish/bearish cross signal.
//
// Confidence is normalized from the post-cross gap relative to the recent
// MACD magnitude: a bigger gap indicates a more decisive momentum shift.
func crossSignal(
	symbol string, cur types.Candle, dir types.Direction, event string,
	macd, signal, norm decimal.Decimal, raw map[string]any,
) types.Signal {
	raw["event"] = event
	gap := macd.Sub(signal).Abs()

	reasonKey := "modules.macd_rsi.reason.bullish_cross"
	if dir == types.DirectionShort {
		reasonKey = "modules.macd_rsi.reason.bearish_cross"
	}
	return types.Signal{
		Module:     ModuleName,
		Symbol:     symbol,
		Direction:  dir,
		Confidence: crossConfidence(gap, norm),
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason: types.Msg(reasonKey,
			"macd", fmt.Sprintf("%.4f", macd.InexactFloat64()),
			"signal", fmt.Sprintf("%.4f", signal.InexactFloat64())),
		Raw: raw,
	}
}

func crossConfidence(gap, norm decimal.Decimal) float64 {
	if !norm.IsPositive() {
		return 0.5
	}
	ratio := gap.Div(norm).InexactFloat64()
	return clamp(0.5+0.45*clamp(ratio, 0, 1), 0.5, 0.95)
}

// reversalSignal builds an RSI overbought/oversold reversal signal.
//
// Confidence is based on how deep RSI went into the extreme zone before
// crossing back: the deeper it went, the more informative the reversal.
func reversalSignal(
	symbol string, cur types.Candle, dir types.Direction, event string,
	extremeRSI, oversold, overbought float64, raw map[string]any,
) types.Signal {
	raw["event"] = event

	var depth, span float64
	reasonKey := "modules.macd_rsi.reason.bullish_reversal"
	if dir == types.DirectionShort {
		reasonKey = "modules.macd_rsi.reason.bearish_reversal"
		depth = extremeRSI - overbought
		span = 100 - overbought
	} else {
		depth = oversold - extremeRSI
		span = oversold
	}
	conf := 0.5
	if span > 0 {
		conf = clamp(0.5+0.45*clamp(depth/span, 0, 1), 0.5, 0.95)
	}

	return types.Signal{
		Module:     ModuleName,
		Symbol:     symbol,
		Direction:  dir,
		Confidence: conf,
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason:     types.Msg(reasonKey, "prev_rsi", fmt.Sprintf("%.1f", extremeRSI)),
		Raw:        raw,
	}
}

func clamp(v, lo, hi float64) float64 {
	switch {
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}
