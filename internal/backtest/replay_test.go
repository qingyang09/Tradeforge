package backtest

import (
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testStrategy() types.StrategyConfig {
	return types.StrategyConfig{
		ID: "11111111-1111-4111-8111-111111111111", Name: "测试",
		Symbol: "BTCUSDT", Timeframe: types.TF1h, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{
			{Module: "support_resistance", Params: map[string]any{"pivot_strength": 1, "min_touches": 2}},
			{Module: "volume_breakout", Params: map[string]any{"window": 20, "multiplier": 2.0}},
			{Module: "cvd_orderflow", Params: map[string]any{"window": 50, "imbalance_threshold": 0.2}},
		},
		Risk:  types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State: types.StateDraft,
	}
}

// structuredData 复刻 gen-testdata 的第一段形态：第 219 根是设计好的触发点。
func structuredData() types.MarketData {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	return synth.New("BTCUSDT", types.TF1h, start).
		Oscillate(200, 100, 110, 1000).
		Trend(19, 100, 110, 1000, 0.85).
		AddBar(110, 113, 3000, 0.85).
		RandomWalk(60, 113, 0.008, 1000, 20250101).
		Build()
}

// 每根 K 线都要有一条决策，且顺序与 K 线一一对应——
// 下游回测靠这个对应关系把信号和价格对齐。
func TestReplayEmitsOneDecisionPerCandle(t *testing.T) {
	md := structuredData()
	meta, decisions, err := Replay(context.Background(), testStrategy(), md, nil, DefaultWindow, quietLogger())
	if err != nil {
		t.Fatalf("重放失败：%v", err)
	}

	if meta.BarCount != len(md.Candles) {
		t.Errorf("meta.bar_count = %d，期望 %d", meta.BarCount, len(md.Candles))
	}
	if len(decisions) != len(md.Candles) {
		t.Fatalf("决策数 = %d，期望 %d", len(decisions), len(md.Candles))
	}
	for i, d := range decisions {
		if d.Index != i {
			t.Fatalf("第 %d 条决策的 index = %d，顺序错乱", i, d.Index)
		}
		if !d.BarTime.Equal(md.Candles[i].CloseTime) {
			t.Fatalf("第 %d 条决策的时间 %s 与 K 线收盘时间 %s 不符",
				i, d.BarTime, md.Candles[i].CloseTime)
		}
	}
}

// 设计好的触发点必须、且只有它触发——这是端到端链路的关键断言。
func TestReplayTriggersAtDesignedPoint(t *testing.T) {
	_, decisions, err := Replay(context.Background(), testStrategy(), structuredData(), nil, DefaultWindow, quietLogger())
	if err != nil {
		t.Fatalf("重放失败：%v", err)
	}

	var triggered []int
	for _, d := range decisions {
		if d.Triggered {
			triggered = append(triggered, d.Index)
		}
	}
	if len(triggered) != 1 || triggered[0] != 219 {
		t.Fatalf("触发点 = %v，期望仅第 219 根（构造的突破+放量+订单流同向那一根）", triggered)
	}

	d := decisions[219]
	if d.Direction != "LONG" {
		t.Errorf("方向 = %s，期望 LONG", d.Direction)
	}
	if len(d.Signals) != 3 {
		t.Fatalf("信号数 = %d，期望 3", len(d.Signals))
	}
	// 可解释性：每个模块都要说明自己为什么给出这个方向。
	for _, s := range d.Signals {
		if s.Direction != types.DirectionLong {
			t.Errorf("模块 %s 方向 = %s，期望 LONG", s.Module, s.Direction)
		}
		if strings.TrimSpace(s.Reason) == "" {
			t.Errorf("模块 %s 没有说明触发原因", s.Module)
		}
	}
	t.Logf("触发点信号明细：")
	for _, s := range d.Signals {
		t.Logf("  %-20s conf=%.3f  %s", s.Module, s.Confidence, s.Reason)
	}
}

// 前视偏差是回测里最致命的错误：第 i 根的决策绝不能受第 i+1 根之后的数据影响。
//
// 验证方法：把某根 K 线之后的数据整段换掉，重放到该根为止的决策必须一字不变。
func TestReplayHasNoLookAheadBias(t *testing.T) {
	full := structuredData()
	cut := 260

	// 构造一份"前 cut 根相同、之后完全不同"的行情。
	altered := types.MarketData{Symbol: full.Symbol, Timeframe: full.Timeframe}
	altered.Candles = append(altered.Candles, full.Candles[:cut]...)
	tail := synth.New("BTCUSDT", types.TF1h, full.Candles[cut].OpenTime).
		Trend(len(full.Candles)-cut, 113, 500, 9999, 0.99).
		Build()
	altered.Candles = append(altered.Candles, tail.Candles...)

	cfg := testStrategy()
	_, base, err := Replay(context.Background(), cfg, full, nil, DefaultWindow, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := Replay(context.Background(), cfg, altered, nil, DefaultWindow, quietLogger())
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < cut; i++ {
		if base[i].Triggered != other[i].Triggered ||
			base[i].Direction != other[i].Direction ||
			base[i].Score != other[i].Score {
			t.Fatalf("第 %d 根的决策因未来数据变化而改变，存在前视偏差：\n  原=%+v\n  改=%+v",
				i, base[i], other[i])
		}
	}
}

// ---------- 多周期：背景周期不能提前泄露未收盘的数据 ----------

// contextHourly1h 构造一段 1 小时 K 线：均量在 1000 附近的平淡走势，除了 spikeAt
// 这一根（spike=true 时）放出 6 倍量的阳线——这根一旦真的被 volume_breakout 看到，
// 会把信号从中性掰成 LONG。用它来检验"这根 K 线真收盘之前，触发周期的决策绝不能
// 因为它而改变"。
func contextHourly1h(hours, spikeAt int, spike bool) types.MarketData {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	b := synth.New("BTCUSDT", types.TF1h, start)
	for i := 0; i < hours; i++ {
		vol := 1000.0
		if spike && i == spikeAt {
			vol = 6000.0
		}
		b.Add(100, 106, 99, 105, vol, 0.5) // 固定的小阳线，方向恒为 LONG，只有放量与否会变
	}
	return b.Build()
}

// triggerQuarterHourly 构造跟 contextHourly1h 同一段时间范围的 15 分钟 K 线，
// 内容本身不参与信号计算（策略唯一的模块跑在 1h 背景周期上），只提供触发节奏。
func triggerQuarterHourly(hours int) types.MarketData {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	b := synth.New("BTCUSDT", types.TF15m, start)
	for i := 0; i < hours*4; i++ {
		b.Add(100, 100.5, 99.5, 100, 250, 0.5)
	}
	return b.Build()
}

func multiTFStrategy() types.StrategyConfig {
	return types.StrategyConfig{
		ID: "44444444-4444-4444-8444-444444444444", Name: "多周期测试",
		Symbol: "BTCUSDT", Timeframe: types.TF15m, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{{
			Module: "volume_breakout", Timeframe: types.TF1h,
			Params: map[string]any{"window": 20, "multiplier": 2.0},
		}},
		Risk:  types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State: types.StateDraft,
	}
}

// 回测可信度的根本前提：触发周期在背景周期那根 K 线真正收盘之前的决策，
// 绝不能因为那根 K 线最终长什么样而改变。
func TestReplayContextTimeframeHasNoLookAheadBias(t *testing.T) {
	const hours = 40
	const spikeAt = 25 // 0-indexed，第 26 根 1h K 线

	trig := triggerQuarterHourly(hours)
	cfg := multiTFStrategy()

	runWithContext := func(ctxMD types.MarketData) []DecisionLine {
		contextFeeds := map[types.Timeframe]types.MarketData{types.TF1h: ctxMD}
		_, decisions, err := Replay(context.Background(), cfg, trig, contextFeeds, DefaultWindow, quietLogger())
		if err != nil {
			t.Fatalf("重放失败：%v", err)
		}
		return decisions
	}

	spikeDecisions := runWithContext(contextHourly1h(hours, spikeAt, true))
	noSpikeDecisions := runWithContext(contextHourly1h(hours, spikeAt, false))

	if len(spikeDecisions) != len(noSpikeDecisions) || len(spikeDecisions) != len(trig.Candles) {
		t.Fatalf("决策数量不一致：spike=%d noSpike=%d trigger K线=%d",
			len(spikeDecisions), len(noSpikeDecisions), len(trig.Candles))
	}

	// 第 spikeAt 根 1h K 线的收盘时间，是它在共享的 15 分钟时间轴上第一次真正收盘的
	// 时刻——对应 15 分钟序列里下标 (spikeAt+1)*4-1 那一根（每小时 4 根 15 分钟）。
	visibleFromIdx := (spikeAt+1)*4 - 1

	for i := 0; i < visibleFromIdx; i++ {
		if spikeDecisions[i].Triggered != noSpikeDecisions[i].Triggered ||
			spikeDecisions[i].Direction != noSpikeDecisions[i].Direction ||
			spikeDecisions[i].Score != noSpikeDecisions[i].Score {
			t.Fatalf("第 %d 根触发K线的决策在放量那根 1h K 线收盘前就已经不同，"+
				"存在跨周期前视偏差：\n  spike=%+v\n  noSpike=%+v",
				i, spikeDecisions[i], noSpikeDecisions[i])
		}
	}

	// 反向健全性检查：真正收盘之后，两边必须能出现差异——否则说明背景周期的数据
	// 压根没被正确对齐进来，测试就是在验证一个空壳。
	differed := false
	for i := visibleFromIdx; i < len(spikeDecisions); i++ {
		if spikeDecisions[i].Triggered != noSpikeDecisions[i].Triggered ||
			spikeDecisions[i].Direction != noSpikeDecisions[i].Direction {
			differed = true
			break
		}
	}
	if !differed {
		t.Fatal("放量 1h K 线收盘之后，两边决策应该出现差异；" +
			"始终一致说明背景周期的数据根本没有被对齐进来，这个测试没有测到真正的行为")
	}
}

// 滑动窗口不应改变决策结果——窗口只是性能优化，不是语义的一部分。
func TestWindowSizeDoesNotChangeDecisions(t *testing.T) {
	md := structuredData()
	cfg := testStrategy()

	_, wide, err := Replay(context.Background(), cfg, md, nil, 0, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	_, narrow, err := Replay(context.Background(), cfg, md, nil, DefaultWindow, quietLogger())
	if err != nil {
		t.Fatal(err)
	}

	if len(wide) != len(narrow) {
		t.Fatalf("决策数不一致：%d vs %d", len(wide), len(narrow))
	}
	for i := range wide {
		if !reflect.DeepEqual(wide[i], narrow[i]) {
			t.Fatalf("第 %d 条决策不一致，窗口大小影响了决策结果：\n  全量=%+v\n  窗口=%+v",
				i, wide[i], narrow[i])
		}
	}
}
