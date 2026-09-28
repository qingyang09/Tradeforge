// Package execution implements the multi-symbol execution layer.
//
// Core invariant: execution across different symbols is fully isolated. Every
// strategy that enters LIVE (or PAPER_TRADING) is bound to its own worker, and
// a failure in one symbol — an error, a timeout, even a panic — must never
// affect any other symbol.
package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// OrderRequest is a single order placement request.
type OrderRequest struct {
	StrategyID string
	Symbol     string
	Side       types.OrderSide
	// Quantity is the base-currency amount.
	Quantity decimal.Decimal
	// RefPrice is the reference price at order time (latest close); simulated
	// fills are matched against it.
	RefPrice decimal.Decimal
	// Provenance records what triggered this order. Required.
	Provenance types.OrderProvenance
}

// Broker abstracts an order-placement channel.
//
// The interface exists so "paper trading" and "live trading" run through
// exactly the same code path, branching only at this one layer — this avoids
// a situation where paper trading works but live trading quietly takes a
// different branch.
type Broker interface {
	// Name returns the channel name, written to logs and audit records.
	Name() string
	// Mode reports whether this channel is paper or live. The execution layer
	// uses this for its final gate check.
	Mode() types.TradingMode
	// PlaceOrder places an order and returns the fill result.
	PlaceOrder(ctx context.Context, req OrderRequest) (types.Order, error)
}

// ErrLiveBrokerRequiresLiveState indicates an attempt to run a non-LIVE strategy
// through a live broker channel.
var ErrLiveBrokerRequiresLiveState = errors.New("strategy is not in LIVE state; live order channels may not be used")

// ---------- paper trading channel ----------

// PaperBroker is a simulated matching channel; it never sends a real order to
// an exchange.
type PaperBroker struct {
	// SlippageBps is the simulated slippage, in basis points.
	SlippageBps decimal.Decimal
	// TakerFeeRate is the simulated fee rate.
	TakerFeeRate decimal.Decimal

	mu     sync.Mutex
	orders []types.Order
}

// NewPaperBroker builds a paper channel with conservative default cost
// parameters.
func NewPaperBroker() *PaperBroker {
	return &PaperBroker{
		SlippageBps:  decimal.NewFromInt(5),
		TakerFeeRate: decimal.NewFromFloat(0.0004),
	}
}

// Name implements Broker.
func (b *PaperBroker) Name() string { return "paper" }

// Mode implements Broker.
func (b *PaperBroker) Mode() types.TradingMode { return types.ModePaper }

// PlaceOrder implements Broker: fills immediately at the reference price plus
// slippage.
func (b *PaperBroker) PlaceOrder(_ context.Context, req OrderRequest) (types.Order, error) {
	if !req.RefPrice.IsPositive() {
		return types.Order{}, fmt.Errorf("reference price %s is not positive, cannot simulate a fill", req.RefPrice)
	}
	if !req.Quantity.IsPositive() {
		return types.Order{}, fmt.Errorf("order quantity %s is not positive", req.Quantity)
	}

	// Slippage always works against the trader: buys are pushed up, sells are pushed down.
	slip := req.RefPrice.Mul(b.SlippageBps).Div(decimal.NewFromInt(10000))
	fill := req.RefPrice.Add(slip)
	if req.Side == types.SideSell {
		fill = req.RefPrice.Sub(slip)
	}

	now := time.Now().UTC()
	order := types.Order{
		ID:          idgen.NewUUID(),
		StrategyID:  req.StrategyID,
		Symbol:      req.Symbol,
		Side:        req.Side,
		Type:        types.OrderMarket,
		Mode:        types.ModePaper,
		Quantity:    req.Quantity,
		FilledPrice: fill,
		Fee:         fill.Mul(req.Quantity).Mul(b.TakerFeeRate),
		Status:      types.OrderFilled,
		Provenance:  req.Provenance,
		CreatedAt:   now,
		FilledAt:    now,
	}

	b.mu.Lock()
	b.orders = append(b.orders, order)
	b.mu.Unlock()
	return order, nil
}

// Orders returns every simulated fill placed so far, for tests and display.
func (b *PaperBroker) Orders() []types.Order {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]types.Order, len(b.orders))
	copy(out, b.orders)
	return out
}

// ---------- order persistence ----------

// OrderRecorder writes an order to audit storage.
type OrderRecorder interface {
	RecordOrder(ctx context.Context, order types.Order) error
}

// RiskEventRecorder writes a risk event to audit storage.
type RiskEventRecorder interface {
	RecordRiskEvent(ctx context.Context, ev RiskEvent) error
}

// RiskEvent records a single risk-control trigger.
type RiskEvent struct {
	StrategyID string
	Symbol     string
	// Rule is the name of the triggered rule, e.g. "max_daily_loss".
	Rule string
	// Detail is a snapshot of the data at trigger time.
	Detail map[string]any
	// Action is the action the system took, e.g. "close_and_suspend".
	Action    string
	CreatedAt time.Time
}
