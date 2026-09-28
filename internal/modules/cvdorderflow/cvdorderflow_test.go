package cvdorderflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func builder() *synth.Builder { return synth.New("BTCUSDT", types.TF1h, base) }

// 持续的主动买入占优 → 多头失衡。
func TestBullishImbalance(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 100.1, 1000, 0.8) // 主动买 80%，卖 20%，净差 +60%
	}

	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "imbalance_threshold": 0.25, "detect": DetectImbalance,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Fatalf("方向 = %s，期望 LONG（原因：%s）", sig.Direction, sig.Reason)
	}
	if sig.Raw["event"] != eventImbalance {
		t.Errorf("event = %v，期望 %s", sig.Raw["event"], eventImbalance)
	}
	if imb := sig.Raw["imbalance"].(float64); imb < 0.59 || imb > 0.61 {
		t.Errorf("失衡度 = %v，期望约 0.6", imb)
	}
}

func TestBearishImbalance(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 99.9, 1000, 0.2)
	}
	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "detect": DetectImbalance,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionShort {
		t.Fatalf("方向 = %s，期望 SHORT（原因：%s）", sig.Direction, sig.Reason)
	}
}

// 买卖大致均衡时不应触发。
func TestBalancedFlowIsNeutral(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 100, 1000, 0.5)
	}
	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{"window": 50})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL（原因：%s）", sig.Direction, sig.Reason)
	}
}

// 价格在涨但主动买盘净流出 → 顶背离，方向跟随 CVD 取空。
func TestBearishDivergence(t *testing.T) {
	b := builder().Trend(60, 100, 120, 1000, 0.35) // 价格上行，主动买入仅 35%

	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "detect": DetectDivergence,
		"divergence_threshold": 0.15, "min_price_move": 0.005,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionShort {
		t.Fatalf("方向 = %s，期望 SHORT（原因：%s，raw=%v）", sig.Direction, sig.Reason, sig.Raw)
	}
	if sig.Raw["event"] != eventDivergence {
		t.Errorf("event = %v，期望 %s", sig.Raw["event"], eventDivergence)
	}
}

// 价格在跌但主动买盘净流入 → 底背离，方向取多。
func TestBullishDivergence(t *testing.T) {
	b := builder().Trend(60, 120, 100, 1000, 0.65)

	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "detect": DetectDivergence,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Fatalf("方向 = %s，期望 LONG（原因：%s，raw=%v）", sig.Direction, sig.Reason, sig.Raw)
	}
}

// 价格与订单流同向不是背离，只检测背离时应给出中性。
func TestSameDirectionIsNotDivergence(t *testing.T) {
	b := builder().Trend(60, 100, 120, 1000, 0.8) // 价涨 + 买盘强，同向

	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "detect": DetectDivergence,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL（同向不构成背离）", sig.Direction)
	}
}

// 价格几乎没动时的"背离"是噪声，min_price_move 应拦住它。
func TestFlatPriceSuppressesDivergence(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 100.001, 1000, 0.2) // 价格几乎不动，买盘极弱
	}
	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "detect": DetectDivergence, "min_price_move": 0.01,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sig.Raw["event"] == eventDivergence {
		t.Errorf("价格几乎没动却报了背离：%s", sig.Reason)
	}
}

func TestInsufficientDataReturnsNeutral(t *testing.T) {
	b := builder()
	for i := 0; i < 10; i++ {
		b.AddBar(100, 101, 1000, 0.8)
	}
	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{"window": 50})
	if err != nil {
		t.Fatalf("数据不足不应报错，得到：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL", sig.Direction)
	}
}

// 行情数据完全没有主动买入量字段时必须报错：
// 照算下去净差恒为负，会凭空造出一串看空信号。
func TestMissingTakerDataIsAnError(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 101, 1000, 0.0)
	}
	_, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{"window": 50})
	if err == nil {
		t.Fatal("期望在缺少主动买入量时报错")
	}
	if !strings.Contains(err.Error(), "主动买入量") {
		t.Errorf("错误信息应说明缺失的是主动买入量，得到：%v", err)
	}
}

// 占位数据源让链路在没有真实订单流时也能跑，但必须在信号里明确标注。
func TestSyntheticProviderIsLabelled(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 101, 1000, 0.0)
	}
	m := New(SyntheticFlowProvider{})
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{"window": 50})
	if err != nil {
		t.Fatalf("占位数据源下不应报错，得到：%v", err)
	}
	if sig.Raw["is_synthetic"] != true {
		t.Error("使用占位数据源时 raw.is_synthetic 必须为 true，否则下游无法拒绝它进入实盘")
	}
	if sig.Raw["provider"] != "synthetic_from_candles" {
		t.Errorf("raw.provider = %v，期望标注为占位数据源", sig.Raw["provider"])
	}
}

// 数据源返回长度不匹配是故障，不能静默按短的那个算。
func TestProviderLengthMismatchIsAnError(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 101, 1000, 0.6)
	}
	m := New(shortProvider{})
	if _, err := m.Evaluate(context.Background(), b.Build(), map[string]any{"window": 50}); err == nil {
		t.Fatal("期望在数据源长度不匹配时报错")
	}
}

type shortProvider struct{}

func (shortProvider) Name() string { return "short" }
func (shortProvider) Deltas(context.Context, types.MarketData) ([]Delta, error) {
	return []Delta{{}}, nil
}

// 数据源报错必须冒泡，不能被吞成中性信号——那会掩盖真实故障。
func TestProviderErrorPropagates(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 101, 1000, 0.6)
	}
	m := New(failingProvider{})
	_, err := m.Evaluate(context.Background(), b.Build(), map[string]any{"window": 50})
	if !errors.Is(err, errProvider) {
		t.Fatalf("期望数据源错误冒泡，得到：%v", err)
	}
}

var errProvider = errors.New("数据源不可用")

type failingProvider struct{}

func (failingProvider) Name() string { return "failing" }
func (failingProvider) Deltas(context.Context, types.MarketData) ([]Delta, error) {
	return nil, errProvider
}

func TestInvalidParamsRejected(t *testing.T) {
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 101, 1000, 0.6)
	}
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"失衡阈值超过 1", map[string]any{"imbalance_threshold": 1.5}},
		{"窗口小于下限", map[string]any{"window": 2}},
		{"检测模式非法", map[string]any{"detect": "everything"}},
		{"未知参数", map[string]any{"lookback": 50}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewDefault().Evaluate(context.Background(), b.Build(), tc.params); err == nil {
				t.Fatalf("期望拒绝 %v，实际通过了", tc.params)
			}
		})
	}
}

func TestContextCancellationRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := builder()
	for i := 0; i < 60; i++ {
		b.AddBar(100, 101, 1000, 0.6)
	}
	if _, err := NewDefault().Evaluate(ctx, b.Build(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，得到：%v", err)
	}
}

// 背离的信息量高于单纯失衡，both 模式下应优先报背离。
func TestDivergenceTakesPrecedenceOverImbalance(t *testing.T) {
	b := builder().Trend(60, 100, 120, 1000, 0.2) // 价涨 + 强烈卖压：两个条件同时成立

	sig, err := NewDefault().Evaluate(context.Background(), b.Build(), map[string]any{
		"window": 50, "detect": DetectBoth,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sig.Raw["event"] != eventDivergence {
		t.Errorf("event = %v，期望优先报 %s", sig.Raw["event"], eventDivergence)
	}
}
