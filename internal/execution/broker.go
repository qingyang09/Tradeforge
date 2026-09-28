// Package execution 实现多标的执行层。
//
// 核心不变量：不同标的的执行完全隔离。每个进入 LIVE（或 PAPER_TRADING）
// 的策略绑定一个独立 worker，一个标的的异常——报错、超时、甚至 panic——
// 都不得影响其它标的。
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

// OrderRequest 是一次下单请求。
type OrderRequest struct {
	StrategyID string
	Symbol     string
	Side       types.OrderSide
	// Quantity 是基础货币数量。
	Quantity decimal.Decimal
	// RefPrice 是下单时的参考价（最新收盘价），模拟成交按它撮合。
	RefPrice decimal.Decimal
	// Provenance 记录这笔单是被什么触发的，必填。
	Provenance types.OrderProvenance
}

// Broker 是下单通道的抽象。
//
// 抽象出接口是为了让"模拟盘"和"实盘"走完全相同的代码路径，
// 只在这一层分叉——避免出现"模拟盘跑通了但实盘走的是另一条分支"。
type Broker interface {
	// Name 返回通道名，写入日志与审计。
	Name() string
	// Mode 返回该通道是模拟盘还是实盘。执行层据此做最后一道拦截。
	Mode() types.TradingMode
	// PlaceOrder 下单并返回成交结果。
	PlaceOrder(ctx context.Context, req OrderRequest) (types.Order, error)
}

// ErrLiveBrokerRequiresLiveState 表示试图用实盘通道执行非 LIVE 状态的策略。
var ErrLiveBrokerRequiresLiveState = errors.New("非 LIVE 状态的策略不得使用实盘下单通道")

// ---------- 模拟盘通道 ----------

// PaperBroker 是模拟撮合通道，永远不会向交易所发出真实订单。
type PaperBroker struct {
	// SlippageBps 是模拟滑点，以基点计。
	SlippageBps decimal.Decimal
	// TakerFeeRate 是模拟手续费率。
	TakerFeeRate decimal.Decimal

	mu     sync.Mutex
	orders []types.Order
}

// NewPaperBroker 用保守的默认成本参数构造模拟通道。
func NewPaperBroker() *PaperBroker {
	return &PaperBroker{
		SlippageBps:  decimal.NewFromInt(5),
		TakerFeeRate: decimal.NewFromFloat(0.0004),
	}
}

// Name 实现 Broker。
func (b *PaperBroker) Name() string { return "paper" }

// Mode 实现 Broker。
func (b *PaperBroker) Mode() types.TradingMode { return types.ModePaper }

// PlaceOrder 实现 Broker：按参考价加滑点立即成交。
func (b *PaperBroker) PlaceOrder(_ context.Context, req OrderRequest) (types.Order, error) {
	if !req.RefPrice.IsPositive() {
		return types.Order{}, fmt.Errorf("参考价 %s 非正，无法模拟成交", req.RefPrice)
	}
	if !req.Quantity.IsPositive() {
		return types.Order{}, fmt.Errorf("下单数量 %s 非正", req.Quantity)
	}

	// 滑点永远对交易者不利：买入上浮、卖出下压。
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

// Orders 返回已模拟成交的全部订单，供测试与展示使用。
func (b *PaperBroker) Orders() []types.Order {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]types.Order, len(b.orders))
	copy(out, b.orders)
	return out
}

// ---------- 订单落库 ----------

// OrderRecorder 把订单写入审计存储。
type OrderRecorder interface {
	RecordOrder(ctx context.Context, order types.Order) error
}

// RiskEventRecorder 把风控事件写入审计存储。
type RiskEventRecorder interface {
	RecordRiskEvent(ctx context.Context, ev RiskEvent) error
}

// RiskEvent 是一次风控触发记录。
type RiskEvent struct {
	StrategyID string
	Symbol     string
	// Rule 是触发的规则名，如 "max_daily_loss"。
	Rule string
	// Detail 是触发时的数据快照。
	Detail map[string]any
	// Action 是系统采取的动作，如 "close_and_suspend"。
	Action    string
	CreatedAt time.Time
}
