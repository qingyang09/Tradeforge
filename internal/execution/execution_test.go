package execution

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// btcStrategy is the "BTC uses combination A" strategy.
func btcStrategy() types.StrategyConfig {
	return types.StrategyConfig{
		ID: "aaaaaaaa-1111-4111-8111-111111111111", Name: "BTC 三模块",
		Symbol: "BTCUSDT", Timeframe: types.TF1h, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{
			{Module: "support_resistance", Params: map[string]any{"min_touches": 3}},
			{Module: "volume_breakout", Params: map[string]any{"multiplier": 2.0}},
		},
		Risk: types.RiskConfig{
			MaxPositionSizeQuote: dec("1000"),
			MaxDailyLossQuote:    dec("50"),
			StopLossPct:          0.05,
		},
		State: types.StatePaperTrading,
	}
}

// ethStrategy is the "ETH uses combination B" strategy: modules, params, and risk control are all different.
func ethStrategy() types.StrategyConfig {
	return types.StrategyConfig{
		ID: "bbbbbbbb-2222-4222-8222-222222222222", Name: "ETH 单模块",
		Symbol: "ETHUSDT", Timeframe: types.TF15m, Combine: types.CombineWeighted,
		Threshold: 0.6,
		Modules: []types.ModuleConfig{
			{Module: "cvd_orderflow", Weight: 1.0, Params: map[string]any{"window": 30}},
		},
		Risk: types.RiskConfig{
			MaxPositionSizeQuote: dec("500"),
			MaxDailyLossQuote:    dec("10000"), // deliberately set very wide, to verify isolation
		},
		State: types.StatePaperTrading,
	}
}

func decision(cfg types.StrategyConfig, dir types.Direction, price string) types.Decision {
	return types.Decision{
		ID: "dec-" + price, StrategyID: cfg.ID, Symbol: cfg.Symbol,
		Direction: dir, Score: 0.8, Triggered: dir != types.DirectionNeutral,
		Price: dec(price), Timestamp: base,
		Signals: []types.Signal{{
			Module: cfg.Modules[0].Module, Symbol: cfg.Symbol,
			Direction: dir, Confidence: 0.8, Reason: types.Message{Literal: "测试信号"},
		}},
	}
}

// ---------- support/resistance stop-loss and take-profit ----------

func TestOpenPositionResolvesSupportResistanceStopLoss(t *testing.T) {
	cfg := btcStrategy()
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance
	cfg.Risk.StopLossPct = 0
	broker := NewPaperBroker()
	w, err := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}

	d := decision(cfg, types.DirectionLong, "100")
	d.Signals = []types.Signal{supportResistanceSignal("95", "110", false)}
	if err := w.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	pos := w.Position()
	if !pos.IsOpen() {
		t.Fatal("should have opened a position")
	}
	if !pos.StopLossPrice.Equal(dec("95")) {
		t.Errorf("stop-loss price should equal the support level 95 at open time, got %s", pos.StopLossPrice)
	}
}

// When no support level is detected nearby, the position must not be opened
// without stop-loss protection — the user explicitly asked for a stop-loss.
func TestOpenPositionRejectsWhenSupportResistanceLevelUnavailable(t *testing.T) {
	cfg := btcStrategy()
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance
	cfg.Risk.StopLossPct = 0
	broker := NewPaperBroker()
	w, err := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}

	d := decision(cfg, types.DirectionLong, "100")
	d.Signals = []types.Signal{supportResistanceSignal("", "110", false)} // no support level nearby
	if err := w.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	if w.Position().IsOpen() {
		t.Error("should not open a position when the stop-loss price can't be resolved")
	}
	if w.Stats().OrdersRejected == 0 {
		t.Error("should be counted as one rejected open")
	}
}

// Take-profit is not a safety mechanism, unlike stop-loss, so the two are
// asymmetric: failing to resolve a take-profit price (e.g. a breakout entry
// with no new resistance level nearby yet) should not reject the trade —
// this trade simply has no take-profit line. Real backtest data has
// confirmed this is precisely the most common case for breakout strategies
// (the resistance level price just broke through becomes the new support,
// with no resistance level above it yet).
func TestOpenPositionOpensWithoutTakeProfitWhenResistanceUnavailable(t *testing.T) {
	cfg := btcStrategy()
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance
	cfg.Risk.StopLossPct = 0
	cfg.Risk.TakeProfitMode = types.RiskLevelModeSupportResistance
	broker := NewPaperBroker()
	w, err := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}

	d := decision(cfg, types.DirectionLong, "100")
	d.Signals = []types.Signal{supportResistanceSignal("95", "", false)} // support present, resistance absent
	if err := w.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	pos := w.Position()
	if !pos.IsOpen() {
		t.Fatal("failing to resolve a take-profit price should not block this open")
	}
	if !pos.StopLossPrice.Equal(dec("95")) {
		t.Errorf("stop-loss price should be set normally to the support level 95, got %s", pos.StopLossPrice)
	}
	if !pos.TakeProfitPrice.IsZero() {
		t.Errorf("take-profit price should be the zero value (unset), got %s", pos.TakeProfitPrice)
	}
}

// ---------- position sizing by risk percentage (PositionSizingMode = risk_pct) ----------

func riskPctStrategy(equity string, riskPct float64, maxCap string) types.StrategyConfig {
	cfg := btcStrategy()
	cfg.Risk = types.RiskConfig{
		MaxPositionSizeQuote: dec(maxCap),
		PositionSizingMode:   types.PositionSizingModeRiskPct,
		AccountEquityQuote:   dec(equity),
		RiskPerTradePct:      riskPct,
		StopLossPct:          0.05, // entry 100, stop-loss distance 5%
	}
	return cfg
}

// Equity 10000, risk 1% (=100), stop-loss distance 5% -> position = 100 / 0.05
// = 2000, quantity = 2000 / 100 = 20.
func TestOpenPositionRiskPctSizing(t *testing.T) {
	cfg := riskPctStrategy("10000", 0.01, "1000000")
	broker := NewPaperBroker()
	w, err := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}

	d := decision(cfg, types.DirectionLong, "100")
	if err := w.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	pos := w.Position()
	if !pos.IsOpen() {
		t.Fatal("should have opened a position")
	}
	if !pos.Quantity.Equal(dec("20")) {
		t.Errorf("quantity computed from risk percentage = %s, want 20 (position 2000 / entry price 100)", pos.Quantity)
	}
	if !pos.StopLossPrice.Equal(dec("95")) {
		t.Errorf("stop-loss price = %s, want 95", pos.StopLossPrice)
	}
}

// Once the computed position exceeds the hard cap, the whole trade is
// rejected outright, never silently shrunk — silently shrinking would break
// the "this trade only takes on N% equity risk" semantics the user explicitly asked for.
func TestOpenPositionRiskPctSizingRejectedWhenExceedsMaxPositionCap(t *testing.T) {
	// Same equity/risk percentage/stop-loss distance, which should compute a
	// position of 2000, but the hard cap only allows 500.
	cfg := riskPctStrategy("10000", 0.01, "500")
	broker := NewPaperBroker()
	w, err := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}

	d := decision(cfg, types.DirectionLong, "100")
	if err := w.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	if w.Position().IsOpen() {
		t.Fatal("should not open a position when the computed size exceeds the hard cap (and must never be silently shrunk to the cap)")
	}
	if w.Stats().OrdersRejected == 0 {
		t.Error("should be counted as one rejected open")
	}
}

// Risk-percentage position sizing combined with support_resistance
// stop-loss: verifies that the "resolve stop-loss first, then compute
// position size" ordering change in openPosition actually took effect — the
// stop-loss price comes from the detected support level (distance 5, i.e.
// 5%), not some hard-coded percentage.
func TestOpenPositionRiskPctSizingUsesResolvedSupportResistanceStopLoss(t *testing.T) {
	cfg := btcStrategy()
	cfg.Risk = types.RiskConfig{
		MaxPositionSizeQuote: dec("1000000"),
		PositionSizingMode:   types.PositionSizingModeRiskPct,
		AccountEquityQuote:   dec("10000"),
		RiskPerTradePct:      0.01,
		StopLossMode:         types.RiskLevelModeSupportResistance,
	}
	broker := NewPaperBroker()
	w, err := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}

	d := decision(cfg, types.DirectionLong, "100")
	d.Signals = []types.Signal{supportResistanceSignal("95", "110", false)} // distance 5%
	if err := w.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	pos := w.Position()
	if !pos.IsOpen() {
		t.Fatal("should have opened a position")
	}
	if !pos.StopLossPrice.Equal(dec("95")) {
		t.Errorf("stop-loss price should take the detected support level 95, got %s", pos.StopLossPrice)
	}
	// Equity 10000 x 1% = 100, distance 5% -> position 2000, quantity 20 (the
	// same numbers as the fixed-percentage stop-loss scenario, because both
	// sides happen to have a 5% stop-loss distance).
	if !pos.Quantity.Equal(dec("20")) {
		t.Errorf("quantity = %s, want 20", pos.Quantity)
	}
}

// When the stop-loss can't be resolved, risk-percentage sizing mode should
// reject the open just like fixed-amount mode — this rejection happens at
// the "resolve stop-loss" stage, earlier than "compute position size", and
// the ordering change must not alter this pre-existing behavior.
func TestOpenPositionRiskPctSizingSkipsWhenStopUnresolvable(t *testing.T) {
	cfg := btcStrategy()
	cfg.Risk = types.RiskConfig{
		MaxPositionSizeQuote: dec("1000000"),
		PositionSizingMode:   types.PositionSizingModeRiskPct,
		AccountEquityQuote:   dec("10000"),
		RiskPerTradePct:      0.01,
		StopLossMode:         types.RiskLevelModeSupportResistance,
	}
	broker := NewPaperBroker()
	w, err := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}

	d := decision(cfg, types.DirectionLong, "100")
	d.Signals = []types.Signal{supportResistanceSignal("", "110", false)} // no support level nearby
	if err := w.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	if w.Position().IsOpen() {
		t.Error("should not open a position when the stop-loss can't be resolved")
	}
	if w.Stats().OrdersRejected == 0 {
		t.Error("should be counted as one rejected open")
	}
}

// When the stop-loss triggers, the actual forced-close price should trigger
// around the resolved support level, not a fixed percentage.
func TestSupportResistanceStopLossForcesClose(t *testing.T) {
	cfg := btcStrategy()
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance
	cfg.Risk.StopLossPct = 0
	cfg.Risk.MaxDailyLossQuote = dec("10000")
	broker := NewPaperBroker()
	w, err := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	open := decision(cfg, types.DirectionLong, "100")
	open.Signals = []types.Signal{supportResistanceSignal("95", "110", false)}
	if err := w.Handle(ctx, open); err != nil {
		t.Fatal(err)
	}
	if !w.Position().IsOpen() {
		t.Fatal("should have opened a position")
	}

	// Price drops below the support level: a 4% drop (shallower than
	// btcStrategy's original default 5% pct stop-loss) — only the
	// support-level mode (stop-loss price 95) would trigger here, verifying
	// this really goes through the new logic and not a leftover pct check.
	if err := w.Handle(ctx, decision(cfg, types.DirectionNeutral, "94")); err != nil {
		t.Fatal(err)
	}
	if w.Position().IsOpen() {
		t.Error("should have closed the position after breaking below the support level")
	}
}

// ---------- order provenance ----------

// Every order must be able to answer "which module's which signal, with what parameters, triggered this".
func TestOrderCarriesFullProvenance(t *testing.T) {
	cfg := btcStrategy()
	broker := NewPaperBroker()
	w, err := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	if err != nil {
		t.Fatal(err)
	}

	if err := w.Handle(context.Background(), decision(cfg, types.DirectionLong, "100")); err != nil {
		t.Fatal(err)
	}

	orders := broker.Orders()
	if len(orders) != 1 {
		t.Fatalf("order count = %d, want 1", len(orders))
	}
	p := orders[0].Provenance
	if p.DecisionID == "" {
		t.Error("missing decision ID, can't trace back what triggered this")
	}
	if len(p.Signals) == 0 {
		t.Error("missing module signal detail")
	}
	if p.Combine != types.CombineAll {
		t.Errorf("combine mode = %s, want ALL", p.Combine)
	}
	// Params must be a snapshot, not just the module name.
	params, ok := p.ModuleParams["volume_breakout"]
	if !ok {
		t.Fatalf("missing param snapshot for volume_breakout, got: %v", p.ModuleParams)
	}
	if params["multiplier"] != 2.0 {
		t.Errorf("param snapshot = %v, want it to include multiplier=2.0", params)
	}
}

// The param snapshot must be decoupled from the strategy config: editing the
// config afterward must not rewrite a historical order's provenance.
func TestProvenanceSnapshotIsIndependentOfLaterConfigEdits(t *testing.T) {
	cfg := btcStrategy()
	broker := NewPaperBroker()
	w, _ := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	_ = w.Handle(context.Background(), decision(cfg, types.DirectionLong, "100"))

	// Edit the params map in the strategy config after the fact.
	cfg.Modules[1].Params["multiplier"] = 99.0

	got := broker.Orders()[0].Provenance.ModuleParams["volume_breakout"]["multiplier"]
	if got != 2.0 {
		t.Errorf("the order provenance's params were polluted by a later config edit: %v", got)
	}
}

// ---------- state-machine gate ----------

func TestLiveBrokerRejectsNonLiveStrategy(t *testing.T) {
	cfg := btcStrategy()
	cfg.State = types.StatePaperTrading

	_, err := NewWorker(cfg, &fakeLiveBroker{}, WithWorkerLogger(quietLogger()))
	if !errors.Is(err, ErrLiveBrokerRequiresLiveState) {
		t.Fatalf("a non-LIVE strategy must not accept a live channel, want ErrLiveBrokerRequiresLiveState, got: %v", err)
	}
}

func TestDraftStrategyCannotTradeAtAll(t *testing.T) {
	cfg := btcStrategy()
	cfg.State = types.StateDraft

	if _, err := NewWorker(cfg, NewPaperBroker(), WithWorkerLogger(quietLogger())); err == nil {
		t.Fatal("a strategy in DRAFT state should not be able to trade at all")
	}
}

type fakeLiveBroker struct{}

func (fakeLiveBroker) Name() string            { return "fake-live" }
func (fakeLiveBroker) Mode() types.TradingMode { return types.ModeLive }
func (fakeLiveBroker) PlaceOrder(context.Context, OrderRequest) (types.Order, error) {
	return types.Order{}, nil
}

// ---------- risk control ----------

func TestStopLossForcesClose(t *testing.T) {
	cfg := btcStrategy()
	// Widen the daily-loss cap so stop-loss is the only rule that can trigger
	// — otherwise a 1000 position dropping 6% loses 60, which would hit the
	// 50 daily-loss cap first (that's a different test case).
	cfg.Risk.MaxDailyLossQuote = dec("10000")
	broker := NewPaperBroker()
	w, _ := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	ctx := context.Background()

	if err := w.Handle(ctx, decision(cfg, types.DirectionLong, "100")); err != nil {
		t.Fatal(err)
	}
	if !w.Position().IsOpen() {
		t.Fatal("should have opened a position")
	}

	// Drops 6%, past the 5% stop-loss line.
	if err := w.Handle(ctx, decision(cfg, types.DirectionNeutral, "94")); err != nil {
		t.Fatal(err)
	}
	if w.Position().IsOpen() {
		t.Error("should have closed the position after the stop-loss triggered")
	}
	if w.Stats().RiskEvents == 0 {
		t.Error("a risk-control trigger should be counted in stats")
	}
	// Stop-loss is just a single trade's normal exit; it must not suspend the whole symbol.
	if w.Suspended() {
		t.Error("a stop-loss should not cause this symbol to be suspended")
	}
}

func TestDailyLossLimitSuspendsSymbol(t *testing.T) {
	cfg := btcStrategy()
	cfg.Risk.StopLossPct = 0 // disable the stop-loss so the unrealized loss hits the daily-loss cap directly
	broker := NewPaperBroker()
	w, _ := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	ctx := context.Background()

	_ = w.Handle(ctx, decision(cfg, types.DirectionLong, "100"))
	// A 1000 USDT position dropping 10% is roughly a 100 loss, past the 50 daily-loss cap.
	_ = w.Handle(ctx, decision(cfg, types.DirectionNeutral, "90"))

	if w.Position().IsOpen() {
		t.Error("should have force-closed once the daily-loss cap triggered")
	}
	if !w.Suspended() {
		t.Fatal("this symbol should be suspended once the daily-loss cap triggered")
	}
	if w.Stats().SuspendReason.Key != "execution.risk.max_daily_loss_force_close" {
		t.Errorf("suspend reason should state which rule triggered: %+v", w.Stats().SuspendReason)
	}

	// No new positions after being suspended.
	before := w.Stats().OrdersPlaced
	_ = w.Handle(ctx, decision(cfg, types.DirectionLong, "100"))
	if w.Stats().OrdersPlaced != before {
		t.Error("should not keep opening new positions after being suspended")
	}
	if w.Stats().OrdersRejected == 0 {
		t.Error("a rejected open should be counted in stats")
	}
}

func TestMaxHoldingPeriodForcesClose(t *testing.T) {
	cfg := btcStrategy()
	cfg.Risk.MaxHoldingPeriod = types.D(2 * time.Hour)
	cfg.Risk.StopLossPct = 0

	now := base
	broker := NewPaperBroker()
	w, _ := NewWorker(cfg, broker,
		WithWorkerLogger(quietLogger()),
		WithClock(func() time.Time { return now }),
	)
	ctx := context.Background()

	_ = w.Handle(ctx, decision(cfg, types.DirectionLong, "100"))
	if !w.Position().IsOpen() {
		t.Fatal("should have opened a position")
	}

	now = base.Add(3 * time.Hour)
	_ = w.Handle(ctx, decision(cfg, types.DirectionNeutral, "100"))

	if w.Position().IsOpen() {
		t.Error("should have force-closed after exceeding the max holding period")
	}
}

func TestPositionSizeCapRejectsOversizedOrder(t *testing.T) {
	r := NewRiskManager("BTCUSDT", types.RiskConfig{MaxPositionSizeQuote: dec("100")})
	v := r.CheckOpen(base, dec("500"))
	if v.Allowed() {
		t.Fatal("an open exceeding the per-trade cap should be rejected")
	}
	if v.Rule != "max_position_size" {
		t.Errorf("rule name = %q", v.Rule)
	}
}

// A new day resets the loss stats but does not auto-lift the suspension —
// a symbol that tripped risk control must be confirmed by a human.
func TestNewDayResetsLossButNotSuspension(t *testing.T) {
	r := NewRiskManager("BTCUSDT", types.RiskConfig{
		MaxPositionSizeQuote: dec("1000"), MaxDailyLossQuote: dec("50"),
	})
	r.RecordRealized(base, dec("-80"))
	r.Halt("max_daily_loss")

	next := base.Add(24 * time.Hour)
	v := r.CheckOpen(next, dec("100"))
	if !r.DayRealizedLoss().IsZero() {
		t.Errorf("the day's loss should reset after rolling to a new day, got %s", r.DayRealizedLoss())
	}
	if v.Allowed() {
		t.Error("rolling to a new day should not auto-lift the risk-control suspension")
	}

	r.Resume()
	if !r.CheckOpen(next, dec("100")).Allowed() {
		t.Error("trading should resume after a manual suspension lift")
	}
}

// ---------- opposite signal ----------

func TestOppositeSignalClosesThenReverses(t *testing.T) {
	cfg := btcStrategy()
	cfg.Risk.StopLossPct = 0
	broker := NewPaperBroker()
	w, _ := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	ctx := context.Background()

	_ = w.Handle(ctx, decision(cfg, types.DirectionLong, "100"))
	_ = w.Handle(ctx, decision(cfg, types.DirectionShort, "101"))

	pos := w.Position()
	if !pos.IsOpen() || pos.Direction != types.DirectionShort {
		t.Fatalf("should be holding a short after the opposite signal, got: %+v", pos)
	}
	if len(broker.Orders()) != 3 {
		t.Errorf("order count = %d, want 3 (open long, close long, open short)", len(broker.Orders()))
	}
}

// ---------- isolation ----------

// A decision routed to the wrong symbol must error — an ETH signal must never
// be allowed to touch a BTC position.
func TestWorkerRejectsForeignSymbol(t *testing.T) {
	cfg := btcStrategy()
	w, _ := NewWorker(cfg, NewPaperBroker(), WithWorkerLogger(quietLogger()))

	d := decision(ethStrategy(), types.DirectionLong, "2000")
	d.StrategyID = cfg.ID // strategy ID matches, but the symbol doesn't
	err := w.Handle(context.Background(), d)
	if err == nil {
		t.Fatal("a decision with a mismatched symbol must be rejected")
	}
	if !strings.Contains(err.Error(), "隔离") {
		t.Errorf("error message should call out the symbol-isolation requirement: %v", err)
	}
}

// A broker channel that keeps erroring for one symbol must not affect another symbol.
func TestBrokerFailureIsIsolatedToOneSymbol(t *testing.T) {
	ctx := context.Background()
	sup := NewSupervisor(quietLogger())
	defer sup.Shutdown()

	btc, eth := btcStrategy(), ethStrategy()
	ethBroker := NewPaperBroker()

	if err := sup.Register(ctx, btc, &failingBroker{}); err != nil {
		t.Fatal(err)
	}
	if err := sup.Register(ctx, eth, ethBroker); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		_ = sup.Dispatch(decision(btc, types.DirectionLong, "100"))
		_ = sup.Dispatch(decision(eth, types.DirectionLong, "2000"))
	}
	sup.Drain()

	stats := sup.StatsByStrategy()
	if stats[btc.ID].Stats.Errors == 0 {
		t.Error("BTC's order failures should be recorded")
	}
	if stats[eth.ID].Stats.Errors != 0 {
		t.Errorf("ETH should not be affected by BTC's failure, got error count %d", stats[eth.ID].Stats.Errors)
	}
	if len(ethBroker.Orders()) == 0 {
		t.Error("ETH should have filled normally")
	}
}

// A panic in one symbol; every other symbol must keep working.
func TestPanicInOneSymbolDoesNotAffectOthers(t *testing.T) {
	ctx := context.Background()
	sup := NewSupervisor(quietLogger())
	defer sup.Shutdown()

	btc, eth := btcStrategy(), ethStrategy()
	ethBroker := NewPaperBroker()

	if err := sup.Register(ctx, btc, &panickingBroker{}); err != nil {
		t.Fatal(err)
	}
	if err := sup.Register(ctx, eth, ethBroker); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		_ = sup.Dispatch(decision(btc, types.DirectionLong, "100"))
		_ = sup.Dispatch(decision(eth, types.DirectionLong, "2000"))
	}
	sup.Drain()

	if sup.StatsByStrategy()[btc.ID].Stats.Errors == 0 {
		t.Error("the panic should have been caught and counted in BTC's error stats")
	}
	if len(ethBroker.Orders()) == 0 {
		t.Fatal("ETH should be completely unaffected by BTC's panic")
	}
}

type failingBroker struct{}

func (failingBroker) Name() string            { return "failing" }
func (failingBroker) Mode() types.TradingMode { return types.ModePaper }
func (failingBroker) PlaceOrder(context.Context, OrderRequest) (types.Order, error) {
	return types.Order{}, errors.New("exchange connection timed out")
}

type panickingBroker struct{}

func (panickingBroker) Name() string            { return "panicking" }
func (panickingBroker) Mode() types.TradingMode { return types.ModePaper }
func (panickingBroker) PlaceOrder(context.Context, OrderRequest) (types.Order, error) {
	panic("internal order-channel crash")
}

// ---------- integration: two symbols, different combinations, independent risk control ----------

// This is the integration test required by stage 6: BTC runs combination A
// and ETH runs combination B simultaneously, verifying the two are isolated
// from each other and their risk controls each take effect independently.
func TestIntegrationTwoSymbolsRunIndependently(t *testing.T) {
	ctx := context.Background()
	sup := NewSupervisor(quietLogger())
	defer sup.Shutdown()

	btc, eth := btcStrategy(), ethStrategy()
	btcBroker, ethBroker := NewPaperBroker(), NewPaperBroker()
	orders := &recordingOrders{}

	// Turn off stop-loss for both symbols so the daily-loss cap is the only
	// force-close rule, making isolation easier to observe.
	btc.Risk.StopLossPct = 0

	if err := sup.Register(ctx, btc, btcBroker, WithOrderRecorder(orders)); err != nil {
		t.Fatal(err)
	}
	if err := sup.Register(ctx, eth, ethBroker, WithOrderRecorder(orders)); err != nil {
		t.Fatal(err)
	}

	// Both symbols open a position at the same time.
	_ = sup.Dispatch(decision(btc, types.DirectionLong, "100"))
	_ = sup.Dispatch(decision(eth, types.DirectionLong, "2000"))
	sup.Drain()

	btcW, _ := sup.Worker(btc.ID)
	ethW, _ := sup.Worker(eth.ID)
	if !btcW.Position().IsOpen() || !ethW.Position().IsOpen() {
		t.Fatal("both symbols should have opened a position")
	}

	// BTC crashes into its own daily-loss cap (50 USDT); ETH drops the same
	// amount but its cap is 10000, so it's unaffected.
	_ = sup.Dispatch(decision(btc, types.DirectionNeutral, "90"))
	_ = sup.Dispatch(decision(eth, types.DirectionNeutral, "1800"))
	sup.Drain()

	if !btcW.Suspended() {
		t.Error("BTC should be suspended for tripping its own daily-loss cap")
	}
	if btcW.Position().IsOpen() {
		t.Error("BTC should have been force-closed")
	}

	if ethW.Suspended() {
		t.Error("ETH's risk allowance is far more generous and should not be caught up in BTC's risk control")
	}
	if !ethW.Position().IsOpen() {
		t.Error("ETH's position should not be affected by BTC's forced close")
	}

	// After BTC is suspended, ETH can still open new positions normally.
	_ = sup.Dispatch(decision(btc, types.DirectionLong, "100"))
	_ = sup.Dispatch(decision(eth, types.DirectionShort, "1800"))
	sup.Drain()

	if btcW.Position().IsOpen() {
		t.Error("suspended BTC should not open a new position")
	}
	if ethW.Position().Direction != types.DirectionShort {
		t.Errorf("ETH should have flipped to short, got direction %s", ethW.Position().Direction)
	}

	// Every order must carry the correct symbol and provenance.
	for _, o := range orders.all() {
		if o.Symbol != "BTCUSDT" && o.Symbol != "ETHUSDT" {
			t.Errorf("found an order for an unknown symbol: %s", o.Symbol)
		}
		if o.Provenance.DecisionID == "" {
			t.Errorf("order %s is missing provenance", o.ID)
		}
		if o.Mode != types.ModePaper {
			t.Errorf("order %s mode = %s, want PAPER", o.ID, o.Mode)
		}
	}
	t.Logf("BTC stats: %+v", btcW.Stats())
	t.Logf("ETH stats: %+v", ethW.Stats())
}

type recordingOrders struct {
	mu     sync.Mutex
	orders []types.Order
}

func (r *recordingOrders) RecordOrder(_ context.Context, o types.Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orders = append(r.orders, o)
	return nil
}

func (r *recordingOrders) all() []types.Order {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]types.Order, len(r.orders))
	copy(out, r.orders)
	return out
}

// ---------- Supervisor ----------

func TestDispatchToUnknownStrategy(t *testing.T) {
	sup := NewSupervisor(quietLogger())
	defer sup.Shutdown()

	err := sup.Dispatch(decision(btcStrategy(), types.DirectionLong, "100"))
	if !errors.Is(err, ErrUnknownStrategy) {
		t.Fatalf("want ErrUnknownStrategy, got: %v", err)
	}
}

func TestDuplicateRegistrationRejected(t *testing.T) {
	ctx := context.Background()
	sup := NewSupervisor(quietLogger())
	defer sup.Shutdown()

	cfg := btcStrategy()
	if err := sup.Register(ctx, cfg, NewPaperBroker()); err != nil {
		t.Fatal(err)
	}
	if err := sup.Register(ctx, cfg, NewPaperBroker()); err == nil {
		t.Fatal("the same strategy should not be registered twice")
	}
}

func TestUnregisterStopsWorker(t *testing.T) {
	ctx := context.Background()
	sup := NewSupervisor(quietLogger())
	defer sup.Shutdown()

	cfg := btcStrategy()
	if err := sup.Register(ctx, cfg, NewPaperBroker()); err != nil {
		t.Fatal(err)
	}
	if err := sup.Unregister(cfg.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := sup.Worker(cfg.ID); ok {
		t.Error("should not be able to fetch the execution instance after unregistering")
	}
	if err := sup.Dispatch(decision(cfg, types.DirectionLong, "100")); !errors.Is(err, ErrUnknownStrategy) {
		t.Errorf("dispatching after unregistering should report unknown strategy, got: %v", err)
	}
}

// ---------- paper trading channel ----------

func TestPaperBrokerNeverClaimsToBeLive(t *testing.T) {
	if NewPaperBroker().Mode() != types.ModePaper {
		t.Fatal("the paper channel must always report as PAPER, or the live gate would be defeated")
	}
}

func TestPaperBrokerSlippageAlwaysHurts(t *testing.T) {
	b := NewPaperBroker()
	b.SlippageBps = decimal.NewFromInt(100) // 1%
	ctx := context.Background()

	buy, err := b.PlaceOrder(ctx, OrderRequest{
		Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: dec("1"), RefPrice: dec("100"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sell, err := b.PlaceOrder(ctx, OrderRequest{
		Symbol: "BTCUSDT", Side: types.SideSell,
		Quantity: dec("1"), RefPrice: dec("100"),
	})
	if err != nil {
		t.Fatal(err)
	}

	if !buy.FilledPrice.Equal(dec("101")) {
		t.Errorf("buy fill price = %s, want 101 (slippage pushed up)", buy.FilledPrice)
	}
	if !sell.FilledPrice.Equal(dec("99")) {
		t.Errorf("sell fill price = %s, want 99 (slippage pushed down)", sell.FilledPrice)
	}
}

func TestBinanceBrokerRefusesNonTestnetURL(t *testing.T) {
	_, err := NewBinanceTestnetBroker(BinanceConfig{
		BaseURL: "https://api.binance.com", APIKey: "k", APISecret: "s",
	})
	if err == nil {
		t.Fatal("must reject an address pointing at production")
	}
}

func TestBinanceBrokerIsTreatedAsPaper(t *testing.T) {
	b, err := NewBinanceTestnetBroker(BinanceConfig{APIKey: "k", APISecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Mode() != types.ModePaper {
		t.Error("the testnet trades with simulated funds and must be treated as paper")
	}
}
