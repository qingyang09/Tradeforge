// Package cvdorderflow 实现 cvd_orderflow 信号模块。
//
// CVD（Cumulative Volume Delta，累计成交量差）是主动买入量减主动卖出量的累计值，
// 反映"是买方还是卖方在主动成交"。模块检测两类现象：
//
//   - 失衡（imbalance）：窗口内净买卖差相对总成交量的占比超过阈值，
//     说明单边主动成交占据压倒性优势。
//   - 背离（divergence）：价格与 CVD 的变动方向相反，
//     即价格在涨但主动买盘在退（或反之）。背离时取 CVD 的方向作为信号方向。
//
// 订单流数据通过 FlowProvider 接口注入，便于后续替换为 Coinglass 等真实数据源。
package cvdorderflow

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName 是该模块在策略配置中的标识。
const ModuleName = "cvd_orderflow"

// 检测模式。
const (
	DetectBoth       = "both"
	DetectImbalance  = "imbalance"
	DetectDivergence = "divergence"
)

// 事件类型，写入 Signal.Raw["event"]。
const (
	eventImbalance  = "imbalance"
	eventDivergence = "divergence"
	eventNone       = "none"
)

// Module 实现 cvd_orderflow 信号模块。
type Module struct {
	provider FlowProvider
}

// New 用指定的订单流数据源构造模块。
func New(p FlowProvider) *Module {
	if p == nil {
		p = CandleFlowProvider{}
	}
	return &Module{provider: p}
}

// NewDefault 用 K 线自带的主动买入量作为数据源构造模块。
func NewDefault() *Module { return New(CandleFlowProvider{}) }

// Provider 返回当前使用的订单流数据源。
func (m *Module) Provider() FlowProvider { return m.provider }

// Name 实现 modules.SignalModule。
func (m *Module) Name() string { return ModuleName }

// Description 实现 modules.SignalModule。
func (m *Module) Description() string {
	return "计算累计成交量差（CVD），检测窗口内的主动买卖失衡，以及价格与 CVD 之间的背离。"
}

// RequiredParams 实现 modules.SignalModule。
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "window", Type: types.ParamInt, Default: 50,
			Min: types.F(10), Max: types.F(1000),
			Description: "计算窗口，参与 CVD 与失衡度统计的 K 线根数。",
		},
		{
			Name: "imbalance_threshold", Type: types.ParamFloat, Default: 0.25,
			Min: types.F(0.01), Max: types.F(1.0),
			Description: "失衡阈值：窗口内净买卖差占总成交量的比例超过该值即触发。取值 0~1。",
		},
		{
			Name: "divergence_threshold", Type: types.ParamFloat, Default: 0.15,
			Min: types.F(0.01), Max: types.F(1.0),
			Description: "背离阈值：CVD 变动的归一化幅度超过该值才认定为有效背离。",
		},
		{
			Name: "min_price_move", Type: types.ParamFloat, Default: 0.005,
			Min: types.F(0), Max: types.F(0.5),
			Description: "背离所需的最小价格变动比例，用于排除价格几乎没动时的伪背离。",
		},
		{
			Name: "detect", Type: types.ParamString, Default: DetectBoth,
			Enum:        []string{DetectBoth, DetectImbalance, DetectDivergence},
			Description: "检测模式：both 同时检测失衡与背离，或只检测其中一种。",
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
	imbThreshold := types.MustFloat(p, "imbalance_threshold")
	divThreshold := types.MustFloat(p, "divergence_threshold")
	minMove := types.MustFloat(p, "min_price_move")
	mode := types.MustString(p, "detect")

	if len(md.Candles) < window {
		return types.NeutralSignal(ModuleName, md.Symbol,
			fmt.Sprintf("K 线不足：需要至少 %d 根，实际 %d 根", window, len(md.Candles)),
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

	// 窗口内的净差与总量。CVD 本身是累计值，窗口内的"CVD 变动"就等于净差之和。
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
		s := types.NeutralSignal(ModuleName, md.Symbol, "窗口内总成交量为零，无法计算 CVD 失衡度", cur.CloseTime)
		s.Price, s.Raw = cur.Close, raw
		return s, nil
	}

	// 失衡度：净差 / 总量，落在 [-1, 1]。
	imbalance := netSum.Div(totalVol).InexactFloat64()
	raw["imbalance"] = imbalance

	// 价格变动：窗口首根开盘价到末根收盘价。
	first := win[0]
	var priceMove float64
	if first.Open.IsPositive() {
		priceMove = cur.Close.Sub(first.Open).Div(first.Open).InexactFloat64()
	}
	raw["price_move"] = priceMove

	// 背离优先于失衡：背离是"价格与订单流打架"，信息量高于单纯的单边占优。
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
		fmt.Sprintf("窗口内 CVD 失衡度 %.3f，价格变动 %.3f%%，未触发失衡或背离条件",
			imbalance, priceMove*100), cur.CloseTime)
	s.Price, s.Raw = cur.Close, raw
	return s, nil
}

// checkDivergence 检测价格与 CVD 的方向背离。
func (m *Module) checkDivergence(
	symbol string, cur types.Candle,
	imbalance, priceMove, divThreshold, minMove float64,
	raw map[string]any,
) (types.Signal, bool) {
	if abs(priceMove) < minMove || abs(imbalance) < divThreshold {
		return types.Signal{}, false
	}
	// 同向就不是背离。
	if (priceMove > 0) == (imbalance > 0) {
		return types.Signal{}, false
	}

	// 背离时跟随 CVD 的方向：订单流被认为先于价格反映真实供需。
	dir := types.DirectionShort
	desc := "价格上涨但主动买盘净流出"
	if imbalance > 0 {
		dir = types.DirectionLong
		desc = "价格下跌但主动买盘净流入"
	}

	raw["event"] = eventDivergence
	return types.Signal{
		Module:     ModuleName,
		Symbol:     symbol,
		Direction:  dir,
		Confidence: divergenceConfidence(imbalance, divThreshold),
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason: fmt.Sprintf("CVD 背离：%s（价格变动 %.2f%%，CVD 失衡度 %.3f，阈值 %.3f）",
			desc, priceMove*100, imbalance, divThreshold),
		Raw: raw,
	}, true
}

// checkImbalance 检测窗口内的单边主动成交失衡。
func (m *Module) checkImbalance(
	symbol string, cur types.Candle,
	imbalance, threshold float64,
	raw map[string]any,
) (types.Signal, bool) {
	if abs(imbalance) < threshold {
		return types.Signal{}, false
	}

	dir, desc := types.DirectionLong, "主动买入"
	if imbalance < 0 {
		dir, desc = types.DirectionShort, "主动卖出"
	}

	raw["event"] = eventImbalance
	return types.Signal{
		Module:     ModuleName,
		Symbol:     symbol,
		Direction:  dir,
		Confidence: imbalanceConfidence(imbalance, threshold),
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason: fmt.Sprintf("CVD 失衡：窗口内%s净占总成交量的 %.1f%%（阈值 %.1f%%）",
			desc, abs(imbalance)*100, threshold*100),
		Raw: raw,
	}, true
}

// imbalanceConfidence 把失衡度映射到 [0.5, 0.95]。
// 失衡度上限是 1（全部为单边成交），因此用它到阈值的剩余空间做线性插值。
func imbalanceConfidence(imbalance, threshold float64) float64 {
	a, t := abs(imbalance), abs(threshold)
	if t >= 1 {
		return 0.95
	}
	c := 0.5 + 0.45*((a-t)/(1-t))
	return clamp(c, 0.5, 0.95)
}

// divergenceConfidence 与失衡同法，但基准略低：
// 背离信号在方向上更早，同时也更容易被后续行情证伪。
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
