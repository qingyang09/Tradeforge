package okx

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// The following rows are raw data captured from the real OKX API (REST
// /market/candles and the WS candle channel push use the same array
// encoding), not hand-written examples — testing against real data catches
// details like field order and confirm semantics that are easy to get wrong
// from the docs alone.
var (
	realConfirmedRow   = []string{"1787180400000", "69264.7", "69462.5", "69160.6", "69337.1", "333.0511483", "23087731.217311397", "23087731.217311397", "1"}
	realUnconfirmedRow = []string{"1787184000000", "69337.2", "69599.4", "69319.9", "69548.1", "115.18879798", "8006796.381173947", "8006796.381173947", "0"}
)

func TestParseCandleRowConfirmed(t *testing.T) {
	c, confirmed, err := parseCandleRow(realConfirmedRow, types.TF1h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmed {
		t.Error("confirm=1 should parse as closed")
	}

	wantOpenTime := time.UnixMilli(1787180400000).UTC()
	if !c.OpenTime.Equal(wantOpenTime) {
		t.Errorf("OpenTime = %s, want %s", c.OpenTime, wantOpenTime)
	}
	if !c.CloseTime.Equal(wantOpenTime.Add(time.Hour)) {
		t.Errorf("CloseTime = %s, want open time + 1 hour", c.CloseTime)
	}
	if !c.Open.Equal(decimal.RequireFromString("69264.7")) {
		t.Errorf("Open = %s, want 69264.7", c.Open)
	}
	if !c.High.Equal(decimal.RequireFromString("69462.5")) {
		t.Errorf("High = %s, want 69462.5", c.High)
	}
	if !c.Low.Equal(decimal.RequireFromString("69160.6")) {
		t.Errorf("Low = %s, want 69160.6", c.Low)
	}
	if !c.Close.Equal(decimal.RequireFromString("69337.1")) {
		t.Errorf("Close = %s, want 69337.1", c.Close)
	}
	if !c.Volume.Equal(decimal.RequireFromString("333.0511483")) {
		t.Errorf("Volume = %s, want 333.0511483", c.Volume)
	}
	// OKX doesn't provide a taker buy/sell split; this must stay zero, not be guessed at.
	if !c.TakerBuyVolume.IsZero() {
		t.Errorf("TakerBuyVolume = %s, want zero (OKX doesn't provide this data)", c.TakerBuyVolume)
	}
}

func TestParseCandleRowUnconfirmed(t *testing.T) {
	_, confirmed, err := parseCandleRow(realUnconfirmedRow, types.TF1h)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmed {
		t.Error("confirm=0 should not parse as closed")
	}
}

func TestParseCandleRowRejectsTooFewFields(t *testing.T) {
	if _, _, err := parseCandleRow([]string{"1", "2", "3"}, types.TF1h); err == nil {
		t.Fatal("should error when there aren't enough fields")
	}
}

func TestParseCandleRowRejectsMalformedPrice(t *testing.T) {
	row := []string{"1787180400000", "not-a-number", "69462.5", "69160.6", "69337.1", "333.05", "0", "0", "1"}
	if _, _, err := parseCandleRow(row, types.TF1h); err == nil {
		t.Fatal("should error on a malformed price field")
	}
}

func TestParseCandleRowRejectsMalformedTimestamp(t *testing.T) {
	row := []string{"not-a-timestamp", "1", "2", "3", "4", "5", "0", "0", "1"}
	if _, _, err := parseCandleRow(row, types.TF1h); err == nil {
		t.Fatal("should error on a malformed timestamp")
	}
}
