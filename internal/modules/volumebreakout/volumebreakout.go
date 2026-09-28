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
func (m *Module) Description() string {
	return "计算最新一根 K 线成交量相对近期均量的倍数，倍数超过阈值时输出信号，方向由 K 线涨跌或主动买卖量净差决定。"
}

// RequiredParams implements modules.SignalModule.
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "window", Type: types.ParamInt, Default: 20,
			Min: types.F(5), Max: types.F(500),
			Description: "均量窗口，用于计算基准成交量的 K 线根数（不含最新一根）。",
		},
		{
			Name: "multiplier", Type: types.ParamFloat, Default: 2.0,
			Min: types.F(1.0), Max: types.F(20.0),
			Description: "放量倍数阈值：最新成交量达到均量的该倍数才触发信号。",
		},
		{
			Name: "direction_source", Type: types.ParamString, Default: SourceCandle,
			Enum:        []string{SourceCandle, SourceTaker},
			Description: "方向判定来源：candle 按 K 线收阳/收阴，taker 按主动买卖量净差。",
		},
		{
			Name: "min_body_ratio", Type: types.ParamFloat, Default: 0.0,
			Min: types.F(0), Max: types.F(1),
			Description: "最小实体占比：K 线实体长度与全幅之比低于该值时视为方向不明，输出中性。0 表示不过滤。",
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
			fmt.Sprintf("K 线不足：需要至少 %d 根，实际 %d 根", window+1, len(md.Candles)),
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
		s := types.NeutralSignal(ModuleName, md.Symbol, "基准窗口内均量为零，无法计算放量倍数", cur.CloseTime)
		s.Price, s.Raw = cur.Close, raw
		return s, nil
	}

	ratio := cur.Volume.Div(avg)
	raw["ratio"] = ratio.InexactFloat64()

	if ratio.LessThan(decimal.NewFromFloat(multiplier)) {
		s := types.NeutralSignal(ModuleName, md.Symbol,
			fmt.Sprintf("成交量为均量的 %.2f 倍，未达到 %.2f 倍阈值", ratio.InexactFloat64(), multiplier),
			cur.CloseTime)
		s.Price, s.Raw = cur.Close, raw
		return s, nil
	}

	// Body-ratio filter: a volume surge on a candle with long wicks and a tiny
	// body is usually a two-way fight, so direction is unclear.
	if minBody.IsPositive() {
		rng := cur.Range()
		if !rng.IsPositive() {
			s := types.NeutralSignal(ModuleName, md.Symbol, "K 线全幅为零，无法判定方向", cur.CloseTime)
			s.Price, s.Raw = cur.Close, raw
			return s, nil
		}
		bodyRatio := cur.Close.Sub(cur.Open).Abs().Div(rng)
		raw["body_ratio"] = bodyRatio.InexactFloat64()
		if bodyRatio.LessThan(minBody) {
			s := types.NeutralSignal(ModuleName, md.Symbol,
				fmt.Sprintf("放量 %.2f 倍，但实体占比 %.2f 低于 %.2f，方向不明",
					ratio.InexactFloat64(), bodyRatio.InexactFloat64(), minBody.InexactFloat64()),
				cur.CloseTime)
			s.Price, s.Raw = cur.Close, raw
			return s, nil
		}
	}

	dir, dirReason := direction(cur, source)
	raw["direction_basis"] = dirReason
	if dir == types.DirectionNeutral {
		s := types.NeutralSignal(ModuleName, md.Symbol,
			fmt.Sprintf("放量 %.2f 倍，但%s，方向不明", ratio.InexactFloat64(), dirReason), cur.CloseTime)
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
		Reason: fmt.Sprintf("成交量为近 %d 根均量的 %.2f 倍（阈值 %.2f 倍），%s",
			window, ratio.InexactFloat64(), multiplier, dirReason),
		Raw: raw,
	}, nil
}

// direction determines direction using the configured source, returning a
// human-readable reason for the determination alongside it.
func direction(c types.Candle, source string) (types.Direction, string) {
	switch source {
	case SourceTaker:
		if !c.Volume.IsPositive() {
			return types.DirectionNeutral, "成交量为零"
		}
		buy, sell := c.TakerBuyVolume, c.TakerSellVolume()
		// When the data source doesn't supply taker buy volume, TakerBuyVolume
		// is zero, making the net difference always negative — which would
		// fabricate a purely artificial short signal. This case must be
		// detected explicitly.
		if buy.IsZero() {
			return types.DirectionNeutral, "数据源未提供主动买入量"
		}
		switch {
		case buy.GreaterThan(sell):
			return types.DirectionLong, fmt.Sprintf("主动买入量 %s 大于主动卖出量 %s", buy.String(), sell.String())
		case sell.GreaterThan(buy):
			return types.DirectionShort, fmt.Sprintf("主动卖出量 %s 大于主动买入量 %s", sell.String(), buy.String())
		default:
			return types.DirectionNeutral, "主动买卖量相等"
		}

	default: // SourceCandle
		switch {
		case c.Close.GreaterThan(c.Open):
			return types.DirectionLong, "该 K 线收阳"
		case c.Close.LessThan(c.Open):
			return types.DirectionShort, "该 K 线收阴"
		default:
			return types.DirectionNeutral, "收盘价等于开盘价"
		}
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
