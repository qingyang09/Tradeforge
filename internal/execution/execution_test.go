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

// btcStrategy 是"BTC 用 A 组合"的策略。
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

// ethStrategy 是"ETH 用 B 组合"的策略：模块、参数、风控全都不同。
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
			MaxDailyLossQuote:    dec("10000"), // 刻意设得很宽，用来验证隔离
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
			Direction: dir, Confidence: 0.8, Reason: "测试信号",
		}},
	}
}

// ---------- 支撑/阻力位止损止盈 ----------

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
		t.Fatal("应当已开仓")
	}
	if !pos.StopLossPrice.Equal(dec("95")) {
		t.Errorf("止损价应等于开仓时的支撑位 95，实际 %s", pos.StopLossPrice)
	}
}

// 附近没有探测到支撑位时，不应该在没有止损保护的情况下开仓——用户明确要求了止损。
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
	d.Signals = []types.Signal{supportResistanceSignal("", "110", false)} // 附近没有支撑位
	if err := w.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	if w.Position().IsOpen() {
		t.Error("算不出止损价时不应该开仓")
	}
	if w.Stats().OrdersRejected == 0 {
		t.Error("应当记为一次被拒绝的开仓")
	}
}

// 止盈不是安全机制，跟止损不对称：算不出止盈价（比如突破型入场附近还没有新的阻力位）
// 不该拒绝这笔交易，只是这一笔没有止盈线——真实回测数据验证过这正是突破策略最常见的
// 情形（价格刚突破的阻力位本身变成了新的支撑，上方暂时没有阻力位）。
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
	d.Signals = []types.Signal{supportResistanceSignal("95", "", false)} // 有支撑、没有阻力
	if err := w.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	pos := w.Position()
	if !pos.IsOpen() {
		t.Fatal("止盈算不出来不应该拦下这笔开仓")
	}
	if !pos.StopLossPrice.Equal(dec("95")) {
		t.Errorf("止损价应该正常设置为支撑位 95，实际 %s", pos.StopLossPrice)
	}
	if !pos.TakeProfitPrice.IsZero() {
		t.Errorf("止盈价应为零值（未设置），实际 %s", pos.TakeProfitPrice)
	}
}

// ---------- 按风险百分比计算仓位（PositionSizingMode = risk_pct） ----------

func riskPctStrategy(equity string, riskPct float64, maxCap string) types.StrategyConfig {
	cfg := btcStrategy()
	cfg.Risk = types.RiskConfig{
		MaxPositionSizeQuote: dec(maxCap),
		PositionSizingMode:   types.PositionSizingModeRiskPct,
		AccountEquityQuote:   dec(equity),
		RiskPerTradePct:      riskPct,
		StopLossPct:          0.05, // 入场 100、止损距离 5%
	}
	return cfg
}

// 权益 10000、风险 1%（=100）、止损距离 5% → 仓位 = 100 / 0.05 = 2000，
// 数量 = 2000 / 100 = 20。
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
		t.Fatal("应当已开仓")
	}
	if !pos.Quantity.Equal(dec("20")) {
		t.Errorf("按风险百分比算出的数量 = %s，期望 20（仓位 2000 / 开仓价 100）", pos.Quantity)
	}
	if !pos.StopLossPrice.Equal(dec("95")) {
		t.Errorf("止损价 = %s，期望 95", pos.StopLossPrice)
	}
}

// 算出来的仓位一旦超过硬上限就整笔拒绝，不做静默缩小——静默缩小会破坏"这笔交易只
// 承担 N% 权益风险"这个用户明确要的语义。
func TestOpenPositionRiskPctSizingRejectedWhenExceedsMaxPositionCap(t *testing.T) {
	// 同样的权益/风险比例/止损距离，本该算出仓位 2000，但硬上限只给 500。
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
		t.Fatal("算出的仓位超过硬上限时不应该开仓（更不应该被静默缩小到上限）")
	}
	if w.Stats().OrdersRejected == 0 {
		t.Error("应当记为一次被拒绝的开仓")
	}
}

// 风险百分比仓位模式配合 support_resistance 止损：验证 openPosition 里"先解析止损、
// 再算仓位"的顺序调整确实生效——止损价来自探测到的支撑位（距离 5，即 5%），
// 而不是某个写死的百分比。
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
	d.Signals = []types.Signal{supportResistanceSignal("95", "110", false)} // 距离 5%
	if err := w.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	pos := w.Position()
	if !pos.IsOpen() {
		t.Fatal("应当已开仓")
	}
	if !pos.StopLossPrice.Equal(dec("95")) {
		t.Errorf("止损价应取探测到的支撑位 95，实际 %s", pos.StopLossPrice)
	}
	// 权益 10000 × 1% = 100，距离 5% → 仓位 2000，数量 20（跟固定百分比止损场景
	// 算出同样的数字，因为两边的止损距离恰好都是 5%）。
	if !pos.Quantity.Equal(dec("20")) {
		t.Errorf("数量 = %s，期望 20", pos.Quantity)
	}
}

// 止损算不出来时，风险百分比仓位模式应该跟固定金额模式一样拒绝开仓——这一步的拒绝
// 发生在"解析止损"阶段，比"算仓位"更早，顺序调整不应该改变这个既有行为。
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
	d.Signals = []types.Signal{supportResistanceSignal("", "110", false)} // 附近没有支撑位
	if err := w.Handle(context.Background(), d); err != nil {
		t.Fatal(err)
	}

	if w.Position().IsOpen() {
		t.Error("止损算不出来时不应该开仓")
	}
	if w.Stats().OrdersRejected == 0 {
		t.Error("应当记为一次被拒绝的开仓")
	}
}

// 触发止损时，实际强平价应该在解析出的支撑位附近触发，而不是固定百分比。
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
		t.Fatal("应当已开仓")
	}

	// 价格跌到支撑位以下：4% 跌幅（比 btcStrategy 原本默认的 5% pct 止损更浅），
	// 只有支撑位模式（止损价 95）会在这里触发，验证走的确实是新逻辑而不是残留的 pct。
	if err := w.Handle(ctx, decision(cfg, types.DirectionNeutral, "94")); err != nil {
		t.Fatal(err)
	}
	if w.Position().IsOpen() {
		t.Error("跌破支撑位后应当已平仓")
	}
}

// ---------- 订单溯源 ----------

// 每笔订单都要能回答"是哪个模块的哪个信号、什么参数触发的"。
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
		t.Fatalf("订单数 = %d，期望 1", len(orders))
	}
	p := orders[0].Provenance
	if p.DecisionID == "" {
		t.Error("缺少决策 ID，无法反查触发依据")
	}
	if len(p.Signals) == 0 {
		t.Error("缺少模块信号明细")
	}
	if p.Combine != types.CombineAll {
		t.Errorf("组合方式 = %s，期望 ALL", p.Combine)
	}
	// 参数必须是快照，不能只存模块名。
	params, ok := p.ModuleParams["volume_breakout"]
	if !ok {
		t.Fatalf("缺少 volume_breakout 的参数快照，实际：%v", p.ModuleParams)
	}
	if params["multiplier"] != 2.0 {
		t.Errorf("参数快照 = %v，期望包含 multiplier=2.0", params)
	}
}

// 参数快照必须与策略配置解耦：事后改配置不该改写历史订单的溯源。
func TestProvenanceSnapshotIsIndependentOfLaterConfigEdits(t *testing.T) {
	cfg := btcStrategy()
	broker := NewPaperBroker()
	w, _ := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	_ = w.Handle(context.Background(), decision(cfg, types.DirectionLong, "100"))

	// 事后修改策略配置里的参数 map。
	cfg.Modules[1].Params["multiplier"] = 99.0

	got := broker.Orders()[0].Provenance.ModuleParams["volume_breakout"]["multiplier"]
	if got != 2.0 {
		t.Errorf("订单溯源里的参数被后续配置改动污染了：%v", got)
	}
}

// ---------- 状态机闸门 ----------

func TestLiveBrokerRejectsNonLiveStrategy(t *testing.T) {
	cfg := btcStrategy()
	cfg.State = types.StatePaperTrading

	_, err := NewWorker(cfg, &fakeLiveBroker{}, WithWorkerLogger(quietLogger()))
	if !errors.Is(err, ErrLiveBrokerRequiresLiveState) {
		t.Fatalf("非 LIVE 状态不得接实盘通道，期望 ErrLiveBrokerRequiresLiveState，得到：%v", err)
	}
}

func TestDraftStrategyCannotTradeAtAll(t *testing.T) {
	cfg := btcStrategy()
	cfg.State = types.StateDraft

	if _, err := NewWorker(cfg, NewPaperBroker(), WithWorkerLogger(quietLogger())); err == nil {
		t.Fatal("DRAFT 状态的策略不应产生任何交易")
	}
}

type fakeLiveBroker struct{}

func (fakeLiveBroker) Name() string            { return "fake-live" }
func (fakeLiveBroker) Mode() types.TradingMode { return types.ModeLive }
func (fakeLiveBroker) PlaceOrder(context.Context, OrderRequest) (types.Order, error) {
	return types.Order{}, nil
}

// ---------- 风控 ----------

func TestStopLossForcesClose(t *testing.T) {
	cfg := btcStrategy()
	// 把日亏上限放宽，让止损成为唯一会触发的规则——
	// 否则 1000 仓位跌 6% 亏掉 60，会先撞上 50 的日亏上限（那是另一条用例）。
	cfg.Risk.MaxDailyLossQuote = dec("10000")
	broker := NewPaperBroker()
	w, _ := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	ctx := context.Background()

	if err := w.Handle(ctx, decision(cfg, types.DirectionLong, "100")); err != nil {
		t.Fatal(err)
	}
	if !w.Position().IsOpen() {
		t.Fatal("应当已开仓")
	}

	// 跌 6%，超过 5% 止损线。
	if err := w.Handle(ctx, decision(cfg, types.DirectionNeutral, "94")); err != nil {
		t.Fatal(err)
	}
	if w.Position().IsOpen() {
		t.Error("触发止损后应当已平仓")
	}
	if w.Stats().RiskEvents == 0 {
		t.Error("风控触发应当被计入统计")
	}
	// 止损只是单笔交易的正常退出，不该暂停整个标的。
	if w.Suspended() {
		t.Error("止损不应导致该标的被暂停")
	}
}

func TestDailyLossLimitSuspendsSymbol(t *testing.T) {
	cfg := btcStrategy()
	cfg.Risk.StopLossPct = 0 // 关掉止损，让浮亏直接撞上单日亏损上限
	broker := NewPaperBroker()
	w, _ := NewWorker(cfg, broker, WithWorkerLogger(quietLogger()))
	ctx := context.Background()

	_ = w.Handle(ctx, decision(cfg, types.DirectionLong, "100"))
	// 1000 USDT 仓位跌 10% ≈ 亏 100，超过 50 的日亏上限。
	_ = w.Handle(ctx, decision(cfg, types.DirectionNeutral, "90"))

	if w.Position().IsOpen() {
		t.Error("触发单日亏损上限后应当强制平仓")
	}
	if !w.Suspended() {
		t.Fatal("触发单日亏损上限后该标的应当被暂停")
	}
	if !strings.Contains(w.Stats().SuspendReason, "上限") {
		t.Errorf("暂停原因应当说明是哪条规则：%q", w.Stats().SuspendReason)
	}

	// 暂停后不再开新仓。
	before := w.Stats().OrdersPlaced
	_ = w.Handle(ctx, decision(cfg, types.DirectionLong, "100"))
	if w.Stats().OrdersPlaced != before {
		t.Error("暂停后不应继续开新仓")
	}
	if w.Stats().OrdersRejected == 0 {
		t.Error("被拒绝的开仓应当计入统计")
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
		t.Fatal("应当已开仓")
	}

	now = base.Add(3 * time.Hour)
	_ = w.Handle(ctx, decision(cfg, types.DirectionNeutral, "100"))

	if w.Position().IsOpen() {
		t.Error("超过最大持仓时间后应当强制平仓")
	}
}

func TestPositionSizeCapRejectsOversizedOrder(t *testing.T) {
	r := NewRiskManager("BTCUSDT", types.RiskConfig{MaxPositionSizeQuote: dec("100")})
	v := r.CheckOpen(base, dec("500"))
	if v.Allowed() {
		t.Fatal("超过单笔上限的开仓应当被拒绝")
	}
	if v.Rule != "max_position_size" {
		t.Errorf("规则名 = %q", v.Rule)
	}
}

// 跨日重置亏损统计，但不自动解除暂停——触发过风控的标的必须人工确认。
func TestNewDayResetsLossButNotSuspension(t *testing.T) {
	r := NewRiskManager("BTCUSDT", types.RiskConfig{
		MaxPositionSizeQuote: dec("1000"), MaxDailyLossQuote: dec("50"),
	})
	r.RecordRealized(base, dec("-80"))
	r.Halt("max_daily_loss")

	next := base.Add(24 * time.Hour)
	v := r.CheckOpen(next, dec("100"))
	if !r.DayRealizedLoss().IsZero() {
		t.Errorf("跨日后当日亏损应重置，实际 %s", r.DayRealizedLoss())
	}
	if v.Allowed() {
		t.Error("跨日不应自动解除风控暂停")
	}

	r.Resume()
	if !r.CheckOpen(next, dec("100")).Allowed() {
		t.Error("人工解除暂停后应当恢复交易")
	}
}

// ---------- 反向信号 ----------

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
		t.Fatalf("反向信号后应当持有空头，实际：%+v", pos)
	}
	if len(broker.Orders()) != 3 {
		t.Errorf("订单数 = %d，期望 3（开多、平多、开空）", len(broker.Orders()))
	}
}

// ---------- 隔离 ----------

// 决策被路由到错的标的必须报错，绝不能拿 ETH 的信号去动 BTC 的仓位。
func TestWorkerRejectsForeignSymbol(t *testing.T) {
	cfg := btcStrategy()
	w, _ := NewWorker(cfg, NewPaperBroker(), WithWorkerLogger(quietLogger()))

	d := decision(ethStrategy(), types.DirectionLong, "2000")
	d.StrategyID = cfg.ID // 策略 ID 对上了，但标的不对
	err := w.Handle(context.Background(), d)
	if err == nil {
		t.Fatal("标的不一致的决策必须被拒绝")
	}
	if !strings.Contains(err.Error(), "隔离") {
		t.Errorf("错误信息应点明标的隔离要求：%v", err)
	}
}

// 一个标的的下单通道持续报错，不得影响另一个标的。
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
		t.Error("BTC 的下单失败应当被记录")
	}
	if stats[eth.ID].Stats.Errors != 0 {
		t.Errorf("ETH 不应受 BTC 故障影响，实际错误数 %d", stats[eth.ID].Stats.Errors)
	}
	if len(ethBroker.Orders()) == 0 {
		t.Error("ETH 应当照常成交")
	}
}

// 一个标的 panic，其它标的必须继续工作。
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
		t.Error("panic 应当被捕获并计入 BTC 的错误统计")
	}
	if len(ethBroker.Orders()) == 0 {
		t.Fatal("ETH 应当完全不受 BTC panic 的影响")
	}
}

type failingBroker struct{}

func (failingBroker) Name() string            { return "failing" }
func (failingBroker) Mode() types.TradingMode { return types.ModePaper }
func (failingBroker) PlaceOrder(context.Context, OrderRequest) (types.Order, error) {
	return types.Order{}, errors.New("交易所连接超时")
}

type panickingBroker struct{}

func (panickingBroker) Name() string            { return "panicking" }
func (panickingBroker) Mode() types.TradingMode { return types.ModePaper }
func (panickingBroker) PlaceOrder(context.Context, OrderRequest) (types.Order, error) {
	panic("下单通道内部崩溃")
}

// ---------- 集成：两个标的、不同组合、独立风控 ----------

// 这是阶段 6 要求的集成测试：BTC 用 A 组合、ETH 用 B 组合同时运行，
// 验证两者互相隔离、风控各自独立生效。
func TestIntegrationTwoSymbolsRunIndependently(t *testing.T) {
	ctx := context.Background()
	sup := NewSupervisor(quietLogger())
	defer sup.Shutdown()

	btc, eth := btcStrategy(), ethStrategy()
	btcBroker, ethBroker := NewPaperBroker(), NewPaperBroker()
	orders := &recordingOrders{}

	// 两个标的关掉止损，让日亏上限成为唯一的强平规则，便于观察隔离。
	btc.Risk.StopLossPct = 0

	if err := sup.Register(ctx, btc, btcBroker, WithOrderRecorder(orders)); err != nil {
		t.Fatal(err)
	}
	if err := sup.Register(ctx, eth, ethBroker, WithOrderRecorder(orders)); err != nil {
		t.Fatal(err)
	}

	// 两个标的同时开仓。
	_ = sup.Dispatch(decision(btc, types.DirectionLong, "100"))
	_ = sup.Dispatch(decision(eth, types.DirectionLong, "2000"))
	sup.Drain()

	btcW, _ := sup.Worker(btc.ID)
	ethW, _ := sup.Worker(eth.ID)
	if !btcW.Position().IsOpen() || !ethW.Position().IsOpen() {
		t.Fatal("两个标的都应当已开仓")
	}

	// BTC 暴跌撞上它自己的日亏上限（50 USDT）；ETH 同幅下跌但上限是 10000，不受影响。
	_ = sup.Dispatch(decision(btc, types.DirectionNeutral, "90"))
	_ = sup.Dispatch(decision(eth, types.DirectionNeutral, "1800"))
	sup.Drain()

	if !btcW.Suspended() {
		t.Error("BTC 应当因触发自己的日亏上限而暂停")
	}
	if btcW.Position().IsOpen() {
		t.Error("BTC 应当已被强制平仓")
	}

	if ethW.Suspended() {
		t.Error("ETH 的风控额度宽松得多，不应被 BTC 的风控牵连")
	}
	if !ethW.Position().IsOpen() {
		t.Error("ETH 的持仓不应被 BTC 的强平影响")
	}

	// BTC 暂停后，ETH 仍能正常开新仓。
	_ = sup.Dispatch(decision(btc, types.DirectionLong, "100"))
	_ = sup.Dispatch(decision(eth, types.DirectionShort, "1800"))
	sup.Drain()

	if btcW.Position().IsOpen() {
		t.Error("暂停中的 BTC 不该再开仓")
	}
	if ethW.Position().Direction != types.DirectionShort {
		t.Errorf("ETH 应当已反手做空，实际方向 %s", ethW.Position().Direction)
	}

	// 全部订单都要带上正确的标的与溯源。
	for _, o := range orders.all() {
		if o.Symbol != "BTCUSDT" && o.Symbol != "ETHUSDT" {
			t.Errorf("出现了未知标的的订单：%s", o.Symbol)
		}
		if o.Provenance.DecisionID == "" {
			t.Errorf("订单 %s 缺少溯源信息", o.ID)
		}
		if o.Mode != types.ModePaper {
			t.Errorf("订单 %s 的模式 = %s，期望 PAPER", o.ID, o.Mode)
		}
	}
	t.Logf("BTC 统计：%+v", btcW.Stats())
	t.Logf("ETH 统计：%+v", ethW.Stats())
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
		t.Fatalf("期望 ErrUnknownStrategy，得到：%v", err)
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
		t.Fatal("同一策略不应被重复注册")
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
		t.Error("注销后不应还能取到执行实例")
	}
	if err := sup.Dispatch(decision(cfg, types.DirectionLong, "100")); !errors.Is(err, ErrUnknownStrategy) {
		t.Errorf("注销后投递应当报未知策略，实际：%v", err)
	}
}

// ---------- 模拟盘通道 ----------

func TestPaperBrokerNeverClaimsToBeLive(t *testing.T) {
	if NewPaperBroker().Mode() != types.ModePaper {
		t.Fatal("模拟通道必须始终标记为 PAPER，否则实盘闸门会失效")
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
		t.Errorf("买入成交价 = %s，期望 101（滑点上浮）", buy.FilledPrice)
	}
	if !sell.FilledPrice.Equal(dec("99")) {
		t.Errorf("卖出成交价 = %s，期望 99（滑点下压）", sell.FilledPrice)
	}
}

func TestBinanceBrokerRefusesNonTestnetURL(t *testing.T) {
	_, err := NewBinanceTestnetBroker(BinanceConfig{
		BaseURL: "https://api.binance.com", APIKey: "k", APISecret: "s",
	})
	if err == nil {
		t.Fatal("必须拒绝指向生产环境的地址")
	}
}

func TestBinanceBrokerIsTreatedAsPaper(t *testing.T) {
	b, err := NewBinanceTestnetBroker(BinanceConfig{APIKey: "k", APISecret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Mode() != types.ModePaper {
		t.Error("测试网用的是模拟资金，必须按模拟盘对待")
	}
}
