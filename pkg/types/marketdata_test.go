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
		t.Errorf("empty input should return an empty slice, got %d candles", len(got))
	}
}

func TestAlignAsOfCutoffBeforeAllCandles(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := []Candle{alignCandle(base), alignCandle(base.Add(time.Hour))}
	got := AlignAsOf(candles, base.Add(-time.Minute))
	if len(got) != 0 {
		t.Errorf("cutoff before all candles should return an empty slice, got %d candles", len(got))
	}
}

func TestAlignAsOfCutoffAfterAllCandles(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := []Candle{alignCandle(base), alignCandle(base.Add(time.Hour))}
	got := AlignAsOf(candles, base.Add(24*time.Hour))
	if len(got) != 2 {
		t.Errorf("cutoff after all candles should return all of them, got %d candles", len(got))
	}
}

func TestAlignAsOfCutoffExactlyOnBoundary(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := []Candle{
		alignCandle(base),
		alignCandle(base.Add(time.Hour)),
		alignCandle(base.Add(2 * time.Hour)),
	}
	// cutoff exactly equals the second candle's close time: that candle has genuinely
	// closed and must be included (must not drop this boundary case just because the
	// check is "strictly after" — at the moment of triggering, the candle for the
	// current period itself is naturally supposed to be visible).
	got := AlignAsOf(candles, base.Add(time.Hour))
	if len(got) != 2 {
		t.Fatalf("cutoff exactly on a candle's close time should include that candle, got %d", len(got))
	}
	if !got[len(got)-1].CloseTime.Equal(base.Add(time.Hour)) {
		t.Errorf("the last candle should be the one matching cutoff")
	}
}

func TestAlignAsOfDoesNotLeakFutureCandle(t *testing.T) {
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := []Candle{
		alignCandle(base),
		alignCandle(base.Add(time.Hour)),
		alignCandle(base.Add(2 * time.Hour)),
	}
	// cutoff falls between the second and third candles: the third hasn't closed yet
	// and must never appear in the result.
	got := AlignAsOf(candles, base.Add(90*time.Minute))
	if len(got) != 2 {
		t.Fatalf("an unclosed candle must not appear in the result, got %d", len(got))
	}
}
