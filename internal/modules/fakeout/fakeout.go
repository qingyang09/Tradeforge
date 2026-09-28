// Package fakeout implements the fakeout signal module: first identify a
// "consolidation range", treat the range's high as "prior high" and its low
// as "prior low", then detect whether price broke above/below the range and
// quickly reversed back — that's a fakeout.
//
// This doesn't use point-by-point pivot clustering (that would report a pile
// of fragmented key levels on any fluctuation, too noisy for a "prior-high
// fakeout" description) — it only tracks one clear range closest to the
// current point. How the range is defined is controlled by range_mode, with
// two mutually incompatible semantics:
//   - tight (default): requires the high/low spread relative to the midpoint
//     price to stay within range_tightness, expanding backward from the
//     current point and stopping the moment it first exceeds tolerance —
//     this is the test for "genuine sideways chop" and automatically
//     excludes trending markets. Limitation: the tolerance is a fixed ratio,
//     and the longer the sample, the more likely it is that one extreme wick
//     stretches the high/low spread — so a consolidation spanning a very
//     long time is actually more likely to get cut off early by this fixed
//     threshold, missing the true prior high/low.
//   - extreme: doesn't test "tight enough" at all — it just takes the
//     highest/lowest price across the entire range_lookback window as the
//     prior high/low, regardless of whether this period was genuinely
//     sideways. Choosing this mode is the user telling the system "I've
//     already decided this period is a consolidation, just report me the
//     extremes." Suited to a consolidation that spans a long time and isn't
//     easy to quantify as a specific number of candles. The tradeoff is it
//     no longer distinguishes "consolidation" from "trend": a one-directional
//     trending market fed into it will still dutifully report the range's
//     highest/lowest price.
package fakeout

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName is this module's identifier in strategy configs.
const ModuleName = "fakeout"

// Range-determination modes; see the package doc.
const (
	RangeModeTight   = "tight"
	RangeModeExtreme = "extreme"
)

// Module implements the fakeout signal module. The zero value is usable.
type Module struct{}

// New returns a module instance.
func New() *Module { return &Module{} }

// Name implements modules.SignalModule.
func (m *Module) Name() string { return ModuleName }

// Description implements modules.SignalModule.
func (m *Module) Description() string {
	return "检测盘整区间的假突破：先在最近的一段行情里识别出一个区间，把区间高点当" +
		"'前高'、低点当'前低'，再看最近几根 K 线是否突破了区间高/低点后又很快收回——" +
		"收回则判定为假突破。区间怎么定由 range_mode 决定：tight（默认）要求高低点" +
		"幅度够紧凑，自动排除趋势行情，但跨度很长的盘整容易被这个固定容差提前截断；" +
		"extreme 不判断紧凑度，直接取回看窗口内的最高/最低价，适合说不清该量化成" +
		"多少根K线、但确实拖了很久的盘整。只关注一个离当前最近的区间，不产出大量" +
		"细碎的关键位。"
}

// RequiredParams implements modules.SignalModule.
func (m *Module) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{
		{
			Name: "range_mode", Type: types.ParamString, Default: RangeModeTight,
			Enum: []string{RangeModeTight, RangeModeExtreme},
			Description: "tight：高低点幅度必须在 range_tightness 容差内才算盘整，自动排除" +
				"趋势行情，但很长的盘整容易被固定容差提前截断；extreme：不判断紧凑度，" +
				"直接取 range_lookback 整个回看窗口内的最高/最低价当前高/前低，适合跨度很长、" +
				"说不清该量化成多少根K线的盘整，代价是趋势行情也会被老实报出区间高低点。",
		},
		{
			Name: "range_lookback", Type: types.ParamInt, Default: 60,
			Min: types.F(10), Max: types.F(300),
			Description: "向前搜索盘整区间的最大根数（不含用于扫描假突破的最近几根）。" +
				"extreme 模式下这就是实际用来取最高/最低价的窗口大小，不会再收窄。",
		},
		{
			Name: "min_range_bars", Type: types.ParamInt, Default: 10,
			Min: types.F(3), Max: types.F(200),
			Description: "构成一次有效盘整区间最少需要多少根 K 线；不足这个数量不算盘整，" +
				"不会产生可监控的区间高低点。",
		},
		{
			Name: "range_tightness", Type: types.ParamFloat, Default: 0.03,
			Min: types.F(0.002), Max: types.F(0.2),
			Description: "判定'盘整'的松紧度：区间最高价与最低价之差相对区间中枢价格的比例，" +
				"超过这个比例就不算横盘（说明还在趋势里），值越小要求盘整得越紧。" +
				"range_mode 为 extreme 时这个参数不生效。",
		},
		{
			Name: "breakout_confirm", Type: types.ParamFloat, Default: 0.001,
			Min: types.F(0), Max: types.F(0.05),
			Description: "突破确认幅度：收盘价要越过区间高/低点这个比例才算真正突破，用于过滤刺破。",
		},
		{
			Name: "reversal_window", Type: types.ParamInt, Default: 5,
			Min: types.F(1), Max: types.F(20),
			Description: "假突破判定窗口：突破发生后最多几根 K 线内收回才算假突破，超过这个窗口" +
				"再收回不算（此时更像是趋势延续后的正常回调，而不是这次突破本身失败了）。",
		},
		{
			Name: "reversal_confirm", Type: types.ParamFloat, Default: 0.001,
			Min: types.F(0), Max: types.F(0.05),
			Description: "收回确认幅度：最新收盘价要跌回/涨回区间高/低点这个比例以内才算确认收回，" +
				"跟 breakout_confirm 是两个独立的阈值，分别控制突破和收回各自的确认严格度。",
		},
	}
}

// Event types, written to Signal.Raw["event"].
const (
	eventFakeoutResistance = "fakeout_resistance" // false breakout above the range high, then reversed -> bearish
	eventFakeoutSupport    = "fakeout_support"    // false breakdown below the range low, then reversed -> bullish
	eventNone              = "none"
)

// consolidationRange is an identified consolidation range.
type consolidationRange struct {
	High decimal.Decimal
	Low  decimal.Decimal
	Bars int
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

	rangeMode := types.MustString(p, "range_mode")
	rangeLookback := types.MustInt(p, "range_lookback")
	minRangeBars := types.MustInt(p, "min_range_bars")
	rangeTightness := decimal.NewFromFloat(types.MustFloat(p, "range_tightness"))
	breakoutConfirm := decimal.NewFromFloat(types.MustFloat(p, "breakout_confirm"))
	reversalWindow := types.MustInt(p, "reversal_window")
	reversalConfirm := decimal.NewFromFloat(types.MustFloat(p, "reversal_confirm"))

	neutral := func(reason string, raw map[string]any) types.Signal {
		s := types.NeutralSignal(ModuleName, md.Symbol, reason, md.Time())
		if last, ok := md.Last(); ok {
			s.Price = last.Close
		}
		s.Raw = raw
		return s
	}
	notFound := map[string]any{"event": eventNone, "range_found": false}

	// The scan window is "the candles where a breakout might have happened" +
	// the current candle (used to determine whether it has already reversed back).
	scanSize := reversalWindow + 1
	if len(md.Candles) < minRangeBars+scanSize {
		return neutral(fmt.Sprintf("K 线不足：需要至少 %d 根，实际 %d 根",
			minRangeBars+scanSize, len(md.Candles)), notFound), nil
	}

	n := len(md.Candles)
	cur := md.Candles[n-1]
	if !cur.Close.IsPositive() {
		return neutral("最新收盘价非正，数据异常", notFound), nil
	}
	// The candles within the scan window where a breakout might have happened (excluding the current candle).
	scanCandles := md.Candles[n-scanSize : n-1]

	// The consolidation range is computed only from history before the scan
	// window, to keep the breakout event we're currently trying to detect
	// from leaking into the range calculation.
	levelHistory := md.Candles[:n-scanSize]
	if len(levelHistory) > rangeLookback {
		levelHistory = levelHistory[len(levelHistory)-rangeLookback:]
	}
	// Carry out "how far back this actually looked" even when no range was
	// found — the chart uses it to mark the analysis window's start, so the
	// user can see "the system looked at this much history but found
	// nothing", rather than assuming it didn't look at all.
	windowStart := map[string]any{"window_start": levelHistory[0].OpenTime.Format(time.RFC3339)}

	var rng consolidationRange
	var found bool
	if rangeMode == RangeModeExtreme {
		rng, found = extremeRange(levelHistory, minRangeBars)
	} else {
		rng, found = findConsolidationRange(levelHistory, minRangeBars, rangeTightness)
	}
	if !found {
		raw := map[string]any{"event": eventNone, "range_found": false}
		for k, v := range windowStart {
			raw[k] = v
		}
		return neutral("未在回看窗口内找到有效盘整区间（价格波动幅度或维持时间不满足要求）", raw), nil
	}

	// Once a range is found, window_start is switched to point at that
	// range's actual start (the last rng.Bars candles of levelHistory)
	// instead of the entire rangeLookback window's start — rng.High/rng.Low
	// were only computed over those candles within the range, so if we kept
	// reporting "looked back rangeLookback candles", the analysis-window
	// marker drawn on the chart would be wider than the range itself, and the
	// user would assume the "prior high" should cover all the way out to
	// where the marker sits — but the history outside the marker, before the
	// range's actual start (even if it contains a taller wick), was never
	// actually counted into the "prior high" at all. This is exactly a bug
	// that really happened before: "the prior high didn't capture the true
	// max of the whole consolidation zone" — the root cause was this marker
	// not matching the actual computation range, not rng.High being computed wrong.
	rangeStart := levelHistory[len(levelHistory)-rng.Bars]
	windowStart = map[string]any{"window_start": rangeStart.OpenTime.Format(time.RFC3339)}

	rangeInfo := map[string]any{
		"range_high": rng.High.String(),
		"range_low":  rng.Low.String(),
		"range_bars": rng.Bars,
	}
	for k, v := range windowStart {
		rangeInfo[k] = v
	}

	one := decimal.NewFromInt(1)
	upBreakout := rng.High.Mul(one.Add(breakoutConfirm))
	downReclaim := rng.High.Mul(one.Sub(reversalConfirm))
	downBreakout := rng.Low.Mul(one.Sub(breakoutConfirm))
	upReclaim := rng.Low.Mul(one.Add(reversalConfirm))

	var event string
	var dir types.Direction
	var levelPrice, breakoutClose decimal.Decimal

	if maxClose, ok := maxCloseAbove(scanCandles, upBreakout); ok && cur.Close.LessThan(downReclaim) {
		event, dir, levelPrice, breakoutClose = eventFakeoutResistance, types.DirectionShort, rng.High, maxClose
	} else if minClose, ok := minCloseBelow(scanCandles, downBreakout); ok && cur.Close.GreaterThan(upReclaim) {
		event, dir, levelPrice, breakoutClose = eventFakeoutSupport, types.DirectionLong, rng.Low, minClose
	}

	if event == "" {
		raw := map[string]any{"event": eventNone, "range_found": true}
		for k, v := range rangeInfo {
			raw[k] = v
		}
		return neutral("找到盘整区间，但最近未出现'突破后又收回'的假突破模式", raw), nil
	}

	raw := map[string]any{
		"event":          event,
		"level_price":    levelPrice.String(),
		"breakout_close": breakoutClose.String(),
		"reclaim_close":  cur.Close.String(),
		"range_found":    true,
	}
	for k, v := range rangeInfo {
		raw[k] = v
	}

	return types.Signal{
		Module:     ModuleName,
		Symbol:     md.Symbol,
		Direction:  dir,
		Confidence: confidenceFor(rng.Bars, minRangeBars),
		Timestamp:  cur.CloseTime,
		Price:      cur.Close,
		Reason:     reasonFor(event, levelPrice, rng.Bars, breakoutClose, cur.Close),
		Raw:        raw,
	}, nil
}

// findConsolidationRange expands the window backward from the end of
// history as far as possible, finding the longest segment closest to the
// current point whose spread still stays within tightness.
//
// As the window expands, the range high can only get higher and the low can
// only get lower, so their difference is monotonically non-decreasing; as
// long as price stays positive, the spread's ratio to the midpoint price is
// therefore also monotonically non-decreasing (provable: holding the low
// fixed and raising the high, the sign of the ratio's derivative with
// respect to the new high equals the low itself, always positive; holding
// the high fixed and lowering the low is symmetric). So "stop the first time
// the ratio exceeds the threshold" is guaranteed to find the longest valid
// window, with no need to backtrack and retry.
func findConsolidationRange(history []types.Candle, minBars int, tightness decimal.Decimal) (consolidationRange, bool) {
	n := len(history)
	if n < minBars {
		return consolidationRange{}, false
	}

	var best consolidationRange
	found := false
	var hi, lo decimal.Decimal

	for bars := 1; bars <= n; bars++ {
		c := history[n-bars]
		if bars == 1 || c.High.GreaterThan(hi) {
			hi = c.High
		}
		if bars == 1 || c.Low.LessThan(lo) {
			lo = c.Low
		}
		if bars < minBars {
			continue
		}
		mid := hi.Add(lo).Div(decimal.NewFromInt(2))
		if !mid.IsPositive() {
			break
		}
		if hi.Sub(lo).Div(mid).GreaterThan(tightness) {
			break
		}
		best = consolidationRange{High: hi, Low: lo, Bars: bars}
		found = true
	}
	return best, found
}

// extremeRange is the range definition used when range_mode=extreme: it
// doesn't test "tight enough" at all, it just takes the highest/lowest price
// across the entire history (range_lookback candles long) as the prior high/low.
//
// The key difference from findConsolidationRange: the latter stops expanding
// the window early wherever the spread breaks tolerance, so a longer window
// is more likely to get cut off by a single wick; extreme skips that test
// entirely — the user choosing this mode has already decided this whole
// period is a consolidation, so the system just dutifully reports the
// extremes within it.
func extremeRange(history []types.Candle, minBars int) (consolidationRange, bool) {
	n := len(history)
	if n < minBars {
		return consolidationRange{}, false
	}
	hi, lo := history[0].High, history[0].Low
	for _, c := range history[1:] {
		if c.High.GreaterThan(hi) {
			hi = c.High
		}
		if c.Low.LessThan(lo) {
			lo = c.Low
		}
	}
	return consolidationRange{High: hi, Low: lo, Bars: n}, true
}

// maxCloseAbove reports whether any candle in candles closed above threshold, and returns the highest such close.
func maxCloseAbove(candles []types.Candle, threshold decimal.Decimal) (decimal.Decimal, bool) {
	var max decimal.Decimal
	found := false
	for _, c := range candles {
		if c.Close.GreaterThan(threshold) && (!found || c.Close.GreaterThan(max)) {
			max, found = c.Close, true
		}
	}
	return max, found
}

// minCloseBelow reports whether any candle in candles closed below threshold, and returns the lowest such close.
func minCloseBelow(candles []types.Candle, threshold decimal.Decimal) (decimal.Decimal, bool) {
	var min decimal.Decimal
	found := false
	for _, c := range candles {
		if c.Close.LessThan(threshold) && (!found || c.Close.LessThan(min)) {
			min, found = c.Close, true
		}
	}
	return min, found
}

// confidenceFor maps the number of candles the range held for into a [0,1]
// confidence: the longer it held, the more solid the range, and the more
// credible the fakeout determination. Baseline 0.55, with a full 0.3 added
// for every 20 candles beyond min_range_bars, capped at 0.95.
func confidenceFor(bars, minBars int) float64 {
	extra := float64(bars-minBars) / 20.0
	c := 0.55 + 0.3*extra
	if c > 0.95 {
		c = 0.95
	}
	if c < 0.55 {
		c = 0.55
	}
	return c
}

func reasonFor(event string, levelPrice decimal.Decimal, rangeBars int, breakoutClose, reclaimClose decimal.Decimal) string {
	switch event {
	case eventFakeoutResistance:
		return fmt.Sprintf("识别到 %d 根 K 线构成的盘整区间，其高点 %s 曾被收盘价 %s 突破，随后收回至 %s，判定为假突破",
			rangeBars, levelPrice, breakoutClose, reclaimClose)
	case eventFakeoutSupport:
		return fmt.Sprintf("识别到 %d 根 K 线构成的盘整区间，其低点 %s 曾被收盘价 %s 跌破，随后收回至 %s，判定为假跌破",
			rangeBars, levelPrice, breakoutClose, reclaimClose)
	default:
		return "无事件"
	}
}
