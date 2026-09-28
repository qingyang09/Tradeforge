package types

import (
	"time"

	"github.com/shopspring/decimal"
)

// OrderSide is the order's direction.
type OrderSide string

const (
	// SideBuy is a buy order.
	SideBuy OrderSide = "BUY"
	// SideSell is a sell order.
	SideSell OrderSide = "SELL"
)

// SideFor maps a signal direction to an order side to open a position.
// Neutral has no corresponding open action.
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

// OrderType is the order type. Only market orders are supported in the MVP stage.
type OrderType string

const (
	// OrderMarket is a market order.
	OrderMarket OrderType = "MARKET"
	// OrderLimit is a limit order.
	OrderLimit OrderType = "LIMIT"
)

// OrderStatus is the order's status.
type OrderStatus string

const (
	OrderPending  OrderStatus = "PENDING"
	OrderFilled   OrderStatus = "FILLED"
	OrderRejected OrderStatus = "REJECTED"
	OrderCanceled OrderStatus = "CANCELED"
)

// TradingMode distinguishes whether an order came from paper trading or live trading.
//
// This field must travel with the order all the way through: a paper-trading
// order must never reach the real order-placement channel.
type TradingMode string

const (
	// ModePaper is paper trading — no real orders are placed.
	ModePaper TradingMode = "PAPER"
	// ModeLive is live trading.
	ModeLive TradingMode = "LIVE"
)

// Order is a single order request and its execution result.
//
// Provenance is a mandatory field: every order must be able to answer "which
// module's which signal, with what parameters, triggered this" — that's the
// platform's baseline for explainability.
type Order struct {
	ID         string      `json:"id"`
	StrategyID string      `json:"strategy_id"`
	Symbol     string      `json:"symbol"`
	Side       OrderSide   `json:"side"`
	Type       OrderType   `json:"type"`
	Mode       TradingMode `json:"mode"`

	// Quantity is the base-currency amount (e.g. number of BTC).
	Quantity decimal.Decimal `json:"quantity"`
	// Price is the limit order's order price; zero for a market order.
	Price decimal.Decimal `json:"price,omitempty"`
	// FilledPrice is the actual average fill price.
	FilledPrice decimal.Decimal `json:"filled_price,omitempty"`
	// Fee is this order's fee, denominated in the quote currency.
	Fee decimal.Decimal `json:"fee,omitempty"`

	Status OrderStatus `json:"status"`
	// ExchangeOrderID is the order ID returned by the exchange; empty for paper trading.
	ExchangeOrderID string `json:"exchange_order_id,omitempty"`
	// RejectReason explains why when Status is REJECTED (including risk-control rejections).
	RejectReason string `json:"reject_reason,omitempty"`

	Provenance OrderProvenance `json:"provenance"`

	CreatedAt time.Time `json:"created_at"`
	FilledAt  time.Time `json:"filled_at,omitempty"`
}

// OrderProvenance records the full trigger trail for an order, suitable for
// displaying to the user directly.
type OrderProvenance struct {
	// DecisionID links back to the decision the combination engine wrote to the audit table.
	DecisionID string `json:"decision_id"`
	// Combine is the aggregation mode in effect at the time.
	Combine CombineMode `json:"combine"`
	// Score is the aggregated strength at the time.
	Score float64 `json:"score"`
	// Threshold is the trigger threshold at the time (WEIGHTED mode).
	Threshold float64 `json:"threshold,omitempty"`
	// Signals are each module's signal at the time.
	Signals []Signal `json:"signals"`
	// ModuleParams is a snapshot of the effective parameters for each module at the
	// time, keyed by module name. A snapshot is stored rather than a reference to the
	// strategy config, because the config may be edited afterward.
	ModuleParams map[string]map[string]any `json:"module_params"`
	// Note is a purely factual annotation (e.g. "forced close triggered by risk control").
	Note string `json:"note,omitempty"`
}

// Position is the current holding on a given symbol.
type Position struct {
	StrategyID string          `json:"strategy_id"`
	Symbol     string          `json:"symbol"`
	Direction  Direction       `json:"direction"`
	Quantity   decimal.Decimal `json:"quantity"`
	EntryPrice decimal.Decimal `json:"entry_price"`
	OpenedAt   time.Time       `json:"opened_at"`
	// EntryOrderID links to the order that opened this position, from which Provenance
	// can be traced.
	EntryOrderID string `json:"entry_order_id"`
	// StopLossPrice/TakeProfitPrice are the absolute stop-loss/take-profit prices
	// computed at the moment the position was opened; zero means unset. Regardless
	// of whether RiskConfig uses a fixed percentage or the support_resistance mode,
	// once opened these are converted to and stored as absolute prices — every
	// subsequent candle only needs to compare the current price against these two
	// values, without needing to know which mode originally produced them (see
	// ResolveStopLossPrice in internal/execution/risk.go).
	StopLossPrice   decimal.Decimal `json:"stop_loss_price,omitempty"`
	TakeProfitPrice decimal.Decimal `json:"take_profit_price,omitempty"`
}

// IsOpen reports whether the position holds a nonzero quantity.
func (p Position) IsOpen() bool {
	return p.Direction != DirectionNeutral && p.Quantity.IsPositive()
}

// UnrealizedPnL computes floating P&L at the given price, denominated in the quote currency.
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
