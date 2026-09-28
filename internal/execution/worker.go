package execution

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// Worker 是单个策略（也即单个标的）的执行实例。
//
// 一个 Worker 只服务一个策略，内部状态（持仓、风控计数）完全私有。
// 标的之间的隔离就是靠"各自一个 Worker、互不共享状态"实现的。
type Worker struct {
	cfg    types.StrategyConfig
	broker Broker
	risk   *RiskManager
	logger *slog.Logger

	orders OrderRecorder
	events RiskEventRecorder

	mu       sync.Mutex
	position types.Position
	stats    Stats

	// now 可注入，便于测试控制持仓超时等与时间相关的逻辑。
	now func() time.Time
}

// Stats 是 Worker 的运行统计。
type Stats struct {
	DecisionsSeen  int
	OrdersPlaced   int
	OrdersRejected int
	RiskEvents     int
	Errors         int
	RealizedPnL    decimal.Decimal
	Suspended      bool
	SuspendReason  string
}

// WorkerOption 配置 Worker。
type WorkerOption func(*Worker)

// WithOrderRecorder 设置订单落库通道。
func WithOrderRecorder(r OrderRecorder) WorkerOption {
	return func(w *Worker) { w.orders = r }
}

// WithRiskEventRecorder 设置风控事件落库通道。
func WithRiskEventRecorder(r RiskEventRecorder) WorkerOption {
	return func(w *Worker) { w.events = r }
}

// WithWorkerLogger 设置日志器。
func WithWorkerLogger(l *slog.Logger) WorkerOption {
	return func(w *Worker) {
		if l != nil {
			w.logger = l
		}
	}
}

// WithClock 注入时间源，仅用于测试。
func WithClock(f func() time.Time) WorkerOption {
	return func(w *Worker) {
		if f != nil {
			w.now = f
		}
	}
}

// NewWorker 为一个策略创建执行实例。
func NewWorker(cfg types.StrategyConfig, broker Broker, opts ...WorkerOption) (*Worker, error) {
	if broker == nil {
		return nil, fmt.Errorf("策略 %s 缺少下单通道", cfg.ID)
	}
	// 最后一道闸门：实盘通道只服务 LIVE 状态的策略。
	// 状态机已经把关一次，这里再拦一次——这类错误的代价是真金白银。
	if broker.Mode() == types.ModeLive && !strategy.CanTradeLive(cfg.State) {
		return nil, fmt.Errorf("策略 %s 当前状态为 %s：%w",
			cfg.ID, cfg.State, ErrLiveBrokerRequiresLiveState)
	}
	if broker.Mode() == types.ModePaper &&
		!strategy.CanTradePaper(cfg.State) && !strategy.CanTradeLive(cfg.State) {
		return nil, fmt.Errorf("策略 %s 当前状态为 %s，不允许产生任何交易", cfg.ID, cfg.State)
	}

	w := &Worker{
		cfg:      cfg,
		broker:   broker,
		risk:     NewRiskManager(cfg.Symbol, cfg.Risk),
		logger:   slog.Default(),
		now:      func() time.Time { return time.Now().UTC() },
		position: types.Position{StrategyID: cfg.ID, Symbol: cfg.Symbol},
	}
	for _, o := range opts {
		o(w)
	}
	return w, nil
}

// Symbol 返回该 Worker 负责的标的。
func (w *Worker) Symbol() string { return w.cfg.Symbol }

// StrategyID 返回该 Worker 负责的策略。
func (w *Worker) StrategyID() string { return w.cfg.ID }

// Stats 返回运行统计快照。
func (w *Worker) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}

// Position 返回当前持仓快照。
func (w *Worker) Position() types.Position {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.position
}

// Suspended 报告该标的是否已被风控暂停。
func (w *Worker) Suspended() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats.Suspended
}

// Resume 解除风控暂停。这是人工操作入口，系统自身不调用。
func (w *Worker) Resume() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.risk.Resume()
	w.stats.Suspended = false
	w.stats.SuspendReason = ""
}

// Handle 处理一条决策。
//
// 它是 Worker 的唯一入口，且对错误"就地消化"：任何异常都只影响本标的，
// 记录日志与统计后正常返回。返回 error 只是为了让调用方能观测，
// 上层 Supervisor 不会因此中断其它标的。
func (w *Worker) Handle(ctx context.Context, d types.Decision) (err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 单个标的的 panic 绝不能掀翻整个执行层。
	defer func() {
		if r := recover(); r != nil {
			w.stats.Errors++
			err = fmt.Errorf("标的 %s 的执行实例发生 panic：%v", w.cfg.Symbol, r)
			w.logger.Error("执行实例 panic，已隔离",
				"symbol", w.cfg.Symbol, "strategy_id", w.cfg.ID, "panic", r)
		}
	}()

	if d.StrategyID != "" && d.StrategyID != w.cfg.ID {
		return fmt.Errorf("决策属于策略 %s，不该被路由到 %s 的执行实例",
			d.StrategyID, w.cfg.ID)
	}
	if d.Symbol != "" && d.Symbol != w.cfg.Symbol {
		return fmt.Errorf("决策标的 %s 与本执行实例的 %s 不一致；标的必须严格隔离",
			d.Symbol, w.cfg.Symbol)
	}

	w.stats.DecisionsSeen++
	now := w.now()
	price := d.Price

	// ---- 1. 已有持仓先过风控 ----
	if w.position.IsOpen() && price.IsPositive() {
		if verdict := w.risk.CheckPosition(now, w.position, price); verdict.Verdict == RiskForceClose {
			if err := w.forceClose(ctx, d, verdict, now); err != nil {
				return err
			}
			return nil
		}
	}

	if !d.Triggered {
		return nil
	}

	// ---- 2. 反向信号先平后开 ----
	if w.position.IsOpen() && d.Direction == w.position.Direction.Opposite() {
		if err := w.closePosition(ctx, d, "signal", now); err != nil {
			return err
		}
	}

	// ---- 3. 开仓 ----
	if !w.position.IsOpen() && d.Direction != types.DirectionNeutral {
		return w.openPosition(ctx, d, now)
	}
	return nil
}

func (w *Worker) openPosition(ctx context.Context, d types.Decision, now time.Time) error {
	if !d.Price.IsPositive() {
		w.stats.Errors++
		return fmt.Errorf("决策价格 %s 非正，无法开仓", d.Price)
	}

	side, ok := types.SideFor(d.Direction)
	if !ok {
		return fmt.Errorf("方向 %s 没有对应的开仓动作", d.Direction)
	}

	// 止损的绝对价格要在下单前就算好：算不出来（比如配了 support_resistance 模式但
	// 当前附近没有探测到支撑/阻力位）就不该开仓——用户明确要求了止损保护，没有保护地
	// 开仓等于没忠实执行他的规则。跟 CheckOpen 的拒绝走同一套记录路径。
	//
	// 这一步必须在算仓位之前：risk_pct 仓位模式需要止损距离才能算出仓位大小，
	// fixed_quote 模式虽然不需要，但统一顺序，不为两种模式分别维护一套流程。
	stopLossPrice, err := ResolveStopLossPrice(w.cfg.Risk, d.Direction, d.Price, d.Signals)
	if err != nil {
		w.stats.OrdersRejected++
		w.logger.Info("开仓被止损条件拒绝", "symbol", w.cfg.Symbol, "err", err)
		w.recordRiskEvent(ctx, RiskDecision{
			Verdict: RiskReject, Rule: "unresolved_stop_loss", Reason: err.Error(),
		}, "reject_open", now)
		return nil
	}

	notional, err := ResolvePositionSizeQuote(w.cfg.Risk, d.Price, stopLossPrice)
	if err != nil {
		w.stats.OrdersRejected++
		w.logger.Info("开仓被仓位计算拒绝", "symbol", w.cfg.Symbol, "err", err)
		w.recordRiskEvent(ctx, RiskDecision{
			Verdict: RiskReject, Rule: "unresolved_position_size", Reason: err.Error(),
		}, "reject_open", now)
		return nil
	}

	verdict := w.risk.CheckOpen(now, notional)
	if !verdict.Allowed() {
		w.stats.OrdersRejected++
		w.logger.Info("开仓被风控拒绝",
			"symbol", w.cfg.Symbol, "rule", verdict.Rule, "reason", verdict.Reason)
		w.recordRiskEvent(ctx, verdict, "reject_open", now)
		return nil
	}

	// 止盈算不出来不拒绝开仓，只是这一笔没有止盈线——止盈不是安全机制，跟止损不对称：
	// 突破型入场恰恰是最常见的"附近没有阻力位可当止盈目标"的情形（价格刚突破的那个位
	// 本身变成了支撑，上方往往还没有新的阻力位聚出来），如果止盈也按止损的标准拒绝，
	// 这个模式在突破策略上会几乎不可用。没有止盈目标不影响这笔交易的安全性，
	// 后续还有反向信号/持仓超时等其它退出机制兜底。
	takeProfitPrice, err := ResolveTakeProfitPrice(w.cfg.Risk, d.Direction, d.Price, d.Signals)
	if err != nil {
		w.logger.Info("本次开仓算不出止盈价，先不设止盈", "symbol", w.cfg.Symbol, "err", err)
		takeProfitPrice = decimal.Zero
	}

	order, err := w.broker.PlaceOrder(ctx, OrderRequest{
		StrategyID: w.cfg.ID,
		Symbol:     w.cfg.Symbol,
		Side:       side,
		Quantity:   notional.Div(d.Price),
		RefPrice:   d.Price,
		Provenance: w.provenance(d, "开仓"),
	})
	if err != nil {
		w.stats.Errors++
		w.logger.Error("下单失败", "symbol", w.cfg.Symbol, "err", err)
		return fmt.Errorf("标的 %s 开仓失败：%w", w.cfg.Symbol, err)
	}

	w.stats.OrdersPlaced++
	w.recordOrder(ctx, order)

	w.position = types.Position{
		StrategyID:      w.cfg.ID,
		Symbol:          w.cfg.Symbol,
		Direction:       d.Direction,
		Quantity:        order.Quantity,
		EntryPrice:      order.FilledPrice,
		StopLossPrice:   stopLossPrice,
		TakeProfitPrice: takeProfitPrice,
		OpenedAt:        now,
		EntryOrderID:    order.ID,
	}
	// 开仓手续费即刻计入当日盈亏。
	w.stats.RealizedPnL = w.stats.RealizedPnL.Sub(order.Fee)
	w.risk.RecordRealized(now, order.Fee.Neg())
	return nil
}

func (w *Worker) closePosition(ctx context.Context, d types.Decision, reason string, now time.Time) error {
	pos := w.position
	if !pos.IsOpen() {
		return nil
	}

	price := d.Price
	if !price.IsPositive() {
		price = pos.EntryPrice
	}
	side, _ := types.SideFor(pos.Direction.Opposite())

	prov := w.provenance(d, "平仓："+reason)
	order, err := w.broker.PlaceOrder(ctx, OrderRequest{
		StrategyID: w.cfg.ID,
		Symbol:     w.cfg.Symbol,
		Side:       side,
		Quantity:   pos.Quantity,
		RefPrice:   price,
		Provenance: prov,
	})
	if err != nil {
		w.stats.Errors++
		w.logger.Error("平仓下单失败", "symbol", w.cfg.Symbol, "err", err)
		return fmt.Errorf("标的 %s 平仓失败：%w", w.cfg.Symbol, err)
	}

	w.stats.OrdersPlaced++
	w.recordOrder(ctx, order)

	realized := pos.UnrealizedPnL(order.FilledPrice).Sub(order.Fee)
	w.stats.RealizedPnL = w.stats.RealizedPnL.Add(realized)
	w.risk.RecordRealized(now, realized)

	w.position = types.Position{StrategyID: w.cfg.ID, Symbol: w.cfg.Symbol}
	return nil
}

// forceClose 执行风控强平，并按规则决定是否暂停该标的。
func (w *Worker) forceClose(
	ctx context.Context, d types.Decision, verdict RiskDecision, now time.Time,
) error {
	w.stats.RiskEvents++
	w.logger.Warn("风控触发强制平仓",
		"symbol", w.cfg.Symbol, "rule", verdict.Rule, "reason", verdict.Reason)

	closeErr := w.closePosition(ctx, d, verdict.Rule, now)

	// 单日亏损属于"这个标的今天不该再交易"，必须暂停并等人工确认。
	// 止损/止盈/超时只是单笔交易的正常退出，不暂停策略。
	action := "close"
	if verdict.Rule == "max_daily_loss" {
		w.risk.Halt(verdict.Rule)
		w.stats.Suspended = true
		w.stats.SuspendReason = verdict.Reason
		action = "close_and_suspend"
	}
	w.recordRiskEvent(ctx, verdict, action, now)
	return closeErr
}

// provenance 组装订单溯源信息。
//
// 每笔订单都必须能回答"是哪个模块的哪个信号、什么参数触发的"，
// 这是平台的可解释性底线，也是用户界面上要展示的内容。
func (w *Worker) provenance(d types.Decision, note string) types.OrderProvenance {
	params := make(map[string]map[string]any, len(w.cfg.Modules))
	for _, mc := range w.cfg.Modules {
		// 存参数快照而非引用策略配置：配置随后可能被修改，
		// 而订单必须永远能还原"当时"的参数。
		snapshot := make(map[string]any, len(mc.Params))
		for k, v := range mc.Params {
			snapshot[k] = v
		}
		params[mc.Module] = snapshot
	}
	return types.OrderProvenance{
		DecisionID:   d.ID,
		Combine:      w.cfg.Combine,
		Score:        d.Score,
		Threshold:    w.cfg.Threshold,
		Signals:      d.Signals,
		ModuleParams: params,
		Note:         note,
	}
}

func (w *Worker) recordOrder(ctx context.Context, order types.Order) {
	if w.orders == nil {
		return
	}
	if err := w.orders.RecordOrder(ctx, order); err != nil {
		// 落库失败不回滚订单——单已经下出去了，谎报没下更危险。
		w.stats.Errors++
		w.logger.Error("订单落库失败（订单已成交，请人工核对）",
			"symbol", w.cfg.Symbol, "order_id", order.ID, "err", err)
	}
}

func (w *Worker) recordRiskEvent(
	ctx context.Context, verdict RiskDecision, action string, now time.Time,
) {
	if w.events == nil {
		return
	}
	detail := verdict.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	detail["reason"] = verdict.Reason
	if err := w.events.RecordRiskEvent(ctx, RiskEvent{
		StrategyID: w.cfg.ID,
		Symbol:     w.cfg.Symbol,
		Rule:       verdict.Rule,
		Detail:     detail,
		Action:     action,
		CreatedAt:  now,
	}); err != nil {
		w.logger.Error("风控事件落库失败", "symbol", w.cfg.Symbol, "err", err)
	}
}
