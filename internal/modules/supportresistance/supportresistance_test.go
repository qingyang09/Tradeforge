package supportresistance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func newBuilder() *synth.Builder { return synth.New("BTCUSDT", types.TF1h, base) }

func decimalFromFloat(f float64) decimal.Decimal { return decimal.NewFromFloat(f) }

// 在 100 和 110 之间反复震荡，制造出被多次触及的支撑位与阻力位，
// 最后一根决定性地站上 110 → 应当识别为向上突破。
func TestBreakoutAboveResistance(t *testing.T) {
	b := newBuilder().Oscillate(64, 100, 110, 1000)
	b.AddBar(110, 113, 1500, 0.6) // 收在 113，明显越过 110

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"lookback": 200, "pivot_strength": 1, "tolerance": 0.005,
		"min_touches": 2, "breakout_confirm": 0.001, "proximity": 0.003,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Fatalf("方向 = %s，期望 LONG。原因：%s，raw=%v", sig.Direction, sig.Reason, sig.Raw)
	}
	if got := sig.Raw["event"]; got != eventBreakout {
		t.Errorf("event = %v，期望 %s", got, eventBreakout)
	}
	if sig.Confidence <= 0 || sig.Confidence > 1 {
		t.Errorf("置信度 %v 超出 (0,1]", sig.Confidence)
	}
	if sig.Reason == "" {
		t.Error("Reason 不能为空，可解释性要求每个信号都能说明触发原因")
	}
	ws, ok := sig.Raw["window_start"].(string)
	if !ok || ws == "" {
		t.Fatalf("window_start 应该是非空字符串，供画板标出分析窗口起点，实际 raw=%v", sig.Raw)
	}
	if _, err := time.Parse(time.RFC3339, ws); err != nil {
		t.Errorf("window_start 应该是合法的 RFC3339 时间，实际 %q：%v", ws, err)
	}
}

// 关键位数量不够（min_touches 设得很高）时也应该带上 window_start，
// 让画板知道系统看了哪一段历史，即便什么关键位都没找到。
func TestWindowStartPresentWhenNoLevelsFound(t *testing.T) {
	b := newBuilder().Oscillate(64, 100, 110, 1000)
	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"pivot_strength": 1, "min_touches": 10, // 64 根K线的震荡不可能有关键位被摸到这么多次
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if ws, _ := sig.Raw["window_start"].(string); ws == "" {
		t.Errorf("没找到关键位时也应该带上 window_start，实际 raw=%v", sig.Raw)
	}
}

// 同样的震荡区间，最后一根决定性跌穿 100 → 应当识别为向下跌破。
func TestBreakdownBelowSupport(t *testing.T) {
	b := newBuilder().Oscillate(64, 100, 110, 1000)
	// Oscillate 结束时价格不一定在 100，先拉回到 100 附近再跌穿。
	b.AddBar(b.Build().Candles[b.Len()-1].Close.InexactFloat64(), 100, 1000, 0.45)
	b.AddBar(100, 97, 1500, 0.4)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"pivot_strength": 1, "min_touches": 2,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionShort {
		t.Fatalf("方向 = %s，期望 SHORT。原因：%s，raw=%v", sig.Direction, sig.Reason, sig.Raw)
	}
	if got := sig.Raw["event"]; got != eventBreakdown {
		t.Errorf("event = %v，期望 %s", got, eventBreakdown)
	}
}

// 价格贴着支撑位但没跌破 → 回踩测试，方向看多，且置信度应低于突破。
func TestTestSupportProducesLowerConfidenceThanBreakout(t *testing.T) {
	b := newBuilder().Oscillate(64, 100, 110, 1000)
	last := b.Build().Candles[b.Len()-1].Close.InexactFloat64()
	b.AddBar(last, 100.2, 1000, 0.5) // 收在 100.2，落在 100 的 proximity 内

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"pivot_strength": 1, "min_touches": 2, "proximity": 0.005,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Raw["event"] != eventTestSupp {
		t.Fatalf("event = %v，期望 %s（原因：%s）", sig.Raw["event"], eventTestSupp, sig.Reason)
	}
	if sig.Direction != types.DirectionLong {
		t.Errorf("方向 = %s，期望 LONG", sig.Direction)
	}
	touches, ok := sig.Raw["level_touches"].(int)
	if !ok {
		t.Fatalf("raw.level_touches 类型为 %T，期望 int", sig.Raw["level_touches"])
	}
	if breakout := confidenceFor(eventBreakout, touches); sig.Confidence >= breakout {
		t.Errorf("同为 %d 次触及时，回踩置信度 %v 不应达到突破的 %v", touches, sig.Confidence, breakout)
	}
}

// 边界：K 线数量不足以识别 pivot 时返回中性信号 + nil error，而不是报错。
func TestInsufficientDataReturnsNeutral(t *testing.T) {
	b := newBuilder()
	for i := 0; i < 4; i++ {
		b.AddFlat(100, 100)
	}
	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"pivot_strength": 3})
	if err != nil {
		t.Fatalf("数据不足属于正常情况，不应报错，得到：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL", sig.Direction)
	}
	if sig.Confidence != 0 {
		t.Errorf("中性信号置信度 = %v，期望 0", sig.Confidence)
	}
	if sig.Reason == "" {
		t.Error("中性信号也要说明原因")
	}
}

// 完全没有 K 线时同样返回中性，不能 panic。
func TestEmptyCandles(t *testing.T) {
	sig, err := New().Evaluate(context.Background(), types.MarketData{Symbol: "BTCUSDT"}, nil)
	if err != nil {
		t.Fatalf("空数据不应报错，得到：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL", sig.Direction)
	}
}

// 参数非法必须报错（调用方的问题），而不是悄悄用默认值兜底。
func TestInvalidParamsRejected(t *testing.T) {
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"回看窗口越界", map[string]any{"lookback": 100000}},
		{"容差为负", map[string]any{"tolerance": -0.1}},
		{"未知参数", map[string]any{"magic": 1}},
		{"类型错误", map[string]any{"lookback": "200"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New().Evaluate(context.Background(), b.Build(), tc.params); err == nil {
				t.Fatalf("期望拒绝 %v，实际通过了", tc.params)
			}
		})
	}
}

// 单调上涨的行情里不该冒出被反复触及的关键位，应输出中性。
func TestMonotonicTrendHasNoRepeatedLevels(t *testing.T) {
	b := newBuilder().Trend(80, 100, 200, 1000, 0.6)
	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"min_touches": 3, "pivot_strength": 2,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("单调趋势中方向 = %s，期望 NEUTRAL（raw=%v）", sig.Direction, sig.Raw)
	}
}

func TestContextCancellationRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := newBuilder().Oscillate(40, 100, 110, 1000)
	if _, err := New().Evaluate(ctx, b.Build(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，得到：%v", err)
	}
}

// 聚类必须按相对比例而非绝对价差，否则同一套参数在不同价位的标的上行为不一致。
func TestClusteringIsRelativeNotAbsolute(t *testing.T) {
	tolerance := decimalFromFloat(0.01)

	// 低价标的：100 与 100.5 相差 0.5%，应归为一类。
	low := clusterLevels([]pivot{
		{Price: decimalFromFloat(100), Index: 0},
		{Price: decimalFromFloat(100.5), Index: 1},
	}, tolerance, "high")
	if len(low) != 1 {
		t.Errorf("100 与 100.5 在 1%% 容差下应聚为 1 类，得到 %d 类", len(low))
	}

	// 高价标的：50000 与 50250 同样相差 0.5%，也应归为一类。
	high := clusterLevels([]pivot{
		{Price: decimalFromFloat(50000), Index: 0},
		{Price: decimalFromFloat(50250), Index: 1},
	}, tolerance, "high")
	if len(high) != 1 {
		t.Errorf("50000 与 50250 在 1%% 容差下应聚为 1 类，得到 %d 类", len(high))
	}

	// 相差 5% 则必须分开。
	far := clusterLevels([]pivot{
		{Price: decimalFromFloat(100), Index: 0},
		{Price: decimalFromFloat(105), Index: 1},
	}, tolerance, "high")
	if len(far) != 2 {
		t.Errorf("100 与 105 在 1%% 容差下应分为 2 类，得到 %d 类", len(far))
	}
}

func TestFindPivotsIgnoresPlateaus(t *testing.T) {
	// 连续相等的高点不构成摆动点，否则聚类会虚增触及次数。
	b := newBuilder()
	for _, p := range []float64{100, 101, 105, 105, 105, 101, 100} {
		b.AddFlat(p, 100)
	}
	highs, _ := findPivots(b.Build().Candles, 1)
	if len(highs) != 0 {
		t.Errorf("平台形态不应产出摆动高点，得到 %d 个", len(highs))
	}
}
