package types

import (
	"testing"
	"time"
)

func alignCandle(closeTime time.Time) Candle {
	return Candle{OpenTime: closeTime.Add(-time.Hour), CloseTime: closeTime}
}

func TestAlignAsOfEmptyInput(t *testing.T) {
	got := AlignAsOf(nil, time.Now())
	if len(got) != 0 {
		t.Errorf("空输入应返回空切片，实际 %d 根", len(got))
	}
}

func TestAlignAsOfCutoffBeforeAllCandles(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := []Candle{alignCandle(base), alignCandle(base.Add(time.Hour))}
	got := AlignAsOf(candles, base.Add(-time.Minute))
	if len(got) != 0 {
		t.Errorf("cutoff 早于全部 K 线时应返回空切片，实际 %d 根", len(got))
	}
}

func TestAlignAsOfCutoffAfterAllCandles(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := []Candle{alignCandle(base), alignCandle(base.Add(time.Hour))}
	got := AlignAsOf(candles, base.Add(24*time.Hour))
	if len(got) != 2 {
		t.Errorf("cutoff 晚于全部 K 线时应返回全部，实际 %d 根", len(got))
	}
}

func TestAlignAsOfCutoffExactlyOnBoundary(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := []Candle{
		alignCandle(base),
		alignCandle(base.Add(time.Hour)),
		alignCandle(base.Add(2 * time.Hour)),
	}
	// cutoff 恰好等于第二根的收盘时间：该根已经真实收盘，必须包含在内（不能因为
	// "严格早于"就漏掉这个边界，触发那一刻自己所在周期的这根 K 线本来就应该可见）。
	got := AlignAsOf(candles, base.Add(time.Hour))
	if len(got) != 2 {
		t.Fatalf("cutoff 恰好等于某根收盘时间时应包含该根，实际返回 %d 根", len(got))
	}
	if !got[len(got)-1].CloseTime.Equal(base.Add(time.Hour)) {
		t.Errorf("最后一根应是 cutoff 对应的那根")
	}
}

func TestAlignAsOfDoesNotLeakFutureCandle(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := []Candle{
		alignCandle(base),
		alignCandle(base.Add(time.Hour)),
		alignCandle(base.Add(2 * time.Hour)),
	}
	// cutoff 落在第二、三根之间：第三根还没收盘，绝不能出现在结果里。
	got := AlignAsOf(candles, base.Add(90*time.Minute))
	if len(got) != 2 {
		t.Fatalf("未收盘的那根不应出现在结果里，实际返回 %d 根", len(got))
	}
}
