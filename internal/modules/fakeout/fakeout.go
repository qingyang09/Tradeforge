// Package fakeout 实现 fakeout 信号模块：先识别一段"盘整区间"，把区间的高点当作
// "前高"、低点当作"前低"，再检测价格是否突破了区间高/低点后又很快收回——这才是
// 假突破。
//
// 不使用逐个摆动点聚类（那会在任意波动上报出一堆细碎的关键位，对"前高假突破"这种
// 描述来说噪音太多），只关注一个明确的、离当前最近的区间。区间怎么定，由
// range_mode 决定，两种互不兼容的语义：
//   - tight（默认）：要求高低点幅度相对中枢价的比例不超过 range_tightness，
//     从当前往回扩，扩到第一次超出容差就停——这是"真的横盘震荡"的判定，
//     会自动排除趋势行情。局限：容差是固定比例，样本越长，出现一根极端影线
//     把高低差撑大的概率天然越高，所以时间跨度很长的盘整反而更容易被这个
//     固定阈值提前截断，抓不到真正的前高/前低。
//   - extreme：不判断"够不够紧凑"，直接取 range_lookback 整个回看窗口内的
//     最高价/最低价当作前高/前低——不管这段行情是不是真的横盘，用户选了这个
//     模式就是在告诉系统"这段我自己认定是盘整，你只管把极值报给我"。适合
//     跨度很长、说不清具体该量化成多少根K线的盘整。代价是不再区分"盘整"和
//     "趋势"：单边趋势行情传进来也会老实报出区间内的最高/最低价。
package fakeout

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ModuleName 是该模块在策略配置中的标识。
const ModuleName = "fakeout"

// 区间判定模式，见包文档。
const (
	RangeModeTight   = "tight"
	RangeModeExtreme = "extreme"
)

// Module 实现 fakeout 信号模块。零值可用。
type Module struct{}

// New 返回模块实例。
func New() *Module { return &Module{} }

// Name 实现 modules.SignalModule。
func (m *Module) Name() string { return ModuleName }

// Description 实现 modules.SignalModule。
func (m *Module) Description() string {
	return "检测盘整区间的假突破：先在最近的一段行情里识别出一个区间，把区间高点当" +
		"'前高'、低点当'前低'，再看最近几根 K 线是否突破了区间高/低点后又很快收回——" +
		"收回则判定为假突破。区间怎么定由 range_mode 决定：tight（默认）要求高低点" +
		"幅度够紧凑，自动排除趋势行情，但跨度很长的盘整容易被这个固定容差提前截断；" +
		"extreme 不判断紧凑度，直接取回看窗口内的最高/最低价，适合说不清该量化成" +
		"多少根K线、但确实拖了很久的盘整。只关注一个离当前最近的区间，不产出大量" +
		"细碎的关键位。"
}

// RequiredParams 实现 modules.SignalModule。
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

// 事件类型，写入 Signal.Raw["event"]。
const (
	eventFakeoutResistance = "fakeout_resistance" // 假突破区间高点，随后收回 → 看空
	eventFakeoutSupport    = "fakeout_support"     // 假跌破区间低点，随后收回 → 看多
	eventNone              = "none"
)

// consolidationRange 是识别出的盘整区间。
type consolidationRange struct {
	High decimal.Decimal
	Low  decimal.Decimal
	Bars int
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

	// 扫描窗口是"突破可能发生的那几根" + 当前这根（用来判定是否已经收回）。
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
	// 扫描窗口内可能发生突破的那几根（不含当前这根）。
	scanCandles := md.Candles[n-scanSize : n-1]

	// 盘整区间只用扫描窗口之前的历史数据算，避免这次要检测的突破事件本身
	// 混进区间计算里。
	levelHistory := md.Candles[:n-scanSize]
	if len(levelHistory) > rangeLookback {
		levelHistory = levelHistory[len(levelHistory)-rangeLookback:]
	}
	// 没找到区间时，把"这次到底往回看了多远"带出去——画板要用它在图上标出分析
	// 窗口的起点，让用户能看到"系统看了这么一段，但没找到"，而不是以为压根没看。
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

	// 找到区间后，window_start 改成指向这个区间实际起点（levelHistory 的最后
	// rng.Bars 根），而不再是整个 rangeLookback 回看窗口的起点——rng.High/rng.Low
	// 只统计了区间内这几根 K 线，要是继续报"看了 rangeLookback 那么远"，图上画出来的
	// 分析窗口标记会比区间本身宽，用户会以为"前高"应该覆盖到标记位置那么远，
	// 但标记之外、区间起点之前的那段历史（哪怕里面有根更高的影线）其实根本没被
	// 算进"前高"——即之前真实复现过的问题："前高抓的不是整个盘整区的最高点"，
	// 根源就是这个标记跟实际计算范围对不上，不是 rng.High 算错了。
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

// findConsolidationRange 从 history 末尾开始尽量往前扩大窗口，找出离当前最近、且
// 波动幅度仍在 tightness 以内的最长一段。
//
// 扩大窗口时区间高点只会越扩越高、低点只会越扩越低，两者之差因此单调不减；
// 只要价格恒为正，波动幅度相对中枢价的比例也随之单调不减（可证明：固定低点、
// 抬高点时，比例对新高点的导数符号等于低点本身，恒为正；固定高点、压低点时同理）。
// 于是"比例第一次超过阈值就停"能保证找到的是最长的有效窗口，不需要回头重试。
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

// extremeRange 是 range_mode=extreme 时用的区间定义：不判断"够不够紧凑"，直接把
// 整个 history（range_lookback 那么长）内的最高价/最低价当作前高/前低。
//
// 跟 findConsolidationRange 的关键区别：后者会在波动幅度撑破容差的地方提前停止
// 扩大窗口，所以窗口越长越容易被一根影线截断；extreme 不做这个判断，用户选了
// 这个模式就是自己认定这一整段是盘整，系统只管把区间内的极值老实报出来。
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

// maxCloseAbove 报告 candles 里是否存在收盘价越过 threshold 的一根，并返回其中最高的收盘价。
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

// minCloseBelow 报告 candles 里是否存在收盘价跌破 threshold 的一根，并返回其中最低的收盘价。
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

// confidenceFor 把区间维持的根数映射为 [0,1] 的置信度：维持得越久，说明这个区间
// 越站得住脚，假突破的判定也就越可信。基准 0.55，每超出 min_range_bars 20 根
// 加满一次 0.3 的额度，上限 0.95。
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
