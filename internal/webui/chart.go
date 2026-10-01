package webui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"tradeforge/pkg/types"
)

// segmentOutOfSample matches python/backtest's Segment.OUT_OF_SAMPLE.value
// and pkg/types.BacktestTrade.Segment's value convention (that field only
// documents its values in a comment, with no defined constant).
const segmentOutOfSample = "out_of_sample"

// equityChart holds the precomputed data the equity-curve chart needs.
//
// SVG is a markup language; coordinate-point math is easier to test in Go,
// and the template just drops the ready-made point strings into
// <polyline>/<polygon>, doing no arithmetic of its own.
type equityChart struct {
	Width, Height int
	// EquityInSamplePoints/EquityOutOfSamplePoints: the real equity curve
	// (when IsRealEquityCurve is true) or an approximate cumulative P&L
	// curve (when false, for old records with no per-candle equity data).
	InSamplePoints    string
	OutOfSamplePoints string
	// PeakPoints is the equity curve's running-high envelope (only
	// meaningful when IsRealEquityCurve) -- viewed together with
	// DrawdownAreaPoints, the shaded area between the two lines is the
	// drawdown region.
	PeakPoints string
	// DrawdownAreaPoints is the closed polygon enclosed by the peak line
	// and the equity line, filled semi-transparent to visually mark "how
	// far underwater the account was during this span" -- a cumulative
	// realized-P&L curve can't show this: a trade with a deep unrealized
	// loss mid-trade that exits at a small loss is completely invisible on
	// that kind of curve.
	DrawdownAreaPoints string
	MinLabel, MaxLabel string
	HasTrades          bool
	// IsRealEquityCurve true means this uses the real per-candle equity
	// (including unrealized P&L); false means it fell back to an
	// approximation built from cumulative realized P&L per trade --
	// backtest records from before 2026-09 never persisted a real equity
	// curve, so the UI can only use an approximation for those old records
	// and must honestly disclose the difference to the user.
	IsRealEquityCurve bool
}

// buildEquityChart prefers BacktestResult.EquityCurve (the real per-candle
// equity, including unrealized P&L) for the chart; an old record lacking
// that data falls back to an approximation built from Trades' cumulative
// realized P&L -- see equityChart.IsRealEquityCurve's comment.
func buildEquityChart(result types.BacktestResult) equityChart {
	if len(result.EquityCurve) >= 2 {
		return buildRealEquityChart(result)
	}
	return buildApproxEquityChart(result.Trades)
}

// splitAtFromSegments pulls the in-sample/out-of-sample split time from
// Segments -- Segments[0] (in_sample)'s End is exactly the Python side's
// split_at. Returns the zero value when the data is missing or malformed,
// so the caller treats the whole curve as in-sample (conservative: when
// unsure, don't color-split it).
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
		span = 1 // draws a flat horizontal line when equity never moves, avoiding a division by zero
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
				outPts = append(outPts, inPts[len(inPts)-1]) // connects to the in-sample segment's endpoint so the line doesn't visually break
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

	// Drawdown shading: walk forward along the peak line, then backward
	// along the equity line, meeting end to end to enclose a closed
	// polygon -- the area sandwiched in between is exactly "how far below
	// the running high equity currently sits."
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

// buildApproxEquityChart rebuilds an approximate equity curve from Trades
// (sorted by entry time, cumulative realized P&L) -- used only when
// EquityCurve data is missing (historical backtest records from before
// 2026-09).
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

	cumulative := make([]float64, len(sorted)+1) // cumulative[0] is the starting point, 0
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
		span = 1 // draws a flat horizontal line when P&L is zero throughout, avoiding a division by zero
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
			// The first trade switching from in-sample to out-of-sample: carries over the previous segment's endpoint, so the two polylines don't visually break.
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

// monthlyReturn is one calendar month's return, after bucketing the equity curve by month.
type monthlyReturn struct {
	Month  string // "2025-01"
	Return float64
}

// buildMonthlyReturns buckets the real equity curve by calendar month and
// computes each month's return (month-end equity / previous month-end
// equity - 1; the first month uses the curve's starting equity as the
// denominator). Only meaningful when IsRealEquityCurve data is available --
// the approximate curve is a cumulative sum of per-trade P&L, and forcing a
// calendar-month split on it would miscompute "how much was actually made
// this month" (missing the floating portion of a position still open
// mid-month).
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

// holdingDuration formats a trade's holding period, for the per-trade table
// in the template -- html/template can't do time subtraction, so the
// arithmetic has to live on the Go side.
func holdingDuration(entry, exit time.Time) string {
	d := exit.Sub(entry)
	if d < 0 {
		return "-"
	}
	return d.Round(time.Minute).String()
}
