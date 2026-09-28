package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"tradeforge/internal/marketdata"
	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

// CSV round-trip: write it out then read it back, prices must match exactly
// (no precision loss through float).
func TestCSVRoundTripPreservesPrecision(t *testing.T) {
	md := synth.New("BTCUSDT", types.TF1h, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)).
		Add(1234.56789012, 1300.1, 1200.2, 1250.98765432, 1000.5, 0.6).
		Add(1250.98765432, 1400, 1240, 1380.00000001, 2000.25, 0.7).
		Build()

	var buf bytes.Buffer
	if err := marketdata.WriteCSV(&buf, md); err != nil {
		t.Fatal(err)
	}
	back, err := marketdata.ReadCSV(&buf, "BTCUSDT", types.TF1h)
	if err != nil {
		t.Fatal(err)
	}

	if len(back.Candles) != len(md.Candles) {
		t.Fatalf("candle count after round-trip = %d, want %d", len(back.Candles), len(md.Candles))
	}
	for i := range md.Candles {
		if !back.Candles[i].Close.Equal(md.Candles[i].Close) {
			t.Errorf("candle %d close after round-trip = %s, want %s",
				i, back.Candles[i].Close, md.Candles[i].Close)
		}
		if !back.Candles[i].Volume.Equal(md.Candles[i].Volume) {
			t.Errorf("candle %d volume after round-trip = %s, want %s",
				i, back.Candles[i].Volume, md.Candles[i].Volume)
		}
	}
}

// Duplicate or out-of-order timestamps would make the backtest count the
// same candle twice, so they must be rejected.
func TestCSVRejectsDuplicateTimestamps(t *testing.T) {
	csv := "open_time,open,high,low,close,volume\n" +
		"2025-01-01T00:00:00Z,100,101,99,100,1000\n" +
		"2025-01-01T00:00:00Z,100,101,99,100,1000\n"
	if _, err := marketdata.ReadCSV(strings.NewReader(csv), "BTCUSDT", types.TF1h); err == nil {
		t.Fatal("expected duplicate timestamps to be rejected")
	}
}
