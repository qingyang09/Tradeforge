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

// CSV 往返：写出去再读回来，价格必须完全一致（不得经过 float 丢精度）。
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
		t.Fatalf("往返后 K 线数 = %d，期望 %d", len(back.Candles), len(md.Candles))
	}
	for i := range md.Candles {
		if !back.Candles[i].Close.Equal(md.Candles[i].Close) {
			t.Errorf("第 %d 根收盘价往返后 = %s，期望 %s",
				i, back.Candles[i].Close, md.Candles[i].Close)
		}
		if !back.Candles[i].Volume.Equal(md.Candles[i].Volume) {
			t.Errorf("第 %d 根成交量往返后 = %s，期望 %s",
				i, back.Candles[i].Volume, md.Candles[i].Volume)
		}
	}
}

// 重复或倒序的时间戳会让回测把同一根 K 线算两次，必须拦下。
func TestCSVRejectsDuplicateTimestamps(t *testing.T) {
	csv := "open_time,open,high,low,close,volume\n" +
		"2025-01-01T00:00:00Z,100,101,99,100,1000\n" +
		"2025-01-01T00:00:00Z,100,101,99,100,1000\n"
	if _, err := marketdata.ReadCSV(strings.NewReader(csv), "BTCUSDT", types.TF1h); err == nil {
		t.Fatal("期望拒绝重复时间戳")
	}
}
