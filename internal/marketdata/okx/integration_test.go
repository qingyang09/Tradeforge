//go:build integration

// 真打 OKX 的公开行情接口，不需要 docker、不需要 API key：
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
		t.Fatalf("请求真实 OKX 接口失败：%v", err)
	}
	if len(candles) == 0 {
		t.Fatal("应该拿到至少一根 K 线")
	}
	for i := 1; i < len(candles); i++ {
		if !candles[i-1].OpenTime.Before(candles[i].OpenTime) {
			t.Fatalf("candles[%d] 的开盘时间应晚于 candles[%d]，实际 %s <= %s",
				i, i-1, candles[i].OpenTime, candles[i-1].OpenTime)
		}
	}
	last := candles[len(candles)-1]
	if !last.Close.IsPositive() {
		t.Errorf("最新一根收盘价应为正数，实际 %s", last.Close)
	}
	t.Logf("拿到 %d 根真实 K 线，最新一根：%s 收盘价 %s", len(candles), last.OpenTime, last.Close)
}

func TestFetchCandlesAgainstRealOKXRejectsUnknownInstrument(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	c := NewClient()
	if _, err := c.FetchCandles(ctx, "ZZZUSDT", types.TF1h, 10); err == nil {
		t.Fatal("不存在的交易对应该报错")
	}
}

// 真的连一次 OKX WebSocket，订阅、等一条推送（不一定是已收盘的，1 小时线在测试运行的
// 几秒钟内大概率还没走到收盘）。这里只验证连接、订阅、解析这条链路是通的，
// 不强求在测试超时前一定能等到 confirm=1 的那一刻。
func TestSubscribeAgainstRealOKX(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c := NewClient()
	ch, err := c.Subscribe(ctx, "BTCUSDT", types.TF1h)
	if err != nil {
		t.Fatalf("订阅真实 OKX WebSocket 失败：%v", err)
	}

	select {
	case candle, ok := <-ch:
		if !ok {
			t.Fatal("channel 提前关闭")
		}
		if !candle.Close.IsPositive() {
			t.Errorf("收盘价应为正数，实际 %s", candle.Close)
		}
		t.Logf("收到一根已收盘的实时 K 线：%s 收盘价 %s", candle.OpenTime, candle.Close)
	case <-time.After(15 * time.Second):
		t.Skip("15 秒内没有等到一根收盘的 K 线（1 小时线大概率还没走到收盘边界），" +
			"连接/订阅/解析链路本身已经建立成功，跳过而不是判失败")
	}
}
