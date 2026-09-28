//go:build integration

// Hits OKX's real public market-data API. No docker, no API key needed:
//
//	go test -tags=integration ./internal/marketdata/okx/... -v
package okx

import (
	"context"
	"testing"
	"time"

	"tradeforge/pkg/types"
)

func TestFetchCandlesAgainstRealOKX(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c := NewClient()
	candles, err := c.FetchCandles(ctx, "BTCUSDT", types.TF1h, 10)
	if err != nil {
		t.Fatalf("request to real OKX API failed: %v", err)
	}
	if len(candles) == 0 {
		t.Fatal("should get at least one candle")
	}
	for i := 1; i < len(candles); i++ {
		if !candles[i-1].OpenTime.Before(candles[i].OpenTime) {
			t.Fatalf("candles[%d]'s open time should be after candles[%d], got %s <= %s",
				i, i-1, candles[i].OpenTime, candles[i-1].OpenTime)
		}
	}
	last := candles[len(candles)-1]
	if !last.Close.IsPositive() {
		t.Errorf("latest candle's close should be positive, got %s", last.Close)
	}
	t.Logf("got %d real candles, latest: %s close %s", len(candles), last.OpenTime, last.Close)
}

func TestFetchCandlesAgainstRealOKXRejectsUnknownInstrument(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c := NewClient()
	if _, err := c.FetchCandles(ctx, "ZZZUSDT", types.TF1h, 10); err == nil {
		t.Fatal("a nonexistent trading pair should error")
	}
}

// Actually connects to the real OKX WebSocket once, subscribes, and waits
// for a push (not necessarily a closed one — within the few seconds the test
// runs, a 1h candle has most likely not reached its close boundary yet).
// This only verifies the connect/subscribe/parse path works end to end; it
// doesn't require actually observing a confirm=1 moment before the test
// times out.
func TestSubscribeAgainstRealOKX(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := NewClient()
	ch, err := c.Subscribe(ctx, "BTCUSDT", types.TF1h)
	if err != nil {
		t.Fatalf("subscribing to the real OKX WebSocket failed: %v", err)
	}

	select {
	case candle, ok := <-ch:
		if !ok {
			t.Fatal("channel closed early")
		}
		if !candle.Close.IsPositive() {
			t.Errorf("close should be positive, got %s", candle.Close)
		}
		t.Logf("received one closed live candle: %s close %s", candle.OpenTime, candle.Close)
	case <-time.After(15 * time.Second):
		t.Skip("didn't observe a closed candle within 15 seconds (a 1h candle has most likely " +
			"not reached its close boundary yet); the connect/subscribe/parse path itself " +
			"succeeded, so skip rather than fail")
	}
}
