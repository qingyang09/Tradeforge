// Package cvdorderflow implements the cvd_orderflow signal module.
//
// CVD (Cumulative Volume Delta) is the running sum of taker buy volume minus
// taker sell volume, reflecting "whether buyers or sellers are the aggressor".
// The module detects two phenomena:
//
//   - Imbalance: the net buy/sell difference within the window, as a share of
//     total volume, exceeds a threshold — indicating one side of aggressive
//     order flow overwhelmingly dominates.
//   - Divergence: price and CVD move in opposite directions, i.e. price is
//     rising while buy-side order flow is retreating (or vice versa). On
//     divergence, the signal direction follows CVD's direction.
//
// Order-flow data is injected via the FlowProvider interface, so it can later
// be swapped for a real data source such as Coinglass.
package cvdorderflow

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName is this module's identifier in strategy configs.
const ModuleName = "cvd_orderflow"

// Detection modes.
const (
	DetectBoth       = "both"
	DetectImbalance  = "imbalance"
	DetectDivergence = "divergence"
)

// Event types, written to Signal.Raw["event"].
const (
	eventImbalance  = "imbalance"
	eventDivergence = "divergence"
	eventNone       = "none"
)

// Module implements the cvd_orderflow signal module.
type Module struct {
	provider FlowProvider
}

// New builds the module with the given order-flow data source.
func New(p FlowProvider) *Module {
	if p == nil {
		p = CandleFlowProvider{}
	}
	return &Module{provider: p}
}

// NewDefault builds the module using the candle's own taker buy volume as the data source.
func NewDefault() *Module { return New(CandleFlowProvider{}) }

// Provider returns the order-flow data source currently in use.
func (m *Module) Provider() FlowProvider { return m.provider }

// Name implements modules.SignalModule.
func (m *Module) Name() string { return ModuleName }

// Description implements modules.SignalModule.
func (m *Module) Description() types.Message {
	return types.Msg("modules.cvd_orderflow.description")
}

// RequiredParams implements modules.SignalModule.
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "window", Type: types.ParamInt, Default: 50,
			Min: types.F(10), Max: types.F(1000),
			Description: types.Msg("modules.cvd_orderflow.param.window"),
		},
		{
			Name: "imbalance_threshold", Type: types.ParamFloat, Default: 0.25,
			Min: types.F(0.01), Max: types.F(1.0),
			Description: types.Msg("modules.cvd_orderflow.param.imbalance_threshold"),
		},
		{
			Name: "divergence_threshold", Type: types.ParamFloat, Default: 0.15,
			Min: types.F(0.01), Max: types.F(1.0),
			Description: types.Msg("modules.cvd_orderflow.param.divergence_threshold"),
		},
		{
			Name: "min_price_move", Type: types.ParamFloat, Default: 0.005,
			Min: types.F(0), Max: types.F(0.5),
			Description: types.Msg("modules.cvd_orderflow.param.min_price_move"),
		},
		{
			Name: "detect", Type: types.ParamString, Default: DetectBoth,
			Enum:        []string{DetectBoth, DetectImbalance, DetectDivergence},
			Description: types.Msg("modules.cvd_orderflow.param.detect"),
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
	imbThreshold := types.MustFloat(p, "imbalance_threshold")
	divThreshold := types.MustFloat(p, "divergence_threshold")
	minMove := types.MustFloat(p, "min_price_move")
	mode := types.MustString(p, "detect")

	if len(md.Candles) < window {
		return types.NeutralSignal(ModuleName, md.Symbol,
			types.Msg("modules.cvd_orderflow.reason.insufficient_candles",
				"required", window, "actual", len(md.Candles)),
			md.Time()), nil
	}

	deltas, err := m.provider.Deltas(ctx, md)
	if err != nil {
		return types.Signal{}, fmt.Errorf("%s：读取订单流数据失败：%w", ModuleName, err)
	}
	if len(deltas) != len(md.Candles) {
		return types.Signal{}, fmt.Errorf("%s：订单流数据源返回 %d 条，与 %d 根 K 线不匹配",
			ModuleName, len(deltas), len(md.Candles))
	}

	win := md.Candles[len(md.Candles)-window:]
	winDeltas := deltas[len(deltas)-window:]
	cur := win[len(win)-1]

	// Net difference and total volume within the window. CVD itself is a
	// running total, so the "CVD change" within the window is just the sum of
	// the net differences.
	netSum, totalVol := decimal.Zero, decimal.Zero
	cvd := decimal.Zero
	cvdSeries := make([]string, 0, window)
	for _, d := range winDeltas {
		netSum = netSum.Add(d.Net)
		totalVol = totalVol.Add(d.Total)
		cvd = cvd.Add(d.Net)
		cvdSeries = append(cvdSeries, cvd.String())
	}

	raw := map[string]any{
		"provider":       m.provider.Name(),
		"is_synthetic":   IsSynthetic(m.provider),
		"window":         window,
		"cvd_change":     netSum.String(),
		"window_volume":  totalVol.String(),
		"event":          eventNone,
		"cvd_series_end": lastN(cvdSeries, 10),
	}

	if !totalVol.IsPositive() {
		s := types.NeutralSignal(ModuleName, md.Symbol,
			types.Msg("modules.cvd_orderflow.reason.zero_volume"), cur.CloseTime)
		s.Price, s.Raw = cur.Close, raw
		return s, nil
	}

	// Imbalance: net difference / total volume, falls in [-1, 1].
	imbalance := netSum.Div(totalVol).InexactFloat64()
	raw["imbalance"] = imbalance

	// Price move: from the window's first open to the last close.
	first := win[0]
	var priceMove float64
	if first.Open.IsPositive() {
		priceMove = cur.Close.Sub(first.Open).Div(first.Open).InexactFloat64()
	}
	raw["price_move"] = priceMove

	// Divergence takes priority over imbalance: divergence means "price and
	// order flow are fighting each other", which carries more information
	// than plain one-sided dominance.
	if mode == DetectBoth || mode == DetectDivergence {
		if sig, ok := m.checkDivergence(md.Symbol, cur, imbalance, priceMove, divThreshold, minMove, raw); ok {
			return sig, nil
		}
	}
	if mode == DetectBoth || mode == DetectImbalance {
		if sig, ok := m.checkImbalance(md.Symbol, cur, imbalance, imbThreshold, raw); ok {
			return sig, nil
		}
	}

	s := types.NeutralSignal(ModuleName, md.Symbol,
		types.Msg("modules.cvd_orderflow.reason.no_trigger",
			"imbalance", fmt.Sprintf("%.3f", imbalance),
			"price_move", fmt.Sprintf("%.3f", priceMove*100)),
		cur.CloseTime)
	s.Price, s.Raw = cur.Close, raw
	return s, nil
}

// checkDivergence detects a directional divergence between price and CVD.
func (m *Module) checkDivergence(
	symbol string, cur types.Candle,
	imbalance, priceMove, divThreshold, minMove float64,
	raw map[string]any,
) (types.Signal, bool) {
	if abs(priceMove) < minMove || abs(imbalance) < divThreshold {
		return types.Signal{}, false
	}
	// Same direction means it's not a divergence.
	if (priceMove > 0) == (imbalance > 0) {
		return types.Signal{}, false
	}

	// On divergence, follow CVD's direction: order flow is assumed to reflect
	// real supply/demand ahead of price.
	dir := types.DirectionShort
	reasonKey := "modules.cvd_orderflow.reason.divergence_bearish"
	if imbalance > 0 {
		dir = types.DirectionLong
		reasonKey = "modules.cvd_orderflow.reason.divergence_bullish"
	}

	raw["event"] = eventDivergence
	return types.Signal{
		Module:     ModuleName,
		Symbol:     symbol,
		Direction:  dir,
		Confidence: divergenceConfidence(imbalance, divThreshold),
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason: types.Msg(reasonKey,
			"price_move", fmt.Sprintf("%.2f", priceMove*100),
			"imbalance", fmt.Sprintf("%.3f", imbalance),
			"threshold", fmt.Sprintf("%.3f", divThreshold)),
		Raw: raw,
	}, true
}

// checkImbalance detects a one-sided aggressive-order-flow imbalance within the window.
func (m *Module) checkImbalance(
	symbol string, cur types.Candle,
	imbalance, threshold float64,
	raw map[string]any,
) (types.Signal, bool) {
	if abs(imbalance) < threshold {
		return types.Signal{}, false
	}

	dir, reasonKey := types.DirectionLong, "modules.cvd_orderflow.reason.imbalance_buy"
	if imbalance < 0 {
		dir, reasonKey = types.DirectionShort, "modules.cvd_orderflow.reason.imbalance_sell"
	}

	raw["event"] = eventImbalance
	return types.Signal{
		Module:     ModuleName,
		Symbol:     symbol,
		Direction:  dir,
		Confidence: imbalanceConfidence(imbalance, threshold),
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason: types.Msg(reasonKey,
			"pct", fmt.Sprintf("%.1f", abs(imbalance)*100),
			"threshold_pct", fmt.Sprintf("%.1f", threshold*100)),
		Raw: raw,
	}, true
}

// imbalanceConfidence maps the imbalance ratio into [0.5, 0.95].
// The imbalance ratio's upper bound is 1 (entirely one-sided), so we linearly
// interpolate over the remaining space between it and the threshold.
func imbalanceConfidence(imbalance, threshold float64) float64 {
	a, t := abs(imbalance), abs(threshold)
	if t >= 1 {
		return 0.95
	}
	c := 0.5 + 0.45*((a-t)/(1-t))
	return clamp(c, 0.5, 0.95)
}

// divergenceConfidence uses the same method as imbalance but with a slightly
// lower baseline: divergence signals fire earlier in a directional move, but
// are also more easily invalidated by subsequent price action.
func divergenceConfidence(imbalance, threshold float64) float64 {
	a, t := abs(imbalance), abs(threshold)
	if t >= 1 {
		return 0.85
	}
	c := 0.45 + 0.4*((a-t)/(1-t))
	return clamp(c, 0.45, 0.85)
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
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

func lastN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
