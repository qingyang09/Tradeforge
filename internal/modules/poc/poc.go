// Package poc 实现 poc（Point of Control，成交量分布重心）信号模块。
//
// 真正的 POC 需要逐笔成交数据按精确价格分桶统计，平台目前只有 OHLCV K 线，算不出
// tick 级的成交量分布。这里用标准的粗粒度近似：把每根 K 线的成交量整根记到
// (最高+最低+收盘)/3 所在的价格桶，桶内成交量最大的即为近似 POC——跟本项目
// cvd_orderflow 的 SyntheticFlowProvider（is_synthetic）、news_sentiment 的
// KeywordSentimentProvider（is_heuristic）是同一处理方式：不假装精确，
// 在 Signal.Raw 里明确标注 is_approximate，供下游（比如止损止盈的合规判断）决定
// 是否接受这种精度。
package poc

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName 是该模块在策略配置中的标识。
const ModuleName = "poc"

// Module 实现 poc 信号模块。零值可用。
type Module struct{}

// New 返回模块实例。
func New() *Module { return &Module{} }

// Name 实现 modules.SignalModule。
func (m *Module) Name() string { return ModuleName }

// Description 实现 modules.SignalModule。
func (m *Module) Description() string {
	return "计算成交量分布重心（Point of Control）：把回看窗口内每根 K 线的成交量" +
		"记到其典型价格（最高+最低+收盘取平均）所在的价格桶，成交量最大的桶即为 POC。" +
		"只有 OHLCV 数据，是对真实逐笔成交量分布的粗粒度近似，不是精确值。"
}

// RequiredParams 实现 modules.SignalModule。
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "lookback", Type: types.ParamInt, Default: 100,
			Min: types.F(20), Max: types.F(500),
			Description: "回看窗口，参与成交量分布统计的 K 线根数。",
		},
		{
			Name: "bucket_count", Type: types.ParamInt, Default: 24,
			Min: types.F(5), Max: types.F(100),
			Description: "把回看窗口内的价格区间均分成多少个桶，桶越多价格分辨率越高，" +
				"但每个桶落入的样本也越少。",
		},
		{
			Name: "proximity", Type: types.ParamFloat, Default: 0.003,
			Min: types.F(0.0001), Max: types.F(0.05),
			Description: "触及判定距离：收盘价与 POC 的相对距离在该比例以内视为触及，含义与 " +
				"support_resistance 的同名参数一致。",
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

	lookback := types.MustInt(p, "lookback")
	bucketCount := types.MustInt(p, "bucket_count")
	proximity := decimal.NewFromFloat(types.MustFloat(p, "proximity"))

	neutral := func(reason string) types.Signal {
		s := types.NeutralSignal(ModuleName, md.Symbol, reason, md.Time())
		if last, ok := md.Last(); ok {
			s.Price = last.Close
		}
		s.Raw = map[string]any{"is_approximate": true}
		return s
	}

	if len(md.Candles) < 2 {
		return neutral(fmt.Sprintf("K 线不足：至少需要 2 根，实际 %d 根", len(md.Candles))), nil
	}

	window := md.Candles
	if len(window) > lookback {
		window = window[len(window)-lookback:]
	}
	// 不管算不算得出 POC，都把回看窗口的起点带出去——画板要用它在图上标出
	// "系统正在看这一段历史"，跟 fakeout 模块的 window_start 是同一个用途。
	windowStart := window[0].OpenTime.Format(time.RFC3339)

	pocPrice, pocVolume, err := computePOC(window, bucketCount)
	if err != nil {
		s := neutral(err.Error())
		s.Raw["window_start"] = windowStart
		return s, nil
	}

	cur := window[len(window)-1]
	if !cur.Close.IsPositive() {
		return neutral("最新收盘价非正，数据异常"), nil
	}

	raw := map[string]any{
		"poc_price":      pocPrice.String(),
		"poc_volume":     pocVolume.String(),
		"is_approximate": true,
		"bucket_count":   bucketCount,
		"lookback":       len(window),
		"window_start":   windowStart,
	}

	dist := relDist(cur.Close, pocPrice)
	if dist.GreaterThan(proximity) {
		s := types.NeutralSignal(ModuleName, md.Symbol, "收盘价未触及成交量分布重心（POC）附近", cur.CloseTime)
		s.Price = cur.Close
		s.Raw = raw
		return s, nil
	}

	// 从下方触及 POC：成交密集区常被当作阻力，偏向看空；从上方触及则偏向看多。
	// 跟 support_resistance 的 test_support/test_resistance 是同一套直觉，不是新发明的规则。
	dir := types.DirectionShort
	if cur.Close.LessThan(pocPrice) {
		dir = types.DirectionLong
	}

	return types.Signal{
		Module:     ModuleName,
		Symbol:     md.Symbol,
		Direction:  dir,
		Confidence: 0.4, // 近似值本来就不精确，置信度刻意压低，不跟 support_resistance 的精确关键位同一档
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason: fmt.Sprintf("收盘价 %s 触及近似 POC %s（回看 %d 根 K 线，%d 个价格桶）",
			cur.Close, pocPrice, len(window), bucketCount),
		Raw: raw,
	}, nil
}

// computePOC 把 candles 的成交量按典型价格分桶，返回成交量最大的桶的中点价格与成交量。
func computePOC(candles []types.Candle, bucketCount int) (decimal.Decimal, decimal.Decimal, error) {
	low, high := candles[0].Low, candles[0].High
	for _, c := range candles {
		if c.Low.LessThan(low) {
			low = c.Low
		}
		if c.High.GreaterThan(high) {
			high = c.High
		}
	}
	span := high.Sub(low)
	if !span.IsPositive() {
		return decimal.Zero, decimal.Zero, fmt.Errorf("回看窗口内价格没有波动，无法计算成交量分布")
	}

	buckets := make([]decimal.Decimal, bucketCount)
	bucketWidth := span.Div(decimal.NewFromInt(int64(bucketCount)))
	three := decimal.NewFromInt(3)

	for _, c := range candles {
		typical := c.High.Add(c.Low).Add(c.Close).Div(three)
		idx := typical.Sub(low).Div(bucketWidth).IntPart()
		if idx < 0 {
			idx = 0
		}
		if idx >= int64(bucketCount) {
			idx = int64(bucketCount) - 1
		}
		buckets[idx] = buckets[idx].Add(c.Volume)
	}

	bestIdx := 0
	for i, v := range buckets {
		if v.GreaterThan(buckets[bestIdx]) {
			bestIdx = i
		}
	}

	// 桶中点作为该桶的代表价格。
	half := decimal.NewFromFloat(0.5)
	bucketPrice := low.Add(bucketWidth.Mul(decimal.NewFromInt(int64(bestIdx)).Add(half)))
	return bucketPrice, buckets[bestIdx], nil
}

// relDist 返回两个价格的相对距离 |a-b|/b。b 为零时返回一个必然超出任何容差的大值。
func relDist(a, b decimal.Decimal) decimal.Decimal {
	if b.IsZero() {
		return decimal.NewFromInt(1 << 30)
	}
	return a.Sub(b).Abs().Div(b.Abs())
}
