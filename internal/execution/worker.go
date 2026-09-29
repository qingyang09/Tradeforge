package execution

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/i18n"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// Worker is the execution instance for a single strategy (i.e. a single symbol).
//
// One Worker serves exactly one strategy; its internal state (position, risk
// counters) is entirely private. Isolation between symbols is implemented
// precisely by "each symbol gets its own Worker, sharing no state".
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

	// now can be injected, to let tests control time-dependent logic like
	// position holding timeouts.
	now func() time.Time
}

// Stats holds a Worker's run statistics.
type Stats struct {
	DecisionsSeen  int
	OrdersPlaced   int
	OrdersRejected int
	RiskEvents     int
	Errors         int
	RealizedPnL    decimal.Decimal
	Suspended      bool
	SuspendReason  types.Message
}

// WorkerOption configures a Worker.
type WorkerOption func(*Worker)

// WithOrderRecorder sets the order persistence channel.
func WithOrderRecorder(r OrderRecorder) WorkerOption {
	return func(w *Worker) { w.orders = r }
}

// WithRiskEventRecorder sets the risk-event persistence channel.
func WithRiskEventRecorder(r RiskEventRecorder) WorkerOption {
	return func(w *Worker) { w.events = r }
}

// WithWorkerLogger sets the logger.
func WithWorkerLogger(l *slog.Logger) WorkerOption {
	return func(w *Worker) {
		if l != nil {
			w.logger = l
		}
	}
}

// WithClock injects a time source, for tests only.
func WithClock(f func() time.Time) WorkerOption {
	return func(w *Worker) {
		if f != nil {
			w.now = f
		}
	}
}

// NewWorker creates the execution instance for a strategy.
func NewWorker(cfg types.StrategyConfig, broker Broker, opts ...WorkerOption) (*Worker, error) {
	if broker == nil {
		return nil, fmt.Errorf("strategy %s has no order channel", cfg.ID)
	}
	// Last gate: a live channel only serves strategies in LIVE state. The
	// state machine already checks this once; this is a second check here —
	// this class of mistake costs real money.
	if broker.Mode() == types.ModeLive && !strategy.CanTradeLive(cfg.State) {
		return nil, fmt.Errorf("strategy %s is currently in state %s: %w",
			cfg.ID, cfg.State, ErrLiveBrokerRequiresLiveState)
	}
	if broker.Mode() == types.ModePaper &&
		!strategy.CanTradePaper(cfg.State) && !strategy.CanTradeLive(cfg.State) {
		return nil, fmt.Errorf("strategy %s is currently in state %s, which is not allowed to trade at all", cfg.ID, cfg.State)
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

// Symbol returns the symbol this Worker is responsible for.
func (w *Worker) Symbol() string { return w.cfg.Symbol }

// StrategyID returns the strategy this Worker is responsible for.
func (w *Worker) StrategyID() string { return w.cfg.ID }

// Stats returns a snapshot of the run statistics.
func (w *Worker) Stats() Stats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}

// Position returns a snapshot of the current position.
func (w *Worker) Position() types.Position {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.position
}

// Suspended reports whether this symbol has been suspended by risk control.
func (w *Worker) Suspended() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats.Suspended
}

// Resume lifts a risk-control suspension. This is a manual-operation entry
// point; the system itself never calls it.
func (w *Worker) Resume() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.risk.Resume()
	w.stats.Suspended = false
	w.stats.SuspendReason = types.Message{}
}

// Handle processes a single decision.
//
// It is the Worker's only entry point, and "absorbs" errors in place: any
// failure affects only this symbol — it's logged, counted in stats, and
// Handle returns normally. The returned error exists only so the caller can
// observe it; the Supervisor above never lets it interrupt other symbols.
func (w *Worker) Handle(ctx context.Context, d types.Decision) (err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// A panic in a single symbol must never take down the whole execution layer.
	defer func() {
		if r := recover(); r != nil {
			w.stats.Errors++
			err = fmt.Errorf("execution instance for symbol %s panicked: %v", w.cfg.Symbol, r)
			w.logger.Error("execution instance panicked, isolated",
				"symbol", w.cfg.Symbol, "strategy_id", w.cfg.ID, "panic", r)
		}
	}()

	if d.StrategyID != "" && d.StrategyID != w.cfg.ID {
		return fmt.Errorf("decision belongs to strategy %s, should not have been routed to the execution instance for %s",
			d.StrategyID, w.cfg.ID)
	}
	if d.Symbol != "" && d.Symbol != w.cfg.Symbol {
		return fmt.Errorf("决策标的 %s 与本执行实例的 %s 不一致；标的必须严格隔离",
			d.Symbol, w.cfg.Symbol)
	}

	w.stats.DecisionsSeen++
	now := w.now()
	price := d.Price

	// ---- 1. an existing position goes through risk control first ----
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

	// ---- 2. an opposite signal closes first, then opens ----
	if w.position.IsOpen() && d.Direction == w.position.Direction.Opposite() {
		if err := w.closePosition(ctx, d, "signal", now); err != nil {
			return err
		}
	}

	// ---- 3. open a position ----
	if !w.position.IsOpen() && d.Direction != types.DirectionNeutral {
		return w.openPosition(ctx, d, now)
	}
	return nil
}

func (w *Worker) openPosition(ctx context.Context, d types.Decision, now time.Time) error {
	if !d.Price.IsPositive() {
		w.stats.Errors++
		return fmt.Errorf("decision price %s is not positive, cannot open a position", d.Price)
	}

	side, ok := types.SideFor(d.Direction)
	if !ok {
		return fmt.Errorf("direction %s has no corresponding open-position action", d.Direction)
	}

	// The stop-loss's absolute price must be computed before the order is
	// placed: if it can't be computed (e.g. support_resistance mode is
	// configured but no support/resistance level was detected nearby right
	// now), the position must not be opened — the user explicitly asked for
	// stop-loss protection, and opening unprotected would mean not faithfully
	// executing their rule. This follows the same recording path as a
	// CheckOpen rejection.
	//
	// This step must happen before computing the position size: the risk_pct
	// sizing mode needs the stop-loss distance to compute the size; the
	// fixed_quote mode doesn't need it, but the order is kept uniform rather
	// than maintaining two separate flows for the two modes.
	stopLossPrice, err := ResolveStopLossPrice(w.cfg.Risk, d.Direction, d.Price, d.Signals)
	if err != nil {
		w.stats.OrdersRejected++
		w.logger.Info("open rejected by stop-loss condition", "symbol", w.cfg.Symbol, "err", err)
		w.recordRiskEvent(ctx, RiskDecision{
			Verdict: RiskReject, Rule: "unresolved_stop_loss", Reason: types.Message{Literal: err.Error()},
		}, "reject_open", now)
		return nil
	}

	notional, err := ResolvePositionSizeQuote(w.cfg.Risk, d.Price, stopLossPrice)
	if err != nil {
		w.stats.OrdersRejected++
		w.logger.Info("open rejected by position-size calculation", "symbol", w.cfg.Symbol, "err", err)
		w.recordRiskEvent(ctx, RiskDecision{
			Verdict: RiskReject, Rule: "unresolved_position_size", Reason: types.Message{Literal: err.Error()},
		}, "reject_open", now)
		return nil
	}

	verdict := w.risk.CheckOpen(now, notional)
	if !verdict.Allowed() {
		w.stats.OrdersRejected++
		w.logger.Info("open rejected by risk control",
			"symbol", w.cfg.Symbol, "rule", verdict.Rule, "reason", i18n.Render(i18n.LangEN, verdict.Reason))
		w.recordRiskEvent(ctx, verdict, "reject_open", now)
		return nil
	}

	// Failing to compute a take-profit price does not reject the open — this
	// trade simply has no take-profit line. Take-profit is not a safety
	// mechanism, unlike stop-loss, so the two are asymmetric: a breakout
	// entry is precisely the most common case where "there's no resistance
	// level nearby to use as a take-profit target" (the level price just
	// broke through becomes the new support, and there's often no new
	// resistance clustered above it yet). If take-profit were held to the
	// same standard as stop-loss, this pattern would be nearly unusable for
	// breakout strategies. Having no take-profit target doesn't affect the
	// safety of the trade — other exit mechanisms (opposite signal, holding
	// timeout, etc.) still back it up.
	takeProfitPrice, err := ResolveTakeProfitPrice(w.cfg.Risk, d.Direction, d.Price, d.Signals)
	if err != nil {
		w.logger.Info("could not resolve a take-profit price for this open, leaving take-profit unset", "symbol", w.cfg.Symbol, "err", err)
		takeProfitPrice = decimal.Zero
	}

	order, err := w.broker.PlaceOrder(ctx, OrderRequest{
		StrategyID: w.cfg.ID,
		Symbol:     w.cfg.Symbol,
		Side:       side,
		Quantity:   notional.Div(d.Price),
		RefPrice:   d.Price,
		Provenance: w.provenance(d, types.Msg("execution.provenance.open")),
	})
	if err != nil {
		w.stats.Errors++
		w.logger.Error("order placement failed", "symbol", w.cfg.Symbol, "err", err)
		return fmt.Errorf("failed to open a position for symbol %s: %w", w.cfg.Symbol, err)
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
	// The opening fee is charged against the day's P&L immediately.
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

	prov := w.provenance(d, closeReasonMessage(reason))
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
		w.logger.Error("close-position order failed", "symbol", w.cfg.Symbol, "err", err)
		return fmt.Errorf("failed to close the position for symbol %s: %w", w.cfg.Symbol, err)
	}

	w.stats.OrdersPlaced++
	w.recordOrder(ctx, order)

	realized := pos.UnrealizedPnL(order.FilledPrice).Sub(order.Fee)
	w.stats.RealizedPnL = w.stats.RealizedPnL.Add(realized)
	w.risk.RecordRealized(now, realized)

	w.position = types.Position{StrategyID: w.cfg.ID, Symbol: w.cfg.Symbol}
	return nil
}

// forceClose executes a risk-control forced close, and decides whether to
// suspend this symbol per the triggered rule.
func (w *Worker) forceClose(
	ctx context.Context, d types.Decision, verdict RiskDecision, now time.Time,
) error {
	w.stats.RiskEvents++
	w.logger.Warn("risk control triggered a forced close",
		"symbol", w.cfg.Symbol, "rule", verdict.Rule, "reason", i18n.Render(i18n.LangEN, verdict.Reason))

	closeErr := w.closePosition(ctx, d, verdict.Rule, now)

	// A daily loss breach means "this symbol shouldn't trade again today" —
	// it must be suspended and wait for manual confirmation. Stop-loss /
	// take-profit / holding-timeout are just a single trade's normal exit and
	// don't suspend the strategy.
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

// closeReasonMessage maps a close reason -- either the literal "signal" or a
// RiskDecision.Rule value (stop_loss/take_profit/max_daily_loss/max_holding)
// -- to its catalog message. Kept as a fixed set of complete, translator-owned
// sentences rather than one generic "close: {reason}" template with the rule
// name substituted in, since the rule name itself would then need its own
// per-language rendering -- the same reasoning as aggregate.go's blockerLang
// tradeoff, avoided here because the rule set is small and fixed.
func closeReasonMessage(reason string) types.Message {
	switch reason {
	case "signal":
		return types.Msg("execution.provenance.close.signal")
	case "stop_loss":
		return types.Msg("execution.provenance.close.stop_loss")
	case "take_profit":
		return types.Msg("execution.provenance.close.take_profit")
	case "max_daily_loss":
		return types.Msg("execution.provenance.close.max_daily_loss")
	case "max_holding":
		return types.Msg("execution.provenance.close.max_holding")
	default:
		return types.Msg("execution.provenance.close.other", "rule", reason)
	}
}

// provenance assembles the order's provenance information.
//
// Every order must be able to answer "which module's which signal, with what
// parameters, triggered this" — that's the platform's baseline for
// explainability, and also what gets shown on the user interface.
func (w *Worker) provenance(d types.Decision, note types.Message) types.OrderProvenance {
	params := make(map[string]map[string]any, len(w.cfg.Modules))
	for _, mc := range w.cfg.Modules {
		// Store a snapshot of the params rather than a reference to the
		// strategy config: the config may be edited afterward, but an order
		// must always be able to reconstruct the parameters "as they were at the time".
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
		// A persistence failure doesn't roll back the order — the order has
		// already been placed, and falsely claiming it wasn't is more dangerous.
		w.stats.Errors++
		w.logger.Error("failed to persist order (order was filled, please reconcile manually)",
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
		w.logger.Error("failed to persist risk event", "symbol", w.cfg.Symbol, "err", err)
	}
}
