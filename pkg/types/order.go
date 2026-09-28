package types

import (
	"time"

	"github.com/shopspring/decimal"
)

// OrderSide 是订单方向。
type OrderSide string

const (
	// SideBuy 买入。
	SideBuy OrderSide = "BUY"
	// SideSell 卖出。
	SideSell OrderSide = "SELL"
)

// SideFor 把信号方向映射为开仓方向。中性方向没有对应的开仓动作。
func SideFor(d Direction) (OrderSide, bool) {
	switch d {
	case DirectionLong:
		return SideBuy, true
	case DirectionShort:
		return SideSell, true
	default:
		return "", false
	}
}

// OrderType 是订单类型。MVP 阶段只支持市价单。
type OrderType string

const (
	// OrderMarket 市价单。
	OrderMarket OrderType = "MARKET"
	// OrderLimit 限价单。
	OrderLimit OrderType = "LIMIT"
)

// OrderStatus 是订单状态。
type OrderStatus string

const (
	OrderPending  OrderStatus = "PENDING"
	OrderFilled   OrderStatus = "FILLED"
	OrderRejected OrderStatus = "REJECTED"
	OrderCanceled OrderStatus = "CANCELED"
)

// TradingMode 区分订单是模拟盘还是实盘产生的。
//
// 这个字段必须随订单一路带到底：模拟盘订单绝不能走到真实下单通道。
type TradingMode string

const (
	// ModePaper 模拟盘，不下真实单。
	ModePaper TradingMode = "PAPER"
	// ModeLive 实盘。
	ModeLive TradingMode = "LIVE"
)

// Order 是一次下单请求及其执行结果。
//
// Provenance 是强制字段：每笔订单都必须能回答"是哪个模块的哪个信号、
// 什么参数触发的"，这是平台的可解释性底线。
type Order struct {
	ID         string      `json:"id"`
	StrategyID string      `json:"strategy_id"`
	Symbol     string      `json:"symbol"`
	Side       OrderSide   `json:"side"`
	Type       OrderType   `json:"type"`
	Mode       TradingMode `json:"mode"`

	// Quantity 是基础货币数量（如 BTC 的个数）。
	Quantity decimal.Decimal `json:"quantity"`
	// Price 是限价单的委托价；市价单为零值。
	Price decimal.Decimal `json:"price,omitempty"`
	// FilledPrice 是实际成交均价。
	FilledPrice decimal.Decimal `json:"filled_price,omitempty"`
	// Fee 是本笔手续费，以计价货币计。
	Fee decimal.Decimal `json:"fee,omitempty"`

	Status OrderStatus `json:"status"`
	// ExchangeOrderID 是交易所返回的订单号，模拟盘为空。
	ExchangeOrderID string `json:"exchange_order_id,omitempty"`
	// RejectReason 在 Status 为 REJECTED 时说明原因（含风控拒绝）。
	RejectReason string `json:"reject_reason,omitempty"`

	Provenance OrderProvenance `json:"provenance"`

	CreatedAt time.Time `json:"created_at"`
	FilledAt  time.Time `json:"filled_at,omitempty"`
}

// OrderProvenance 记录一笔订单的完整触发溯源，可直接展示给用户。
type OrderProvenance struct {
	// DecisionID 关联到组合引擎写入审计表的那条决策。
	DecisionID string `json:"decision_id"`
	// Combine 是当时使用的聚合方式。
	Combine CombineMode `json:"combine"`
	// Score 是当时的聚合强度。
	Score float64 `json:"score"`
	// Threshold 是当时的触发阈值（WEIGHTED 模式）。
	Threshold float64 `json:"threshold,omitempty"`
	// Signals 是当时各模块的信号。
	Signals []Signal `json:"signals"`
	// ModuleParams 是当时各模块生效的参数快照，键为模块名。
	// 保存快照而不是引用策略配置，因为配置可能被后续修改。
	ModuleParams map[string]map[string]any `json:"module_params"`
	// Note 是纯事实性的补充说明（如"风控触发的强制平仓"）。
	Note string `json:"note,omitempty"`
}

// Position 是某个标的上的当前持仓。
type Position struct {
	StrategyID string          `json:"strategy_id"`
	Symbol     string          `json:"symbol"`
	Direction  Direction       `json:"direction"`
	Quantity   decimal.Decimal `json:"quantity"`
	EntryPrice decimal.Decimal `json:"entry_price"`
	OpenedAt   time.Time       `json:"opened_at"`
	// EntryOrderID 关联开仓订单，据此可以追到 Provenance。
	EntryOrderID string `json:"entry_order_id"`
	// StopLossPrice/TakeProfitPrice 是开仓那一刻算好的绝对止损/止盈价格，零值表示未设置。
	// 不管 RiskConfig 用的是固定百分比还是 support_resistance 模式，一旦开仓都会换算成
	// 绝对价格存在这里——后续每根 K 线只需要拿当前价跟这两个价格比较，不需要知道当初
	// 是按哪种模式算出来的（见 internal/execution/risk.go 的 ResolveStopLossPrice）。
	StopLossPrice   decimal.Decimal `json:"stop_loss_price,omitempty"`
	TakeProfitPrice decimal.Decimal `json:"take_profit_price,omitempty"`
}

// IsOpen 报告是否持有非零仓位。
func (p Position) IsOpen() bool {
	return p.Direction != DirectionNeutral && p.Quantity.IsPositive()
}

// UnrealizedPnL 按给定价格计算浮动盈亏，以计价货币计。
func (p Position) UnrealizedPnL(price decimal.Decimal) decimal.Decimal {
	if !p.IsOpen() {
		return decimal.Zero
	}
	diff := price.Sub(p.EntryPrice)
	if p.Direction == DirectionShort {
		diff = diff.Neg()
	}
	return diff.Mul(p.Quantity)
}
