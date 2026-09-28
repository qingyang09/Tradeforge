package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"tradeforge/pkg/types"
)

// fakeOKXWSServer 起一个本地假 WebSocket 服务器，模仿 OKX candle 频道的真实行为：
// 收到订阅请求后回一条确认，然后按 rows 顺序推送 K 线数据。这是真的走一遍
// accept/read/write 的网络路径，不是把 Subscribe 函数整个替换掉。
func fakeOKXWSServer(t *testing.T, rows [][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Errorf("accept 失败：%v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()

		// 先读掉客户端的订阅请求，回一条跟真实 OKX 一致的确认消息。
		// 真实 OKX 的确认消息里 arg 是单个 object（{"channel":...,"instId":...}），
		// 不是客户端发的 args 数组本身——这是照真实抓包结果对齐的。
		var sub struct {
			Args []map[string]string `json:"args"`
		}
		if err := wsjson.Read(ctx, conn, &sub); err != nil {
			return
		}
		var arg any
		if len(sub.Args) > 0 {
			arg = sub.Args[0]
		}
		ack := map[string]any{
			"event":  "subscribe",
			"arg":    arg,
			"connId": "test-conn",
		}
		if err := wsjson.Write(ctx, conn, ack); err != nil {
			return
		}

		for _, row := range rows {
			push := map[string]any{
				"arg":  map[string]string{"channel": "candle1H", "instId": "BTC-USDT"},
				"data": [][]string{row},
			}
			if err := wsjson.Write(ctx, conn, push); err != nil {
				return
			}
		}
		// 推完就等上下文结束，让客户端有机会把已经发出的消息读完。
		<-ctx.Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSubscribeOnlyEmitsConfirmedCandles(t *testing.T) {
	srv := fakeOKXWSServer(t, [][]string{
		realUnconfirmedRow, // confirm=0，不该出现在 channel 里
		realUnconfirmedRow, // 同一根的又一次中途更新
		realConfirmedRow,   // confirm=1，应该出现
	})
	c := NewClient(WithWSURL(srv.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := c.Subscribe(ctx, "BTCUSDT", types.TF1h)
	if err != nil {
		t.Fatalf("Subscribe 失败：%v", err)
	}

	select {
	case candle, ok := <-ch:
		if !ok {
			t.Fatal("channel 提前关闭，没收到任何已收盘的 K 线")
		}
		if !candle.Close.Equal(dec("69337.1")) {
			t.Errorf("收到的 K 线收盘价 = %s，期望 69337.1（confirm=1 的那一根）", candle.Close)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("超时没有收到任何 K 线")
	}
}

func TestSubscribeRejectsUnknownSymbolBeforeDialing(t *testing.T) {
	c := NewClient()
	if _, err := c.Subscribe(context.Background(), "NOTASYMBOL", types.TF1h); err == nil {
		t.Fatal("无法识别计价货币的标的应在建立连接前就被拒绝")
	}
}

func TestSubscribeClosesChannelWhenContextCanceled(t *testing.T) {
	srv := fakeOKXWSServer(t, nil)
	c := NewClient(WithWSURL(srv.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := c.Subscribe(ctx, "BTCUSDT", types.TF1h)
	if err != nil {
		t.Fatalf("Subscribe 失败：%v", err)
	}

	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("ctx 取消后不该再收到任何数据")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后 channel 应该被关闭，但超时未关闭")
	}
}
