// Package supportresistance implements the support_resistance signal module.
//
// The algorithm has three steps:
//  1. Identify swing highs and lows (pivots) within the lookback window
//  2. Cluster nearby pivots by relative tolerance into "key levels" — a level
//     touched more times is more significant
//  3. Determine what the latest candle did relative to these key levels:
//     broke out, broke down, or retested
//
// Key levels are always computed from history that excludes the latest
// candle, to avoid the current candle defining its own support/resistance —
// that would make every candle permanently sit right on a level it invented itself.
package supportresistance

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName is this module's identifier in strategy configs.
const ModuleName = "support_resistance"

// Module implements the support_resistance signal module. The zero value is usable.
type Module struct{}

// New returns a module instance.
func New() *Module { return &Module{} }

// Name implements modules.SignalModule.
func (m *Module) Name() string { return ModuleName }

// Description implements modules.SignalModule.
func (m *Module) Description() string {
	return "基于回看窗口内摆动高低点的聚类，计算支撑位与阻力位，并在价格突破、跌破或回踩关键位时输出信号。"
}

// RequiredParams implements modules.SignalModule.
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "lookback", Type: types.ParamInt, Default: 200,
			Min: types.F(30), Max: types.F(1000),
			Description: "回看窗口，参与关键位计算的 K 线根数。",
		},
		{
			Name: "pivot_strength", Type: types.ParamInt, Default: 2,
			Min: types.F(1), Max: types.F(10),
			Description: "摆动点强度：一根 K 线的高点要高于左右各 N 根才算摆动高点，低点同理。数值越大识别出的点越少但越显著。",
		},
		{
			Name: "tolerance", Type: types.ParamFloat, Default: 0.005,
			Min: types.F(0.0001), Max: types.F(0.05),
			Description: "聚类容差，以价格的相对比例计。0.005 表示相差 0.5% 以内的摆动点归为同一个关键位。",
		},
		{
			Name: "min_touches", Type: types.ParamInt, Default: 2,
			Min: types.F(1), Max: types.F(10),
			Description: "一个关键位至少要被触及的次数，低于该次数的位被丢弃。",
		},
		{
			Name: "breakout_confirm", Type: types.ParamFloat, Default: 0.001,
			Min: types.F(0), Max: types.F(0.05),
			Description: "突破确认幅度：收盘价要越过关键位这个比例才算突破，用于过滤刺破。",
		},
		{
			Name: "proximity", Type: types.ParamFloat, Default: 0.003,
			Min: types.F(0.0001), Max: types.F(0.05),
			Description: "回踩判定距离：收盘价与关键位的相对距离在该比例以内视为触及测试。",
		},
	}
}

// Level is one key level produced by clustering.
type Level struct {
	// Price is the mean price of all swing points in this cluster.
	Price decimal.Decimal `json:"price"`
	// Touches is the number of swing points that make up this level.
	Touches int `json:"touches"`
	// Kind is "high" (formed from swing highs) or "low" (formed from swing lows).
	Kind string `json:"kind"`
	// LastIndex is the index, within the lookback window, of the most recent
	// swing point forming this level — a larger value means more recent.
	LastIndex int `json:"last_index"`
}

// Event types, written to Signal.Raw["event"].
const (
	eventBreakout   = "breakout"        // broke above resistance
	eventBreakdown  = "breakdown"       // broke below support
	eventTestSupp   = "test_support"    // retested support
	eventTestResist = "test_resistance" // tested resistance from below
	eventNone       = "none"
)

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
	strength := types.MustInt(p, "pivot_strength")
	tolerance := decimal.NewFromFloat(types.MustFloat(p, "tolerance"))
	minTouches := types.MustInt(p, "min_touches")
	confirm := decimal.NewFromFloat(types.MustFloat(p, "breakout_confirm"))
	proximity := decimal.NewFromFloat(types.MustFloat(p, "proximity"))

	// neutral is the single place that builds neutral signals, carrying the
	// reference price along — even with no signal, downstream audit still
	// needs to know "what was the price at the time".
	neutral := func(reason string) types.Signal {
		s := types.NeutralSignal(ModuleName, md.Symbol, reason, md.Time())
		if last, ok := md.Last(); ok {
			s.Price = last.Close
		}
		return s
	}

	// Need at least: enough candles to identify a pivot (2*strength+1), plus the current and previous candle.
	minCandles := 2*strength + 3
	if len(md.Candles) < minCandles {
		return neutral(fmt.Sprintf("K 线不足：需要至少 %d 根，实际 %d 根", minCandles, len(md.Candles))), nil
	}

	cur := md.Candles[len(md.Candles)-1]
	prev := md.Candles[len(md.Candles)-2]
	if !cur.Close.IsPositive() {
		return neutral("最新收盘价非正，数据异常"), nil
	}

	// Key levels are computed from history only, excluding the latest candle.
	history := md.Candles[:len(md.Candles)-1]
	if len(history) > lookback {
		history = history[len(history)-lookback:]
	}
	// Carry the lookback window's start out regardless of whether any key
	// levels were found — the chart uses it to mark "this is the history the
	// system is looking at", the same purpose as window_start in the
	// fakeout/poc modules.
	windowStart := history[0].OpenTime.Format(time.RFC3339)

	highs, lows := findPivots(history, strength)
	levels := findLevelsFromPivots(highs, lows, tolerance, minTouches)

	if len(levels) == 0 {
		s := neutral(fmt.Sprintf("回看窗口内未找到触及次数达到 %d 次的关键位", minTouches))
		s.Raw = map[string]any{"window_start": windowStart}
		return s, nil
	}

	sig := m.detect(md.Symbol, cur, prev, levels, confirm, proximity)
	sig.Raw["levels"] = levels
	sig.Raw["lookback_candles"] = len(history)
	sig.Raw["window_start"] = windowStart
	sig.Raw["pivot_highs"] = len(highs)
	sig.Raw["pivot_lows"] = len(lows)
	return sig, nil
}

// detect determines what the latest candle did relative to key levels, and
// returns a direction and confidence.
//
// Priority: breakout/breakdown > retest. When multiple key levels qualify at
// once, the most significant one (most touches) wins.
func (m *Module) detect(
	symbol string, cur, prev types.Candle, levels []Level,
	confirm, proximity decimal.Decimal,
) types.Signal {
	one := decimal.NewFromInt(1)

	var best *Level
	var bestEvent string
	var bestDir types.Direction

	consider := func(l Level, event string, dir types.Direction) {
		// Within the same priority, compare touch counts; breakout-type
		// events outrank test-type events.
		if best == nil ||
			(rank(event) > rank(bestEvent)) ||
			(rank(event) == rank(bestEvent) && l.Touches > best.Touches) {
			lv := l
			best, bestEvent, bestDir = &lv, event, dir
		}
	}

	for _, l := range levels {
		if !l.Price.IsPositive() {
			continue
		}
		upTrigger := l.Price.Mul(one.Add(confirm))   // price that must be exceeded for an upward breakout
		downTrigger := l.Price.Mul(one.Sub(confirm)) // price that must be broken for a downward breakdown

		switch {
		// The previous candle was still at or below the level, and this
		// candle's close decisively moved above it -> breakout.
		case prev.Close.LessThanOrEqual(l.Price) && cur.Close.GreaterThan(upTrigger):
			consider(l, eventBreakout, types.DirectionLong)

		// The previous candle was still at or above the level, and this
		// candle's close decisively broke below it -> breakdown.
		case prev.Close.GreaterThanOrEqual(l.Price) && cur.Close.LessThan(downTrigger):
			consider(l, eventBreakdown, types.DirectionShort)

		// No breakout, but the close is sitting right at the level -> a
		// retest. Level below price is a support test, above is a resistance test.
		case relDist(cur.Close, l.Price).LessThanOrEqual(proximity):
			if l.Price.LessThanOrEqual(cur.Close) {
				consider(l, eventTestSupp, types.DirectionLong)
			} else {
				consider(l, eventTestResist, types.DirectionShort)
			}
		}
	}

	nearestSupp, nearestRes := nearest(levels, cur.Close)
	raw := map[string]any{
		"event":              eventNone,
		"close":              cur.Close.String(),
		"prev_close":         prev.Close.String(),
		"nearest_support":    levelPtrString(nearestSupp),
		"nearest_resistance": levelPtrString(nearestRes),
	}

	if best == nil {
		s := types.NeutralSignal(ModuleName, symbol, "收盘价既未突破也未贴近任何关键位", cur.CloseTime)
		s.Price = cur.Close
		s.Raw = raw
		return s
	}

	raw["event"] = bestEvent
	raw["level_price"] = best.Price.String()
	raw["level_touches"] = best.Touches

	return types.Signal{
		Module:     ModuleName,
		Symbol:     symbol,
		Direction:  bestDir,
		Confidence: confidenceFor(bestEvent, best.Touches),
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason:     reasonFor(bestEvent, *best, cur.Close),
		Raw:        raw,
	}
}

func rank(event string) int {
	switch event {
	case eventBreakout, eventBreakdown:
		return 2
	case eventTestSupp, eventTestResist:
		return 1
	default:
		return 0
	}
}

// confidenceFor maps an event type and touch count to a [0,1] confidence.
// Breakout events have a higher baseline than test events; each additional
// touch adds a bit more, capped to prevent unbounded growth.
func confidenceFor(event string, touches int) float64 {
	base := 0.35
	if rank(event) == 2 {
		base = 0.55
	}
	c := base + 0.08*float64(touches-1)
	if c > 0.95 {
		c = 0.95
	}
	return c
}

func reasonFor(event string, l Level, close decimal.Decimal) string {
	switch event {
	case eventBreakout:
		return fmt.Sprintf("收盘价 %s 向上突破阻力位 %s（该位被触及 %d 次）",
			close.String(), l.Price.String(), l.Touches)
	case eventBreakdown:
		return fmt.Sprintf("收盘价 %s 向下跌破支撑位 %s（该位被触及 %d 次）",
			close.String(), l.Price.String(), l.Touches)
	case eventTestSupp:
		return fmt.Sprintf("收盘价 %s 回踩支撑位 %s（该位被触及 %d 次）",
			close.String(), l.Price.String(), l.Touches)
	case eventTestResist:
		return fmt.Sprintf("收盘价 %s 上测阻力位 %s（该位被触及 %d 次）",
			close.String(), l.Price.String(), l.Touches)
	default:
		return "无事件"
	}
}

// findPivots identifies swing highs and swing lows.
//
// A candle is a swing high if and only if its high is strictly greater than
// the high of each of the `strength` candles on both sides. The strict
// inequality is deliberate: consecutive equal highs don't constitute a
// "swing", and counting them all would artificially inflate the touch count
// during clustering.
func findPivots(candles []types.Candle, strength int) (highs, lows []pivot) {
	for i := strength; i < len(candles)-strength; i++ {
		isHigh, isLow := true, true
		for j := i - strength; j <= i+strength; j++ {
			if j == i {
				continue
			}
			if candles[j].High.GreaterThanOrEqual(candles[i].High) {
				isHigh = false
			}
			if candles[j].Low.LessThanOrEqual(candles[i].Low) {
				isLow = false
			}
			if !isHigh && !isLow {
				break
			}
		}
		if isHigh {
			highs = append(highs, pivot{Price: candles[i].High, Index: i})
		}
		if isLow {
			lows = append(lows, pivot{Price: candles[i].Low, Index: i})
		}
	}
	return highs, lows
}

type pivot struct {
	Price decimal.Decimal
	Index int
}

// clusterLevels clusters swing points by relative tolerance.
//
// It works by sorting prices ascending and scanning linearly: a point is
// merged into the current cluster as long as its relative distance from the
// cluster's first point is still within tolerance, otherwise a new cluster
// starts. Using relative tolerance (rather than an absolute price
// difference) ensures the same parameter set behaves consistently on BTC and
// on some low-priced coin.
func clusterLevels(pivots []pivot, tolerance decimal.Decimal, kind string) []Level {
	if len(pivots) == 0 {
		return nil
	}
	sorted := make([]pivot, len(pivots))
	copy(sorted, pivots)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Price.LessThan(sorted[j].Price) })

	var out []Level
	cluster := []pivot{sorted[0]}

	flush := func() {
		sum := decimal.Zero
		last := 0
		for _, p := range cluster {
			sum = sum.Add(p.Price)
			if p.Index > last {
				last = p.Index
			}
		}
		out = append(out, Level{
			Price:     sum.Div(decimal.NewFromInt(int64(len(cluster)))),
			Touches:   len(cluster),
			Kind:      kind,
			LastIndex: last,
		})
	}

	for _, p := range sorted[1:] {
		if relDist(p.Price, cluster[0].Price).LessThanOrEqual(tolerance) {
			cluster = append(cluster, p)
			continue
		}
		flush()
		cluster = []pivot{p}
	}
	flush()
	return out
}

// FindLevels is the exported entry point to this module's "cluster swing
// points into key levels" algorithm, for reuse by other modules that need
// the same key-level definition, avoiding two implementations that could
// drift apart — there is exactly one platform-wide definition of "what a key
// level is". (The fakeout module now uses a coarser-grained "consolidation
// range high/low" instead and no longer depends on this point-by-point clustering.)
func FindLevels(history []types.Candle, pivotStrength int, tolerance decimal.Decimal, minTouches int) []Level {
	highs, lows := findPivots(history, pivotStrength)
	return findLevelsFromPivots(highs, lows, tolerance, minTouches)
}

// findLevelsFromPivots is FindLevels with the pivot-finding step removed;
// Evaluate uses it internally to avoid scanning for pivots twice.
func findLevelsFromPivots(highs, lows []pivot, tolerance decimal.Decimal, minTouches int) []Level {
	levels := append(
		clusterLevels(highs, tolerance, "high"),
		clusterLevels(lows, tolerance, "low")...,
	)
	return filterByTouches(levels, minTouches)
}

func filterByTouches(levels []Level, min int) []Level {
	out := make([]Level, 0, len(levels))
	for _, l := range levels {
		if l.Touches >= min {
			out = append(out, l)
		}
	}
	return out
}

// nearest returns the support closest to the current price (the highest
// level below it) and the resistance closest to it (the lowest level above it).
func nearest(levels []Level, price decimal.Decimal) (support, resistance *Level) {
	for i := range levels {
		l := levels[i]
		switch {
		case l.Price.LessThanOrEqual(price):
			if support == nil || l.Price.GreaterThan(support.Price) {
				support = &levels[i]
			}
		default:
			if resistance == nil || l.Price.LessThan(resistance.Price) {
				resistance = &levels[i]
			}
		}
	}
	return support, resistance
}

// relDist returns the relative distance |a-b|/b between two prices. When b is
// zero, it returns a large value guaranteed to exceed any tolerance.
func relDist(a, b decimal.Decimal) decimal.Decimal {
	if b.IsZero() {
		return decimal.NewFromInt(1 << 30)
	}
	return a.Sub(b).Abs().Div(b.Abs())
}

func levelPtrString(l *Level) any {
	if l == nil {
		return nil
	}
	return map[string]any{"price": l.Price.String(), "touches": l.Touches}
}
