// Package poc implements the poc (Point of Control, volume-distribution
// center of mass) signal module.
//
// A true POC requires tick-by-tick trade data bucketed by exact price; the
// platform currently only has OHLCV candles, so a tick-level volume
// distribution can't be computed. This uses a standard coarse approximation
// instead: each candle's entire volume is attributed to the price bucket
// containing (high+low+close)/3, and the bucket with the most volume is the
// approximate POC. This is the same treatment as this project's
// cvd_orderflow SyntheticFlowProvider (is_synthetic) and news_sentiment's
// KeywordSentimentProvider (is_heuristic): don't pretend it's precise —
// explicitly tag is_approximate in Signal.Raw, letting downstream code (e.g.
// stop-loss/take-profit compliance checks) decide whether to accept this
// level of precision.
package poc

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName is this module's identifier in strategy configs.
const ModuleName = "poc"

// Module implements the poc signal module. The zero value is usable.
type Module struct{}

// New returns a module instance.
func New() *Module { return &Module{} }

// Name implements modules.SignalModule.
func (m *Module) Name() string { return ModuleName }

// Description implements modules.SignalModule.
func (m *Module) Description() string {
	return "计算成交量分布重心（Point of Control）：把回看窗口内每根 K 线的成交量" +
		"记到其典型价格（最高+最低+收盘取平均）所在的价格桶，成交量最大的桶即为 POC。" +
		"只有 OHLCV 数据，是对真实逐笔成交量分布的粗粒度近似，不是精确值。"
}

// RequiredParams implements modules.SignalModule.
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

// Evaluate implements modules.SignalModule.
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
	// Carry the lookback window's start out regardless of whether a POC could
	// be computed — the chart uses it to mark "this is the history the system
	// is looking at", the same purpose as window_start in the fakeout module.
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

	// Approaching the POC from below: a high-volume zone is often treated as
	// resistance, leaning bearish; approaching from above leans bullish.
	// Same intuition as support_resistance's test_support/test_resistance,
	// not a newly invented rule.
	dir := types.DirectionShort
	if cur.Close.LessThan(pocPrice) {
		dir = types.DirectionLong
	}

	return types.Signal{
		Module:     ModuleName,
		Symbol:     md.Symbol,
		Direction:  dir,
		Confidence: 0.4, // an approximation is inherently imprecise, so confidence is deliberately kept low, not on par with support_resistance's exact key levels
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason: fmt.Sprintf("收盘价 %s 触及近似 POC %s（回看 %d 根 K 线，%d 个价格桶）",
			cur.Close, pocPrice, len(window), bucketCount),
		Raw: raw,
	}, nil
}

// computePOC buckets candles' volume by typical price, returning the midpoint
// price and volume of the bucket with the most volume.
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

	// Use the bucket's midpoint as its representative price.
	half := decimal.NewFromFloat(0.5)
	bucketPrice := low.Add(bucketWidth.Mul(decimal.NewFromInt(int64(bestIdx)).Add(half)))
	return bucketPrice, buckets[bestIdx], nil
}

// relDist returns the relative distance |a-b|/b between two prices. When b is
// zero, it returns a large value guaranteed to exceed any tolerance.
func relDist(a, b decimal.Decimal) decimal.Decimal {
	if b.IsZero() {
		return decimal.NewFromInt(1 << 30)
	}
	return a.Sub(b).Abs().Div(b.Abs())
}
