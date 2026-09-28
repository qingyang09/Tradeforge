package okx

import (
	"context"
	"fmt"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"tradeforge/pkg/types"
)

// wsPushMessage 覆盖 OKX WebSocket 在 candle 频道上会推送的两类消息：订阅确认
// （Event 非空）和行情推送（Arg/Data 非空）。两类消息共用同一个反序列化目标，
// 按哪些字段非空来区分，这是已经用真实连接验证过的形状。
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

// Subscribe 订阅某个标的的实时 K 线，只把已收盘（confirm）的推给调用方。
//
// 未收盘的中途更新会被直接丢弃：拿一根还在变化的 K 线去触发决策，等于用未来会变的
// 数据做当下的判断，这正是回测引擎一直小心避免的前视偏差，实时链路上同样不能踩。
//
// 连接断开时返回的 channel 会被关闭，这一层不自动重连——重连退避策略是调用方的事
// （cmd/signal-engine 里的做法，跟 messaging.DecisionReader 读失败后由 cmd/executor
// 自己重试是同一个分工）。
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
		return nil, fmt.Errorf("连接 OKX WebSocket 失败：%w", err)
	}

	sub := map[string]any{
		"op": "subscribe",
		"args": []map[string]string{
			{"channel": "candle" + bar, "instId": instID},
		},
	}
	if err := wsjson.Write(ctx, conn, sub); err != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, fmt.Errorf("发送订阅请求失败：%w", err)
	}

	// 订阅失败（比如 instId 拼错）要在这里就报出来，不能让调用方拿着一个
	// 永远不会有数据的 channel 白等。
	var ack wsPushMessage
	if err := wsjson.Read(ctx, conn, &ack); err != nil {
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, fmt.Errorf("读取订阅确认失败：%w", err)
	}
	if ack.Event == "error" {
		conn.Close(websocket.StatusNormalClosure, "")
		return nil, fmt.Errorf("OKX 拒绝订阅（code=%s）：%s", ack.Code, ack.Msg)
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
				continue // 非行情推送（比如心跳），跳过
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
