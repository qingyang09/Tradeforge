// Package volumebreakout 实现 volume_breakout 信号模块。
//
// 逻辑：把最新一根 K 线的成交量与之前 window 根的均量相比，
// 倍数超过阈值即认为出现放量，方向由 K 线自身决定（收阳看多、收阴看空）。
//
// 均量刻意不含最新一根：把当前这根算进均值会稀释它自己的倍数，
// window 越小稀释越严重，会让阈值的实际含义随参数漂移。
package volumebreakout

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName 是该模块在策略配置中的标识。
const ModuleName = "volume_breakout"

// 方向判定来源。
const (
	// SourceCandle 用 K 线收盘价相对开盘价的涨跌判定方向。
	SourceCandle = "candle"
	// SourceTaker 用主动买卖量的净差判定方向，需要数据源提供 TakerBuyVolume。
	SourceTaker = "taker"
)

// Module 实现 volume_breakout 信号模块。零值可用。
type Module struct{}

// New 返回模块实例。
func New() *Module { return &Module{} }

// Name 实现 modules.SignalModule。
func (m *Module) Name() string { return ModuleName }

// Description 实现 modules.SignalModule。
func (m *Module) Description() string {
	return "计算最新一根 K 线成交量相对近期均量的倍数，倍数超过阈值时输出信号，方向由 K 线涨跌或主动买卖量净差决定。"
}

// RequiredParams 实现 modules.SignalModule。
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

// Evaluate 实现 modules.SignalModule。
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

	// 需要 window 根做基准 + 1 根当前。
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

	// 均量为零通常意味着这段行情根本没有成交（停牌、数据缺口），
	// 此时任何倍数都是无穷大，不能当成有效放量。
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

	// 实体占比过滤：长影线小实体的放量往往是双向厮杀，方向不明确。
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

// direction 按配置的来源判定方向，同时返回一句可读的判定依据。
func direction(c types.Candle, source string) (types.Direction, string) {
	switch source {
	case SourceTaker:
		if !c.Volume.IsPositive() {
			return types.DirectionNeutral, "成交量为零"
		}
		buy, sell := c.TakerBuyVolume, c.TakerSellVolume()
		// 数据源未提供主动买入量时 TakerBuyVolume 为零，此时净差恒为负，
		// 会造出一个纯属虚构的看空信号。必须显式识别这种情况。
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

// confidence 把放量倍数映射到 [0.5, 0.95]。
//
// 刚好达到阈值给 0.5，达到阈值两倍时接近上限，之后增长趋缓：
// 成交量是长尾分布，10 倍和 20 倍的信息量差别远小于 2 倍和 4 倍。
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
