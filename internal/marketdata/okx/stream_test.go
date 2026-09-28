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

// fakeOKXWSServer spins up a local fake WebSocket server that mimics real OKX
// candle-channel behavior: it acks the subscription request and then pushes
// candle rows in order. This genuinely exercises the accept/read/write
// network path rather than stubbing out the Subscribe function entirely.
func fakeOKXWSServer(t *testing.T, rows [][]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Errorf("accept failed: %v", err)
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()

		// First drain the client's subscription request, then reply with an
		// ack matching real OKX. In the real OKX ack, arg is a single object
		// ({"channel":...,"instId":...}), not the client's args array itself
		// — this matches a real packet capture.
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
		// After pushing everything, wait for the context to end so the
		// client has a chance to finish reading what's already been sent.
		<-ctx.Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSubscribeOnlyEmitsConfirmedCandles(t *testing.T) {
	srv := fakeOKXWSServer(t, [][]string{
		realUnconfirmedRow, // confirm=0, shouldn't show up on the channel
		realUnconfirmedRow, // another mid-candle update for the same candle
		realConfirmedRow,   // confirm=1, should show up
	})
	c := NewClient(WithWSURL(srv.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := c.Subscribe(ctx, "BTCUSDT", types.TF1h)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	select {
	case candle, ok := <-ch:
		if !ok {
			t.Fatal("channel closed early, no closed candle received")
		}
		if !candle.Close.Equal(dec("69337.1")) {
			t.Errorf("received candle close = %s, want 69337.1 (the confirm=1 one)", candle.Close)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out without receiving any candle")
	}
}

func TestSubscribeRejectsUnknownSymbolBeforeDialing(t *testing.T) {
	c := NewClient()
	if _, err := c.Subscribe(context.Background(), "NOTASYMBOL", types.TF1h); err == nil {
		t.Fatal("a symbol with an unrecognized quote currency should be rejected before connecting")
	}
}

func TestSubscribeClosesChannelWhenContextCanceled(t *testing.T) {
	srv := fakeOKXWSServer(t, nil)
	c := NewClient(WithWSURL(srv.URL))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := c.Subscribe(ctx, "BTCUSDT", types.TF1h)
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("should not receive any data after ctx is canceled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("channel should close after ctx is canceled, but timed out waiting")
	}
}
