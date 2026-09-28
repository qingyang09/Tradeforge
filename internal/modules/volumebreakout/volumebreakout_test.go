package volumebreakout

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// flat 构造 n 根等量的基准 K 线，成交量固定为 volume。
func flat(n int, price, volume float64) *synth.Builder {
	b := synth.New("BTCUSDT", types.TF1h, base)
	for i := 0; i < n; i++ {
		b.AddBar(price, price, volume, 0.5)
	}
	return b
}

func TestBullishBreakoutOnVolumeSurge(t *testing.T) {
	b := flat(20, 100, 1000)
	b.AddBar(100, 105, 3000, 0.7) // 3 倍量且收阳

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 20, "multiplier": 2.0,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Fatalf("方向 = %s，期望 LONG（原因：%s）", sig.Direction, sig.Reason)
	}
	if ratio := sig.Raw["ratio"].(float64); ratio < 2.99 || ratio > 3.01 {
		t.Errorf("倍数 = %v，期望约 3.0", ratio)
	}
	if sig.Confidence < 0.5 || sig.Confidence > 0.95 {
		t.Errorf("置信度 %v 超出 [0.5, 0.95]", sig.Confidence)
	}
}

func TestBearishBreakoutOnVolumeSurge(t *testing.T) {
	b := flat(20, 100, 1000)
	b.AddBar(100, 95, 5000, 0.3) // 5 倍量且收阴

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"window": 20})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionShort {
		t.Fatalf("方向 = %s，期望 SHORT（原因：%s）", sig.Direction, sig.Reason)
	}
}

// 量能没到阈值就必须是中性，哪怕价格涨得很凶。
func TestBelowThresholdIsNeutral(t *testing.T) {
	b := flat(20, 100, 1000)
	b.AddBar(100, 120, 1500, 0.9) // 只有 1.5 倍量

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"multiplier": 2.0})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL", sig.Direction)
	}
	if sig.Confidence != 0 {
		t.Errorf("置信度 = %v，期望 0", sig.Confidence)
	}
}

// 置信度必须随倍数单调不减，否则"放量越猛信号越弱"这种反直觉行为会误导下游加权。
func TestConfidenceIncreasesWithRatio(t *testing.T) {
	prev := 0.0
	for _, vol := range []float64{2000, 3000, 5000, 10000, 50000} {
		b := flat(20, 100, 1000)
		b.AddBar(100, 105, vol, 0.7)
		sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"multiplier": 2.0})
		if err != nil {
			t.Fatal(err)
		}
		if sig.Confidence < prev {
			t.Errorf("成交量 %v 时置信度 %v 低于上一档的 %v", vol, sig.Confidence, prev)
		}
		if sig.Confidence > 0.95 {
			t.Errorf("置信度 %v 超出上限 0.95", sig.Confidence)
		}
		prev = sig.Confidence
	}
}

// 均量为零时任何倍数都是无穷大，必须判为无法计算而不是触发信号。
func TestZeroAverageVolumeIsNeutral(t *testing.T) {
	b := flat(20, 100, 0)
	b.AddBar(100, 105, 5000, 0.7)

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"window": 20})
	if err != nil {
		t.Fatalf("均量为零属于数据问题，应返回中性而不是报错，得到：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL", sig.Direction)
	}
}

func TestInsufficientDataReturnsNeutral(t *testing.T) {
	b := flat(5, 100, 1000)
	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"window": 20})
	if err != nil {
		t.Fatalf("数据不足不应报错，得到：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL", sig.Direction)
	}
}

// 十字星式的放量方向不明，min_body_ratio 应把它过滤掉。
func TestDojiFilteredByBodyRatio(t *testing.T) {
	b := flat(20, 100, 1000)
	// 上下影线很长、实体极小：开 100 收 100.05，全幅 10。
	b.Add(100, 105, 95, 100.05, 5000, 0.5)

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 20, "multiplier": 2.0, "min_body_ratio": 0.3,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL（实体占比过滤应生效）", sig.Direction)
	}

	// 关掉过滤后，同一根 K 线应当能给出方向。
	sig2, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 20, "multiplier": 2.0, "min_body_ratio": 0.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sig2.Direction != types.DirectionLong {
		t.Errorf("关闭过滤后方向 = %s，期望 LONG", sig2.Direction)
	}
}

func TestTakerDirectionSource(t *testing.T) {
	// 收阳但主动卖出占优：两种方向来源应给出相反结论，证明参数确实生效。
	b := flat(20, 100, 1000)
	b.AddBar(100, 105, 4000, 0.2)

	byCandle, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 20, "direction_source": SourceCandle,
	})
	if err != nil {
		t.Fatal(err)
	}
	byTaker, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 20, "direction_source": SourceTaker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if byCandle.Direction != types.DirectionLong {
		t.Errorf("按 K 线判定方向 = %s，期望 LONG", byCandle.Direction)
	}
	if byTaker.Direction != types.DirectionShort {
		t.Errorf("按主动买卖量判定方向 = %s，期望 SHORT", byTaker.Direction)
	}
}

// 数据源没提供主动买入量时，净差恒为负会造出虚构的看空信号，必须识别出来。
func TestTakerSourceWithMissingDataIsNeutral(t *testing.T) {
	b := flat(20, 100, 1000)
	b.AddBar(100, 105, 4000, 0.0) // TakerBuyVolume 为 0，模拟字段缺失

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 20, "direction_source": SourceTaker,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL（缺少主动买入量时不得凭空判空）", sig.Direction)
	}
}

func TestInvalidParamsRejected(t *testing.T) {
	b := flat(30, 100, 1000)
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"倍数低于下限", map[string]any{"multiplier": 0.5}},
		{"窗口越界", map[string]any{"window": 99999}},
		{"窗口非整数", map[string]any{"window": 20.5}},
		{"枚举外的方向来源", map[string]any{"direction_source": "orderbook"}},
		{"未知参数", map[string]any{"threshold": 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New().Evaluate(context.Background(), b.Build(), tc.params); err == nil {
				t.Fatalf("期望拒绝 %v，实际通过了", tc.params)
			}
		})
	}
}

func TestContextCancellationRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := flat(30, 100, 1000)
	if _, err := New().Evaluate(ctx, b.Build(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，得到：%v", err)
	}
}

// 均量不含最新一根：否则 window 越小，当前这根越会稀释自己的倍数。
func TestAverageExcludesCurrentCandle(t *testing.T) {
	b := flat(10, 100, 1000)
	b.AddBar(100, 105, 10000, 0.7)

	sig, err := New().Evaluate(context.Background(), b.Build(), map[string]any{"window": 10})
	if err != nil {
		t.Fatal(err)
	}
	// 含当前根时均量为 (10*1000+10000)/11 ≈ 1818，倍数约 5.5；
	// 不含时均量为 1000，倍数正好 10。
	if ratio := sig.Raw["ratio"].(float64); ratio < 9.99 || ratio > 10.01 {
		t.Errorf("倍数 = %v，期望 10.0（说明均量把当前根算进去了）", ratio)
	}
}
