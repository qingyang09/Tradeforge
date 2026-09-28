package fakeout

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func newBuilder() *synth.Builder { return synth.New("BTCUSDT", types.TF1h, base) }

// 在 100~110 间震荡出一个盘整区间，随后一根决定性突破区间高点，再一根收回 —— 应判定为假突破，看空。
func TestFakeoutResistanceProducesShort(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	b.AddBar(110, 113, 1500, 0.6) // 突破：明显越过区间高点
	b.AddBar(113, 105, 1500, 0.4) // 收回：跌回区间高点以下

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.12, "reversal_window": 1,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionShort {
		t.Fatalf("方向 = %s，期望 SHORT。原因：%s，raw=%v", sig.Direction, sig.Reason, sig.Raw)
	}
	if got := sig.Raw["event"]; got != eventFakeoutResistance {
		t.Errorf("event = %v，期望 %s", got, eventFakeoutResistance)
	}
	if got := sig.Raw["range_found"]; got != true {
		t.Errorf("range_found = %v，期望 true", got)
	}
	if sig.Confidence <= 0 || sig.Confidence > 1 {
		t.Errorf("置信度 %v 超出 (0,1]", sig.Confidence)
	}
	if sig.Reason == "" {
		t.Error("Reason 不能为空")
	}
}

// 对称场景：假跌破区间低点，随后收回 → 应判定为看多。
func TestFakeoutSupportProducesLong(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	last := b.Build().Candles[b.Len()-1].Close.InexactFloat64()
	b.AddBar(last, 100, 1000, 0.45) // 拉回到区间内
	b.AddBar(100, 97, 1500, 0.4)    // 跌破：明显跌穿区间低点
	b.AddBar(97, 103, 1500, 0.6)    // 收回：涨回区间低点以上

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.12, "reversal_window": 1,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Fatalf("方向 = %s，期望 LONG。原因：%s，raw=%v", sig.Direction, sig.Reason, sig.Raw)
	}
	if got := sig.Raw["event"]; got != eventFakeoutSupport {
		t.Errorf("event = %v，期望 %s", got, eventFakeoutSupport)
	}
}

// 突破之后一直没有收回（还站在区间外）—— 不该判定为假突破。
func TestNoReversalStaysNeutral(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	b.AddBar(110, 113, 1500, 0.6)
	b.AddBar(113, 114, 1500, 0.6) // 继续站在区间外，没有收回

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.12, "reversal_window": 1,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL（没有收回，不该判定为假突破）。raw=%v", sig.Direction, sig.Raw)
	}
}

// 突破发生在 reversal_window 之外（太久以前）——此时它已经落进区间计算用的历史
// 数据里，不再是"扫描窗口"里的候选突破，不该算作这次的假突破。不论区间是否因此被
// 重新算得更宽，最终都不应该判定出假突破。
func TestBreakoutOutsideReversalWindowIgnored(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	b.AddBar(110, 112, 1500, 0.6) // 突破，之后会被挤出扫描窗口（reversal_window=1 时窗口只有 1 根）
	b.AddBar(112, 108, 1500, 0.4) // 扫描窗口里唯一的一根：已经回到位内，本身没有再次突破
	b.AddBar(108, 105, 1500, 0.4) // 当前这根：继续待在位以下，但这不是"收回"，是从未突破过

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.15, "reversal_window": 1,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL（突破发生在窗口之外）。raw=%v", sig.Direction, sig.Raw)
	}
}

// 盘整区间只应该产出一个高点和一个低点，而不是一堆细碎的关键位。
func TestConsolidationRangeIsSingleHighLow(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	b.AddBar(110, 108, 1500, 0.5) // 收在区间内，不构成突破

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.12, "reversal_window": 1,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Raw["range_found"] != true {
		t.Fatalf("应找到盘整区间，raw=%v", sig.Raw)
	}
	hi, err := decimal.NewFromString(sig.Raw["range_high"].(string))
	if err != nil {
		t.Fatalf("range_high 解析失败：%v", err)
	}
	lo, err := decimal.NewFromString(sig.Raw["range_low"].(string))
	if err != nil {
		t.Fatalf("range_low 解析失败：%v", err)
	}
	if !hi.GreaterThan(lo) {
		t.Errorf("range_high(%s) 应大于 range_low(%s)", hi, lo)
	}
	wantHi := decimal.NewFromFloat(110.2)
	if hi.Sub(wantHi).Abs().GreaterThan(decimal.NewFromFloat(0.5)) {
		t.Errorf("range_high = %s，期望接近 %s", hi, wantHi)
	}
}

// window_start 要能反映"分析窗口的起点"，不管有没有找到区间都要带上——画板靠它
// 在图上标出"系统正在看这一段历史"。
func TestWindowStartReflectsAnalysisWindow(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.12, "reversal_window": 1,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	ws, ok := sig.Raw["window_start"].(string)
	if !ok || ws == "" {
		t.Fatalf("window_start 应该是非空字符串，实际 raw=%v", sig.Raw)
	}
	got, err := time.Parse(time.RFC3339, ws)
	if err != nil {
		t.Fatalf("window_start 应该是合法的 RFC3339 时间，实际 %q：%v", ws, err)
	}
	// reversal_window=1 时扫描窗口占 2 根，剩下 38 根都进了 levelHistory（没有超过
	// 默认 range_lookback=60），所以窗口起点应该正好是第 0 根的开盘时间。
	if !got.Equal(base) {
		t.Errorf("window_start = %s，期望等于第一根K线的开盘时间 %s", got, base)
	}
}

// window_start 找到区间后应该指向区间的实际起点，而不是整个 rangeLookback
// 回看窗口的起点——这是真实复现过的问题：用户反映"前高"抓得不准，排查后发现
// 不是 range_high 算错了（它确实是区间内的最高价），而是画板上标出的"分析窗口
// 起点"比区间本身更靠前，用户以为前高应该覆盖到那个更早的标记位置，但标记
// 和标记之间那段没被算进 range_high 的历史里，恰好藏着一根更高的影线，
// 看起来就像"前高"漏抓了。
func TestWindowStartMatchesFoundRangeNotFullLookback(t *testing.T) {
	b := newBuilder()
	// 前面这几根故意留一根很高的影线（130）——如果它被算进"前高"，区间高点会
	// 远超后面真正紧凑的盘整区，用来验证这根影线确实被排除在外。
	b.Add(100, 130, 99, 101, 1000, 0.5)
	for i := 0; i < 4; i++ {
		b.Add(101, 103, 99, 101, 1000, 0.5)
	}
	tightRangeStart := b.Len() // 真正紧凑的盘整区从这一根开始
	for i := 0; i < 15; i++ {
		b.Add(101, 102, 100, 101, 1000, 0.5)
	}
	for i := 0; i < 6; i++ {
		b.Add(101, 101.5, 100.5, 101, 1000, 0.5) // 扫描窗口，不构成突破
	}

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.03, "reversal_window": 5,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Raw["range_found"] != true {
		t.Fatalf("应找到盘整区间，raw=%v", sig.Raw)
	}

	hi, err := decimal.NewFromString(sig.Raw["range_high"].(string))
	if err != nil {
		t.Fatalf("range_high 解析失败：%v", err)
	}
	if hi.GreaterThan(decimal.NewFromFloat(103)) {
		t.Errorf("range_high = %s，不该把前面那根影线高点 130 算进来（那根不在紧凑盘整区内）", hi)
	}

	ws, ok := sig.Raw["window_start"].(string)
	if !ok || ws == "" {
		t.Fatalf("window_start 应该是非空字符串，实际 raw=%v", sig.Raw)
	}
	got, err := time.Parse(time.RFC3339, ws)
	if err != nil {
		t.Fatalf("window_start 不是合法 RFC3339：%v", err)
	}
	wantStart := base.Add(time.Duration(tightRangeStart) * time.Hour)
	if !got.Equal(wantStart) {
		t.Errorf("window_start = %s，期望等于紧凑盘整区的实际起点 %s（不是整个回看窗口的起点 %s）",
			got, wantStart, base)
	}
}

// range_mode=extreme 不判断紧凑度，应该把整个回看窗口内的最高/最低价都算进去——
// 包括那根在 tight 模式下会被排除在外的影线（用跟上一个测试同样的构造，反过来
// 断言：这次那根 130 的影线应该被算进 range_high）。
func TestExtremeModeIncludesOutlierWick(t *testing.T) {
	b := newBuilder()
	b.Add(100, 130, 99, 101, 1000, 0.5)
	for i := 0; i < 4; i++ {
		b.Add(101, 103, 99, 101, 1000, 0.5)
	}
	for i := 0; i < 15; i++ {
		b.Add(101, 102, 100, 101, 1000, 0.5)
	}
	for i := 0; i < 6; i++ {
		b.Add(101, 101.5, 100.5, 101, 1000, 0.5)
	}

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"range_mode": RangeModeExtreme, "min_range_bars": 10, "reversal_window": 5,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Raw["range_found"] != true {
		t.Fatalf("应找到区间，raw=%v", sig.Raw)
	}
	hi, err := decimal.NewFromString(sig.Raw["range_high"].(string))
	if err != nil {
		t.Fatalf("range_high 解析失败：%v", err)
	}
	if !hi.Equal(decimal.NewFromFloat(130)) {
		t.Errorf("range_high = %s，extreme 模式下应该把回看窗口内的最高价 130 算进来", hi)
	}

	ws, _ := sig.Raw["window_start"].(string)
	got, err := time.Parse(time.RFC3339, ws)
	if err != nil {
		t.Fatalf("window_start 不是合法 RFC3339：%v", err)
	}
	if !got.Equal(base) {
		t.Errorf("window_start = %s，extreme 模式用的是整个回看窗口，应该等于第一根K线的开盘时间 %s", got, base)
	}
}

// range_mode=extreme 时即使是单边趋势行情也应该老实报出区间内的最高/最低价，
// 不像 tight 模式那样会因为"不够紧凑"而拒绝识别出区间——这正是切换到这个模式
// 要付出的代价，用户选了它就是自己认定这段是盘整。
func TestExtremeModeAcceptsTrendingMarket(t *testing.T) {
	b := newBuilder().Trend(40, 100, 200, 1000, 0.5)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"range_mode": RangeModeExtreme, "min_range_bars": 10,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Raw["range_found"] != true {
		t.Errorf("extreme 模式不判断紧凑度，趋势行情也应该识别出区间，raw=%v", sig.Raw)
	}
}

// 单边趋势行情不构成盘整，不应该识别出任何区间。
func TestTrendingMarketHasNoConsolidationRange(t *testing.T) {
	b := newBuilder().Trend(40, 100, 200, 1000, 0.5)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"min_range_bars": 10, "range_tightness": 0.03,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Raw["range_found"] != false {
		t.Errorf("趋势行情不应识别出盘整区间，raw=%v", sig.Raw)
	}
	if ws, _ := sig.Raw["window_start"].(string); ws == "" {
		t.Error("没找到区间时也应该带上 window_start，让画板知道系统看了哪一段")
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL", sig.Direction)
	}
}

func TestInsufficientDataReturnsNeutral(t *testing.T) {
	b := newBuilder().AddFlat(100, 1000).AddFlat(100, 1000)
	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("K 线不足时应返回中性信号，实际 %s", sig.Direction)
	}
}

func TestInvalidParamsRejected(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	m := New()
	if _, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"reversal_window": 100, // 超出 [1,20] 上限
	}); err == nil {
		t.Error("reversal_window 越界应报错")
	}
	if _, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"bogus_param": 1,
	}); err == nil {
		t.Error("未知参数应报错")
	}
}

func TestContextCancellationRespected(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := New()
	if _, err := m.Evaluate(ctx, b.Build(), map[string]any{}); err == nil {
		t.Error("已取消的 context 应报错")
	}
}
