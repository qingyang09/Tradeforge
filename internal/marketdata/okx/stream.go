package okx

import (
	"context"
	"fmt"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"tradeforge/pkg/types"
)

// wsPushMessage covers the two message types OKX pushes on the candle
// WebSocket channel: subscription acks (Event non-empty) and market pushes
// (Arg/Data non-empty). Both share the same deserialization target and are
// distinguished by which fields are non-empty — this shape has been verified
// against a real connection.
type wsPushMessage struct {
	Event string `json:"event"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`

	Arg *struct {
		Channel string `json:"channel"`
		InstID  string `json:"instId"`
	} `json:"arg"`
	Data [][]string `json:"data"`
}

// Subscribe subscribes to live candles for a symbol, pushing only closed
// (confirmed) candles to the caller.
//
// Mid-candle updates that haven't closed are dropped outright: triggering a
// decision off a candle that's still changing means basing a current
// judgment on data that will change in the future — exactly the look-ahead
// bias the backtest engine is careful to avoid, and the live path can't
// afford it either.
//
// The returned channel is closed when the connection drops; this layer does
// not auto-reconnect — reconnect/backoff is the caller's responsibility (as
// done in cmd/signal-engine, the same division of labor as
// messaging.DecisionReader leaving retry-on-read-failure to cmd/executor).
func (c *Client) Subscribe(ctx context.Context, symbol string, tf types.Timeframe) (<-chan types.Candle, error) {
	instID, err := ToInstID(symbol)
	if err != nil {
		return nil, err
	}
	bar, err := toBar(tf)
	if err != nil {
		return nil, err
	}

	conn, _, err := websocket.Dial(ctx, c.wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to OKX WebSocket: %w", err)
	}

	sub := map[string]any{
		"op": "subscribe",
		"args": []map[string]string{
			{"channel": "candle" + bar, "instId": instID},
		},
	}
	if err := wsjson.Write(ctx, conn, sub); err != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, fmt.Errorf("failed to send subscribe request: %w", err)
	}

	// A subscription failure (e.g. a typo'd instId) must surface right here
	// — the caller shouldn't be left holding a channel that will never
	// receive data.
	var ack wsPushMessage
	if err := wsjson.Read(ctx, conn, &ack); err != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, fmt.Errorf("failed to read subscription ack: %w", err)
	}
	if ack.Event == "error" {
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, fmt.Errorf("OKX rejected the subscription (code=%s): %s", ack.Code, ack.Msg)
	}

	out := make(chan types.Candle)
	go func() {
		defer close(out)
		defer conn.Close(websocket.StatusNormalClosure, "")
		for {
			var msg wsPushMessage
			if err := wsjson.Read(ctx, conn, &msg); err != nil {
				return
			}
			if msg.Arg == nil || len(msg.Data) == 0 {
				continue // not a market push (e.g. a heartbeat), skip
			}
			for _, row := range msg.Data {
				candle, confirmed, err := parseCandleRow(row, tf)
				if err != nil || !confirmed {
					continue
				}
				select {
				case out <- candle:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}
