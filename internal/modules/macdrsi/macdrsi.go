// Package macdrsi 实现 macd_rsi 信号模块。
//
// 结合两个经典动量指标：
//   - MACD（快慢 EMA 之差与其自身的 EMA）：捕捉趋势动量的转向，
//     MACD 线上穿/下穿信号线视为金叉/死叉。
//   - RSI（相对强弱指数，Wilder 平滑）：捕捉超买超卖后的回归，
//     RSI 从极值区间穿回视为反转。
//
// 三种模式：
//   - macd_cross：只看 MACD 金叉/死叉
//   - rsi_reversal：只看 RSI 从超买/超卖区间穿回
//   - confluence（默认）：要求 MACD 金叉/死叉发生时 RSI 尚未处于同向的极值区间，
//     避免在行情已经透支的位置追单
package macdrsi

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName 是该模块在策略配置中的标识。
const ModuleName = "macd_rsi"

// 检测模式。
const (
	ModeMACDCross   = "macd_cross"
	ModeRSIReversal = "rsi_reversal"
	ModeConfluence  = "confluence"
)

// 事件类型，写入 Signal.Raw["event"]。
const (
	eventBullishCross    = "bullish_cross"
	eventBearishCross    = "bearish_cross"
	eventBullishReversal = "bullish_reversal"
	eventBearishReversal = "bearish_reversal"
	eventNone            = "none"
)

// Module 实现 macd_rsi 信号模块。零值可用。
type Module struct{}

// New 返回模块实例。
func New() *Module { return &Module{} }

// Name 实现 modules.SignalModule。
func (m *Module) Name() string { return ModuleName }

// Description 实现 modules.SignalModule。
func (m *Module) Description() string {
	return "结合 MACD 金叉/死叉与 RSI 超买超卖反转，检测趋势动量的转向。"
}

// RequiredParams 实现 modules.SignalModule。
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "fast_period", Type: types.ParamInt, Default: 12,
			Min: types.F(2), Max: types.F(100),
			Description: "MACD 快线 EMA 周期。",
		},
		{
			Name: "slow_period", Type: types.ParamInt, Default: 26,
			Min: types.F(3), Max: types.F(200),
			Description: "MACD 慢线 EMA 周期，必须大于 fast_period。",
		},
		{
			Name: "signal_period", Type: types.ParamInt, Default: 9,
			Min: types.F(2), Max: types.F(50),
			Description: "MACD 信号线（DEA）的 EMA 周期。",
		},
		{
			Name: "rsi_period", Type: types.ParamInt, Default: 14,
			Min: types.F(2), Max: types.F(100),
			Description: "RSI 计算周期（Wilder 平滑）。",
		},
		{
			Name: "rsi_overbought", Type: types.ParamFloat, Default: 70.0,
			Min: types.F(50), Max: types.F(95),
			Description: "RSI 超买阈值。",
		},
		{
			Name: "rsi_oversold", Type: types.ParamFloat, Default: 30.0,
			Min: types.F(5), Max: types.F(50),
			Description: "RSI 超卖阈值，必须小于 rsi_overbought。",
		},
		{
			Name: "mode", Type: types.ParamString, Default: ModeConfluence,
			Enum: []string{ModeMACDCross, ModeRSIReversal, ModeConfluence},
			Description: "macd_cross 只看 MACD 金叉死叉；rsi_reversal 只看 RSI 极值反转；" +
				"confluence 要求金叉死叉发生时 RSI 未处于同向极值区间。",
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
			Reason: fmt.Sprintf("必须大于 fast_period（%d）", fastPeriod), Given: slowPeriod,
		}
	}
	if oversold >= overbought {
		return types.Signal{}, &types.ParamError{
			Module: ModuleName, Param: "rsi_oversold",
			Reason: fmt.Sprintf("必须小于 rsi_overbought（%g）", overbought), Given: oversold,
		}
	}

	neutral := func(reason string) types.Signal {
		s := types.NeutralSignal(ModuleName, md.Symbol, reason, md.Time())
		if last, ok := md.Last(); ok {
			s.Price = last.Close
		}
		return s
	}

	// 至少要有两个有效的信号线/RSI 取值才能判定穿越（本根 vs 上一根）。
	minForMACD := slowPeriod + signalPeriod
	minForRSI := rsiPeriod + 2
	minCandles := minForMACD
	if minForRSI > minCandles {
		minCandles = minForRSI
	}
	if len(md.Candles) < minCandles {
		return neutral(fmt.Sprintf("K 线不足：需要至少 %d 根，实际 %d 根", minCandles, len(md.Candles))), nil
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
		s := neutral(fmt.Sprintf("模式 %s 下本根未触发信号（MACD %.4f/%.4f，RSI %.1f）",
			mode, macdNow.InexactFloat64(), sigNow.InexactFloat64(), rsiNow))
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

// decideDirection 把三种模式的触发条件收口成一个纯函数，便于单独测试——
// 尤其是 confluence 模式"金叉/死叉发生时 RSI 是否已处于同向极值区间"这条过滤逻辑，
// 不必依赖精心构造的行情数据就能直接验证。
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

// emaSeries 计算指数移动平均，起点用简单平均作种子。
//
// 返回的 series 与 values 等长；[0, validFrom) 区间的值未定义（零值），调用方不得读取。
// values 长度不足 period 时 validFrom 等于 len(values)（即整体不可用）。
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

// rsiSeries 用 Wilder 平滑法计算 RSI，取值范围 [0, 100]。
//
// RSI 是无量纲的振荡指标：内部用 decimal 计算保证精度（增减量本质是价格差），
// 对外以 float64 表示，与置信度、夏普等统计量的处理方式一致。
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
		return 50 // 完全没有波动，视为中性
	case avgLoss.IsZero():
		return 100
	case avgGain.IsZero():
		return 0
	}
	rs := avgGain.Div(avgLoss)
	hundred := decimal.NewFromInt(100)
	return hundred.Sub(hundred.Div(rs.Add(decimal.NewFromInt(1)))).InexactFloat64()
}

// avgAbsMACD 计算 MACD 线在 [from, to] 区间内（最多取最近 50 根）的平均绝对值，
// 用作置信度归一化的量纲基准，使同一套参数在不同价格量级的标的上行为一致。
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

// crossSignal 构造 MACD 金叉/死叉信号。
//
// 置信度按穿越后的差距相对近期 MACD 幅度归一化：差距越大说明动量转向越果断。
func crossSignal(
	symbol string, cur types.Candle, dir types.Direction, event string,
	macd, signal, norm decimal.Decimal, raw map[string]any,
) types.Signal {
	raw["event"] = event
	gap := macd.Sub(signal).Abs()

	verb := "金叉"
	if dir == types.DirectionShort {
		verb = "死叉"
	}
	return types.Signal{
		Module:     ModuleName,
		Symbol:     symbol,
		Direction:  dir,
		Confidence: crossConfidence(gap, norm),
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason:     fmt.Sprintf("MACD %s：MACD 线 %.4f，信号线 %.4f", verb, macd.InexactFloat64(), signal.InexactFloat64()),
		Raw:        raw,
	}
}

func crossConfidence(gap, norm decimal.Decimal) float64 {
	if !norm.IsPositive() {
		return 0.5
	}
	ratio := gap.Div(norm).InexactFloat64()
	return clamp(0.5+0.45*clamp(ratio, 0, 1), 0.5, 0.95)
}

// reversalSignal 构造 RSI 超买超卖反转信号。
//
// 置信度按穿回前 RSI 深入极值区间的程度：跌得越深/冲得越高，反转的信息量越大。
func reversalSignal(
	symbol string, cur types.Candle, dir types.Direction, event string,
	extremeRSI, oversold, overbought float64, raw map[string]any,
) types.Signal {
	raw["event"] = event

	var depth, span float64
	verb := "超卖反弹"
	if dir == types.DirectionShort {
		verb = "超买回落"
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
		Reason:     fmt.Sprintf("RSI %s：前值 %.1f 穿回阈值区间", verb, extremeRSI),
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
