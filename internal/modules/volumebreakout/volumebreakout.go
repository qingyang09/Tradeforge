// Package volumebreakout implements the volume_breakout signal module.
//
// Logic: compare the latest candle's volume against the average of the
// preceding `window` candles; a ratio above the threshold counts as a volume
// surge, and direction comes from the candle itself (bullish close -> long,
// bearish close -> short).
//
// The average deliberately excludes the latest candle: folding it into the
// average would dilute its own ratio, and the smaller window is, the worse
// the dilution — which would make the threshold's effective meaning drift
// with the parameter.
package volumebreakout

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName is this module's identifier in strategy configs.
const ModuleName = "volume_breakout"

// Direction-detection sources.
const (
	// SourceCandle determines direction from the candle's close vs. open.
	SourceCandle = "candle"
	// SourceTaker determines direction from the net difference between taker
	// buy/sell volume; requires the data source to supply TakerBuyVolume.
	SourceTaker = "taker"
)

// Module implements the volume_breakout signal module. The zero value is usable.
type Module struct{}

// New returns a module instance.
func New() *Module { return &Module{} }

// Name implements modules.SignalModule.
func (m *Module) Name() string { return ModuleName }

// Description implements modules.SignalModule.
func (m *Module) Description() types.Message {
	return types.Msg("modules.volume_breakout.description")
}

// RequiredParams implements modules.SignalModule.
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "window", Type: types.ParamInt, Default: 20,
			Min: types.F(5), Max: types.F(500),
			Description: types.Msg("modules.volume_breakout.param.window"),
		},
		{
			Name: "multiplier", Type: types.ParamFloat, Default: 2.0,
			Min: types.F(1.0), Max: types.F(20.0),
			Description: types.Msg("modules.volume_breakout.param.multiplier"),
		},
		{
			Name: "direction_source", Type: types.ParamString, Default: SourceCandle,
			Enum:        []string{SourceCandle, SourceTaker},
			Description: types.Msg("modules.volume_breakout.param.direction_source"),
		},
		{
			Name: "min_body_ratio", Type: types.ParamFloat, Default: 0.0,
			Min: types.F(0), Max: types.F(1),
			Description: types.Msg("modules.volume_breakout.param.min_body_ratio"),
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

	window := types.MustInt(p, "window")
	multiplier := types.MustFloat(p, "multiplier")
	source := types.MustString(p, "direction_source")
	minBody := decimal.NewFromFloat(types.MustFloat(p, "min_body_ratio"))

	// Needs `window` candles as the baseline plus 1 current candle.
	if len(md.Candles) < window+1 {
		return types.NeutralSignal(ModuleName, md.Symbol,
			types.Msg("modules.volume_breakout.reason.insufficient_candles",
				"required", window+1, "actual", len(md.Candles)),
			md.Time()), nil
	}

	cur := md.Candles[len(md.Candles)-1]
	base := md.Candles[len(md.Candles)-1-window : len(md.Candles)-1]

	sum := decimal.Zero
	for _, c := range base {
		sum = sum.Add(c.Volume)
	}
	avg := sum.Div(decimal.NewFromInt(int64(len(base))))

	raw := map[string]any{
		"current_volume":   cur.Volume.String(),
		"average_volume":   avg.String(),
		"window":           window,
		"multiplier":       multiplier,
		"direction_source": source,
	}

	// A zero average usually means there was no trading in this window at all
	// (a halt, a data gap). Any ratio computed against it is effectively
	// infinite, so it can't be treated as a genuine volume surge.
	if !avg.IsPositive() {
		s := types.NeutralSignal(ModuleName, md.Symbol,
			types.Msg("modules.volume_breakout.reason.zero_average_volume"), cur.CloseTime)
		s.Price, s.Raw = cur.Close, raw
		return s, nil
	}

	ratio := cur.Volume.Div(avg)
	raw["ratio"] = ratio.InexactFloat64()
	ratioStr := fmt.Sprintf("%.2f", ratio.InexactFloat64())

	if ratio.LessThan(decimal.NewFromFloat(multiplier)) {
		s := types.NeutralSignal(ModuleName, md.Symbol,
			types.Msg("modules.volume_breakout.reason.below_threshold",
				"ratio", ratioStr, "multiplier", fmt.Sprintf("%.2f", multiplier)),
			cur.CloseTime)
		s.Price, s.Raw = cur.Close, raw
		return s, nil
	}

	// Body-ratio filter: a volume surge on a candle with long wicks and a tiny
	// body is usually a two-way fight, so direction is unclear.
	if minBody.IsPositive() {
		rng := cur.Range()
		if !rng.IsPositive() {
			s := types.NeutralSignal(ModuleName, md.Symbol,
				types.Msg("modules.volume_breakout.reason.zero_range"), cur.CloseTime)
			s.Price, s.Raw = cur.Close, raw
			return s, nil
		}
		bodyRatio := cur.Close.Sub(cur.Open).Abs().Div(rng)
		raw["body_ratio"] = bodyRatio.InexactFloat64()
		if bodyRatio.LessThan(minBody) {
			s := types.NeutralSignal(ModuleName, md.Symbol,
				types.Msg("modules.volume_breakout.reason.ambiguous_body",
					"ratio", ratioStr,
					"body_ratio", fmt.Sprintf("%.2f", bodyRatio.InexactFloat64()),
					"min_body_ratio", fmt.Sprintf("%.2f", minBody.InexactFloat64())),
				cur.CloseTime)
			s.Price, s.Raw = cur.Close, raw
			return s, nil
		}
	}

	dir, basis, basisArgs := direction(cur, source)
	raw["direction_basis"] = basis
	if dir == types.DirectionNeutral {
		s := types.NeutralSignal(ModuleName, md.Symbol,
			directionUnclearMessage(basis, ratioStr), cur.CloseTime)
		s.Price, s.Raw = cur.Close, raw
		return s, nil
	}

	return types.Signal{
		Module:     ModuleName,
		Symbol:     md.Symbol,
		Direction:  dir,
		Confidence: confidence(ratio.InexactFloat64(), multiplier),
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason:     triggeredMessage(basis, window, ratioStr, fmt.Sprintf("%.2f", multiplier), basisArgs),
		Raw:        raw,
	}, nil
}

// Direction-determination bases: machine-readable tags identifying which
// concrete rule decided (or failed to decide) a signal's direction. Used
// both as an audit-friendly Raw value and to select the right catalog key
// for the final Reason message.
const (
	basisBullishCandle     = "bullish_candle"
	basisBearishCandle     = "bearish_candle"
	basisFlatClose         = "flat_close"
	basisZeroVolume        = "zero_volume"
	basisNoTakerData       = "no_taker_data"
	basisTakerBuyDominant  = "taker_buy_dominant"
	basisTakerSellDominant = "taker_sell_dominant"
	basisTakerBalanced     = "taker_balanced"
)

// direction determines direction using the configured source, returning a
// machine-readable basis tag (for Raw/audit and message selection) plus any
// extra values (e.g. taker buy/sell volume) the chosen message needs.
func direction(c types.Candle, source string) (types.Direction, string, map[string]any) {
	switch source {
	case SourceTaker:
		if !c.Volume.IsPositive() {
			return types.DirectionNeutral, basisZeroVolume, nil
		}
		buy, sell := c.TakerBuyVolume, c.TakerSellVolume()
		// When the data source doesn't supply taker buy volume, TakerBuyVolume
		// is zero, making the net difference always negative — which would
		// fabricate a purely artificial short signal. This case must be
		// detected explicitly.
		if buy.IsZero() {
			return types.DirectionNeutral, basisNoTakerData, nil
		}
		switch {
		case buy.GreaterThan(sell):
			return types.DirectionLong, basisTakerBuyDominant, map[string]any{"buy": buy.String(), "sell": sell.String()}
		case sell.GreaterThan(buy):
			return types.DirectionShort, basisTakerSellDominant, map[string]any{"buy": buy.String(), "sell": sell.String()}
		default:
			return types.DirectionNeutral, basisTakerBalanced, nil
		}

	default: // SourceCandle
		switch {
		case c.Close.GreaterThan(c.Open):
			return types.DirectionLong, basisBullishCandle, nil
		case c.Close.LessThan(c.Open):
			return types.DirectionShort, basisBearishCandle, nil
		default:
			return types.DirectionNeutral, basisFlatClose, nil
		}
	}
}

// directionUnclearMessage builds the Reason for a volume surge whose
// direction couldn't be determined, per the basis tag from direction().
func directionUnclearMessage(basis, ratio string) types.Message {
	switch basis {
	case basisZeroVolume:
		return types.Msg("modules.volume_breakout.reason.direction_unclear_zero_volume", "ratio", ratio)
	case basisNoTakerData:
		return types.Msg("modules.volume_breakout.reason.direction_unclear_no_taker_data", "ratio", ratio)
	case basisTakerBalanced:
		return types.Msg("modules.volume_breakout.reason.direction_unclear_taker_balanced", "ratio", ratio)
	default: // basisFlatClose
		return types.Msg("modules.volume_breakout.reason.direction_unclear_flat_close", "ratio", ratio)
	}
}

// triggeredMessage builds the Reason for a confirmed directional signal, per
// the basis tag from direction().
func triggeredMessage(basis string, window int, ratio, multiplier string, basisArgs map[string]any) types.Message {
	switch basis {
	case basisBearishCandle:
		return types.Msg("modules.volume_breakout.reason.triggered_bearish_candle",
			"window", window, "ratio", ratio, "multiplier", multiplier)
	case basisTakerBuyDominant:
		return types.Msg("modules.volume_breakout.reason.triggered_taker_buy_dominant",
			"window", window, "ratio", ratio, "multiplier", multiplier,
			"buy", basisArgs["buy"], "sell", basisArgs["sell"])
	case basisTakerSellDominant:
		return types.Msg("modules.volume_breakout.reason.triggered_taker_sell_dominant",
			"window", window, "ratio", ratio, "multiplier", multiplier,
			"buy", basisArgs["buy"], "sell", basisArgs["sell"])
	default: // basisBullishCandle
		return types.Msg("modules.volume_breakout.reason.triggered_bullish_candle",
			"window", window, "ratio", ratio, "multiplier", multiplier)
	}
}

// confidence maps the volume ratio into [0.5, 0.95].
//
// Just reaching the threshold gives 0.5, reaching twice the threshold gets
// close to the upper bound, and growth tapers off after that: volume follows
// a long-tailed distribution, so the informational difference between 10x
// and 20x is much smaller than between 2x and 4x.
func confidence(ratio, threshold float64) float64 {
	if threshold <= 0 {
		return 0.5
	}
	excess := (ratio - threshold) / threshold
	if excess < 0 {
		excess = 0
	}
	c := 0.5 + 0.45*(excess/(excess+1))
	if c > 0.95 {
		c = 0.95
	}
	return c
}
