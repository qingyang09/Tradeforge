package webui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"tradeforge/pkg/types"
)

// segmentOutOfSample 匹配 python/backtest 端 Segment.OUT_OF_SAMPLE.value 与
// pkg/types.BacktestTrade.Segment 的取值约定（该字段只在注释里说明取值，未定义常量）。
const segmentOutOfSample = "out_of_sample"

// equityChart 是权益曲线图要用到的预计算数据。
//
// SVG 是标记语言，坐标点计算放在 Go 里更好测试；模板只管把现成的点字符串
// 套进 <polyline>/<polygon>，不做任何算术。
type equityChart struct {
	Width, Height int
	// EquityInSamplePoints/EquityOutOfSamplePoints：真实权益曲线（IsRealEquityCurve
	// 为 true 时）或近似的累计盈亏曲线（为 false 时，旧记录没有逐根权益数据）。
	InSamplePoints    string
	OutOfSamplePoints string
	// PeakPoints 是权益曲线的历史新高包络线（只在 IsRealEquityCurve 时有意义）——
	// 配合 DrawdownAreaPoints 一起看，两条线之间的阴影就是回撤区间。
	PeakPoints string
	// DrawdownAreaPoints 是 peak 线与 equity 线围成的闭合多边形，画成半透明填充，
	// 直观标出"这段时间账户在水下多深"——逐笔已实现盈亏的累加曲线看不出这个，
	// 一笔中途深度浮亏、最后小亏离场的交易在那种曲线上完全不可见。
	DrawdownAreaPoints string
	MinLabel, MaxLabel string
	HasTrades          bool
	// IsRealEquityCurve 为 true 表示用的是逐根K线的真实权益（含浮动盈亏），
	// 为 false 表示退回用逐笔已实现盈亏累加近似——2026-09 之前的回测记录没有
	// 持久化真实权益曲线，界面对这些旧记录只能用近似值，需要如实告知用户区别。
	IsRealEquityCurve bool
}

// buildEquityChart 优先用 BacktestResult.EquityCurve（逐根K线的真实权益，含浮动
// 盈亏）画图；没有这份数据的旧记录退回用 Trades 的累计已实现盈亏近似——参见
// equityChart.IsRealEquityCurve 的注释。
func buildEquityChart(result types.BacktestResult) equityChart {
	if len(result.EquityCurve) >= 2 {
		return buildRealEquityChart(result)
	}
	return buildApproxEquityChart(result.Trades)
}

// splitAtFromSegments 从 Segments 里取样本内/样本外的分界时间点——
// Segments[0]（in_sample）的 End 就是 python 端 split_at。数据缺失或形状不对时
// 返回零值，调用方据此把整条曲线都当作样本内处理（保守：不确定就不分色）。
func splitAtFromSegments(segments []types.BacktestSegment) time.Time {
	for _, seg := range segments {
		if seg.Label == "in_sample" {
			return seg.End
		}
	}
	return time.Time{}
}

func buildRealEquityChart(result types.BacktestResult) equityChart {
	const width, height = 640, 220
	c := equityChart{Width: width, Height: height, HasTrades: true, IsRealEquityCurve: true}
	curve := result.EquityCurve
	splitAt := splitAtFromSegments(result.Segments)

	values := make([]float64, len(curve))
	peak := make([]float64, len(curve))
	minV, maxV := curve[0].Equity.InexactFloat64(), curve[0].Equity.InexactFloat64()
	for i, p := range curve {
		v := p.Equity.InexactFloat64()
		values[i] = v
		if i == 0 || v > peak[i-1] {
			peak[i] = v
		} else {
			peak[i] = peak[i-1]
		}
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	span := maxV - minV
	if span == 0 {
		span = 1 // 全程权益不变时画一条水平线，避免除零
	}

	n := len(curve)
	x := func(i int) float64 { return float64(i) / float64(n-1) * float64(width) }
	y := func(v float64) float64 { return float64(height) - (v-minV)/span*float64(height) }
	point := func(i int, v float64) string { return fmt.Sprintf("%.1f,%.1f", x(i), y(v)) }

	var inPts, outPts, peakPts []string
	lastWasOutOfSample := false
	for i := range curve {
		isOOS := !splitAt.IsZero() && !curve[i].Time.Before(splitAt)
		pt := point(i, values[i])
		if isOOS {
			if !lastWasOutOfSample && len(inPts) > 0 {
				outPts = append(outPts, inPts[len(inPts)-1]) // 接上样本内终点，视觉不断开
			}
			outPts = append(outPts, pt)
			lastWasOutOfSample = true
		} else {
			inPts = append(inPts, pt)
			lastWasOutOfSample = false
		}
		peakPts = append(peakPts, point(i, peak[i]))
	}
	c.InSamplePoints = strings.Join(inPts, " ")
	c.OutOfSamplePoints = strings.Join(outPts, " ")
	c.PeakPoints = strings.Join(peakPts, " ")

	// 回撤阴影：沿 peak 线正向走一遍，再沿 equity 线反向走回来，首尾相接围成一个
	// 闭合多边形——中间夹住的正是"权益比历史新高低多少"这块区域。
	forward := make([]string, n)
	backward := make([]string, n)
	for i := 0; i < n; i++ {
		forward[i] = point(i, peak[i])
		backward[n-1-i] = point(i, values[i])
	}
	c.DrawdownAreaPoints = strings.Join(forward, " ") + " " + strings.Join(backward, " ")

	c.MinLabel = fmt.Sprintf("%.2f", minV)
	c.MaxLabel = fmt.Sprintf("%.2f", maxV)
	return c
}

// buildApproxEquityChart 用 Trades（按开仓时间排序、累计已实现盈亏）重建一条近似
// 权益曲线——只在 EquityCurve 数据缺失时使用（2026-09 之前的历史回测记录）。
func buildApproxEquityChart(trades []types.BacktestTrade) equityChart {
	const width, height = 640, 200
	c := equityChart{Width: width, Height: height}
	if len(trades) == 0 {
		return c
	}
	c.HasTrades = true

	sorted := make([]types.BacktestTrade, len(trades))
	copy(sorted, trades)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].EntryTime.Before(sorted[j].EntryTime) })

	cumulative := make([]float64, len(sorted)+1) // cumulative[0] 是起点 0
	for i, t := range sorted {
		cumulative[i+1] = cumulative[i] + t.PnL.InexactFloat64()
	}

	minV, maxV := cumulative[0], cumulative[0]
	for _, v := range cumulative {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}
	span := maxV - minV
	if span == 0 {
		span = 1 // 全程零盈亏时画一条水平线，避免除零
	}

	x := func(i int) float64 { return float64(i) / float64(len(cumulative)-1) * float64(width) }
	y := func(v float64) float64 { return float64(height) - (v-minV)/span*float64(height) }
	point := func(i int, v float64) string { return fmt.Sprintf("%.1f,%.1f", x(i), y(v)) }

	var inPts, outPts []string
	lastWasOutOfSample := false
	for i, t := range sorted {
		if i == 0 {
			origin := point(0, cumulative[0])
			if t.Segment == segmentOutOfSample {
				outPts = append(outPts, origin)
			} else {
				inPts = append(inPts, origin)
			}
		}
		pt := point(i+1, cumulative[i+1])
		if t.Segment == segmentOutOfSample {
			// 从样本内切到样本外的第一笔：把上一段的终点接过来，两条折线视觉上不断开。
			if !lastWasOutOfSample && len(inPts) > 0 {
				outPts = append(outPts, inPts[len(inPts)-1])
			}
			outPts = append(outPts, pt)
			lastWasOutOfSample = true
		} else {
			inPts = append(inPts, pt)
			lastWasOutOfSample = false
		}
	}

	c.InSamplePoints = strings.Join(inPts, " ")
	c.OutOfSamplePoints = strings.Join(outPts, " ")
	c.MinLabel = fmt.Sprintf("%.2f", minV)
	c.MaxLabel = fmt.Sprintf("%.2f", maxV)
	return c
}

// monthlyReturn 是权益曲线按自然月切分后，某个月的收益率。
type monthlyReturn struct {
	Month  string // "2025-01"
	Return float64
}

// buildMonthlyReturns 把真实权益曲线按自然月分桶，算出每个月的收益率
// （月末权益 / 上个月月末权益 - 1；第一个月用曲线起点权益当分母）。
// 只有 IsRealEquityCurve 数据可用时才有意义——近似曲线是逐笔盈亏累加，
// 强行按自然月切分会把"这个月到底赚了多少"算错（漏掉月中持仓的浮动部分）。
func buildMonthlyReturns(curve []types.EquityPoint) []monthlyReturn {
	if len(curve) < 2 {
		return nil
	}
	type bucket struct {
		month     string
		lastValue float64
	}
	var buckets []bucket
	for _, p := range curve {
		month := p.Time.Format("2006-01")
		v := p.Equity.InexactFloat64()
		if len(buckets) == 0 || buckets[len(buckets)-1].month != month {
			buckets = append(buckets, bucket{month: month, lastValue: v})
		} else {
			buckets[len(buckets)-1].lastValue = v
		}
	}
	if len(buckets) < 1 {
		return nil
	}

	out := make([]monthlyReturn, 0, len(buckets))
	prev := curve[0].Equity.InexactFloat64()
	for _, b := range buckets {
		var ret float64
		if prev != 0 {
			ret = (b.lastValue - prev) / prev
		}
		out = append(out, monthlyReturn{Month: b.month, Return: ret})
		prev = b.lastValue
	}
	return out
}

// holdingDuration 格式化一笔交易的持仓时长，供模板里的逐笔交易表格使用——
// html/template 不能做时间减法，算术必须放在 Go 这边。
func holdingDuration(entry, exit time.Time) string {
	d := exit.Sub(entry)
	if d < 0 {
		return "-"
	}
	return d.Round(time.Minute).String()
}
