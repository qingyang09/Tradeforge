package poc

import (
	"context"
	"testing"
	"time"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func newBuilder() *synth.Builder { return synth.New("BTCUSDT", types.TF1h, base) }

// 构造 5 根价格分散在 100~108 的 K 线，其中价格 104 的那根成交量远大于其它几根之和——
// 算出的 POC 应该落在 104 所在的那个桶附近，而不是别的价位。
func TestPOCLandsOnDominantVolumeBucket(t *testing.T) {
	b := newBuilder()
	b.Add(100, 100, 100, 100, 100, 0.5)
	b.Add(102, 102, 102, 102, 100, 0.5)
	b.Add(104, 104, 104, 104, 100000, 0.5) // 成交量远大于其它几根之和
	b.Add(106, 106, 106, 106, 100, 0.5)
	b.Add(108, 108, 108, 108, 100, 0.5)
	// 补一根收盘价贴着 104 的，用来断言 proximity 判定。
	b.Add(104, 104, 104, 104.1, 100, 0.5)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"lookback": 100, "bucket_count": 8, "proximity": 0.05,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Raw["is_approximate"] != true {
		t.Error("POC 必须标注 is_approximate=true，不能假装是精确值")
	}
	pocPriceStr, _ := sig.Raw["poc_price"].(string)
	if pocPriceStr == "" {
		t.Fatal("Raw 里应该有 poc_price")
	}
	// 桶宽 = (108-100)/8 = 1，POC 应该落在 104 所在的桶（[104,105)）附近。
	if pocPriceStr != "104.5" {
		t.Errorf("poc_price = %s，期望落在 104 所在的桶（104.5，即桶中点）", pocPriceStr)
	}
}

// window_start 要能反映回看窗口的起点，供画板在图上标出"系统正在看这一段历史"。
func TestPOCWindowStartReflectsLookback(t *testing.T) {
	b := newBuilder()
	b.Add(100, 100, 100, 100, 100, 0.5)
	b.Add(102, 102, 102, 102, 100, 0.5)
	b.Add(104, 104, 104, 104, 100000, 0.5)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"lookback": 100, "bucket_count": 8, "proximity": 0.05,
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
		t.Fatalf("window_start 应该是合法的 RFC3339 时间：%v", err)
	}
	// 只有 3 根K线，都在 lookback=100 以内，窗口起点就是第一根的开盘时间。
	if !got.Equal(base) {
		t.Errorf("window_start = %s，期望等于第一根K线的开盘时间 %s", got, base)
	}
}

func TestPOCDirectionByProximity(t *testing.T) {
	b := newBuilder()
	b.Add(100, 100, 100, 100, 100, 0.5)
	b.Add(104, 104, 104, 104, 100000, 0.5)
	b.Add(108, 108, 108, 108, 100, 0.5)
	// 当前收盘价明显高于 POC，且不在 proximity 范围内 → 应为中性。
	b.Add(120, 120, 120, 120, 100, 0.5)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"bucket_count": 5, "proximity": 0.01,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("远离 POC 时应为中性，实际 %s（raw=%v）", sig.Direction, sig.Raw)
	}
}

func TestPOCApproachingFromBelowIsLong(t *testing.T) {
	b := newBuilder()
	b.Add(100, 100, 100, 100, 100000, 0.5) // POC 在 100 附近
	b.Add(110, 110, 110, 110, 100, 0.5)
	b.Add(99.9, 99.9, 99.9, 99.9, 100, 0.5) // 当前收盘价从下方贴近 POC

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{
		"bucket_count": 5, "proximity": 0.05,
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Errorf("从下方触及 POC 应为 LONG，实际 %s（raw=%v）", sig.Direction, sig.Raw)
	}
}

func TestPOCNoWithinLookbackPriceMovementReturnsNeutral(t *testing.T) {
	b := newBuilder()
	b.Add(100, 100, 100, 100, 1000, 0.5) // 单根 K 线，high=low，价格没有波动
	b.Add(100, 100, 100, 100, 1000, 0.5)

	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("价格没有波动时应返回中性信号，实际 %s", sig.Direction)
	}
	if ws, _ := sig.Raw["window_start"].(string); ws == "" {
		t.Error("算不出 POC 时也应该带上 window_start，让画板知道系统看了哪一段")
	}
}

func TestPOCInsufficientDataReturnsNeutral(t *testing.T) {
	b := newBuilder().Add(100, 101, 99, 100, 1000, 0.5)
	m := New()
	sig, err := m.Evaluate(context.Background(), b.Build(), map[string]any{})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("K 线不足时应返回中性信号，实际 %s", sig.Direction)
	}
}

func TestPOCInvalidParamsRejected(t *testing.T) {
	b := newBuilder().Add(100, 101, 99, 100, 1000, 0.5).Add(100, 101, 99, 100, 1000, 0.5)
	m := New()
	if _, err := m.Evaluate(context.Background(), b.Build(), map[string]any{"bucket_count": 1000}); err == nil {
		t.Error("bucket_count 越界应报错")
	}
	if _, err := m.Evaluate(context.Background(), b.Build(), map[string]any{"unknown": 1}); err == nil {
		t.Error("未知参数应报错")
	}
}

func TestPOCContextCancellationRespected(t *testing.T) {
	b := newBuilder().Add(100, 101, 99, 100, 1000, 0.5).Add(100, 101, 99, 100, 1000, 0.5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := New()
	if _, err := m.Evaluate(ctx, b.Build(), map[string]any{}); err == nil {
		t.Error("已取消的 context 应报错")
	}
}
