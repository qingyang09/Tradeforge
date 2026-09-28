package execution

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// RiskVerdict 是一次风控判定的结果。
type RiskVerdict int

const (
	// RiskAllow 允许该动作。
	RiskAllow RiskVerdict = iota
	// RiskReject 拒绝开仓，但不暂停策略（如单笔仓位超限）。
	RiskReject
	// RiskForceClose 立即平仓并暂停该标的的策略。
	RiskForceClose
)

func (v RiskVerdict) String() string {
	switch v {
	case RiskAllow:
		return "allow"
	case RiskReject:
		return "reject"
	case RiskForceClose:
		return "force_close"
	default:
		return "unknown"
	}
}

// RiskDecision 是风控判定及其依据。
type RiskDecision struct {
	Verdict RiskVerdict
	// Rule 是触发的规则名，RiskAllow 时为空。
	Rule string
	// Reason 是人类可读的说明。
	Reason string
	// Detail 是触发时的数据快照，写入审计。
	Detail map[string]any
}

// Allowed 报告是否放行。
func (d RiskDecision) Allowed() bool { return d.Verdict == RiskAllow }

// RiskManager 是单个标的的风控器。
//
// 每个标的一个实例，状态互不共享——这正是"风控在标的级别独立"的实现方式。
// BTC 打满单日亏损额度，不该影响 ETH 继续交易。
type RiskManager struct {
	cfg    types.RiskConfig
	symbol string

	// dayKey 是当前统计日（UTC 日期），跨日自动重置。
	dayKey string
	// dayRealizedLoss 是当日累计已实现亏损（正数表示亏损额）。
	dayRealizedLoss decimal.Decimal
	// halted 为 true 表示该标的已被风控暂停，需人工介入才能恢复。
	halted bool
	// haltRule 记录导致暂停的规则。
	haltRule string
}

// NewRiskManager 为某个标的创建风控器。
func NewRiskManager(symbol string, cfg types.RiskConfig) *RiskManager {
	return &RiskManager{symbol: symbol, cfg: cfg}
}

// Halted 报告该标的是否已被风控暂停。
func (r *RiskManager) Halted() bool { return r.halted }

// HaltRule 返回导致暂停的规则名。
func (r *RiskManager) HaltRule() string { return r.haltRule }

// DayRealizedLoss 返回当日累计已实现亏损。
func (r *RiskManager) DayRealizedLoss() decimal.Decimal { return r.dayRealizedLoss }

// CheckOpen 判定是否允许按给定名义金额开仓。
func (r *RiskManager) CheckOpen(now time.Time, notional decimal.Decimal) RiskDecision {
	r.rollDay(now)

	if r.halted {
		return RiskDecision{
			Verdict: RiskReject, Rule: r.haltRule,
			Reason: fmt.Sprintf("标的 %s 已被风控暂停（规则 %s），不再开新仓", r.symbol, r.haltRule),
		}
	}

	if r.cfg.MaxPositionSizeQuote.IsPositive() && notional.GreaterThan(r.cfg.MaxPositionSizeQuote) {
		return RiskDecision{
			Verdict: RiskReject, Rule: "max_position_size",
			Reason: fmt.Sprintf("拟开仓名义金额 %s 超过单笔上限 %s",
				notional, r.cfg.MaxPositionSizeQuote),
			Detail: map[string]any{
				"requested_notional": notional.String(),
				"limit":              r.cfg.MaxPositionSizeQuote.String(),
			},
		}
	}

	// 单日亏损已达上限时不再开新仓。这里只拒绝开仓、不强平——
	// 已有持仓的处置交给 CheckPosition，避免同一根 K 线上重复动作。
	if r.dailyLossBreached() {
		return RiskDecision{
			Verdict: RiskReject, Rule: "max_daily_loss",
			Reason: fmt.Sprintf("当日已实现亏损 %s 达到上限 %s，当日不再开新仓",
				r.dayRealizedLoss, r.cfg.MaxDailyLossQuote),
			Detail: map[string]any{
				"day_realized_loss": r.dayRealizedLoss.String(),
				"limit":             r.cfg.MaxDailyLossQuote.String(),
			},
		}
	}

	return RiskDecision{Verdict: RiskAllow}
}

// CheckPosition 判定已有持仓是否需要被强制平仓。
//
// 顺序即优先级：单日亏损 > 止损 > 止盈 > 持仓超时。
// 单日亏损排在最前，因为它是唯一会连带暂停整个标的的规则。
func (r *RiskManager) CheckPosition(
	now time.Time, pos types.Position, price decimal.Decimal,
) RiskDecision {
	r.rollDay(now)

	if !pos.IsOpen() {
		return RiskDecision{Verdict: RiskAllow}
	}

	// 单日亏损把浮亏也算进去：等浮亏变成已实现再暂停就太晚了。
	if r.cfg.MaxDailyLossQuote.IsPositive() {
		unrealized := pos.UnrealizedPnL(price)
		projected := r.dayRealizedLoss
		if unrealized.IsNegative() {
			projected = projected.Add(unrealized.Neg())
		}
		if projected.GreaterThanOrEqual(r.cfg.MaxDailyLossQuote) {
			return RiskDecision{
				Verdict: RiskForceClose, Rule: "max_daily_loss",
				Reason: fmt.Sprintf("当日亏损（已实现 %s + 浮亏 %s）达到上限 %s，强制平仓并暂停该标的",
					r.dayRealizedLoss, unrealized.Neg(), r.cfg.MaxDailyLossQuote),
				Detail: map[string]any{
					"day_realized_loss": r.dayRealizedLoss.String(),
					"unrealized_pnl":    unrealized.String(),
					"limit":             r.cfg.MaxDailyLossQuote.String(),
				},
			}
		}
	}

	// 止损/止盈的绝对价格阈值在开仓那一刻（Worker.openPosition，见 ResolveStopLossPrice/
	// ResolveTakeProfitPrice）就已经算好存在 pos 上了，不管当初是固定百分比还是
	// support_resistance 模式——这里只需要拿当前价跟这两个价格比，两种模式走的是
	// 完全相同的比较逻辑，不需要在这里区分。
	if pos.StopLossPrice.IsPositive() {
		triggered := price.LessThanOrEqual(pos.StopLossPrice)
		if pos.Direction == types.DirectionShort {
			triggered = price.GreaterThanOrEqual(pos.StopLossPrice)
		}
		if triggered {
			return RiskDecision{
				Verdict: RiskForceClose, Rule: "stop_loss",
				Reason: fmt.Sprintf("触发止损：入场 %s，当前 %s，止损价 %s",
					pos.EntryPrice, price, pos.StopLossPrice),
				Detail: map[string]any{
					"entry_price":     pos.EntryPrice.String(),
					"price":           price.String(),
					"stop_loss_price": pos.StopLossPrice.String(),
				},
			}
		}
	}

	if pos.TakeProfitPrice.IsPositive() {
		triggered := price.GreaterThanOrEqual(pos.TakeProfitPrice)
		if pos.Direction == types.DirectionShort {
			triggered = price.LessThanOrEqual(pos.TakeProfitPrice)
		}
		if triggered {
			return RiskDecision{
				Verdict: RiskForceClose, Rule: "take_profit",
				Reason: fmt.Sprintf("触发止盈：入场 %s，当前 %s，止盈价 %s",
					pos.EntryPrice, price, pos.TakeProfitPrice),
				Detail: map[string]any{
					"entry_price":       pos.EntryPrice.String(),
					"price":             price.String(),
					"take_profit_price": pos.TakeProfitPrice.String(),
				},
			}
		}
	}

	if r.cfg.MaxHoldingPeriod > 0 && !pos.OpenedAt.IsZero() {
		held := now.Sub(pos.OpenedAt)
		if held >= r.cfg.MaxHoldingPeriod.Std() {
			return RiskDecision{
				Verdict: RiskForceClose, Rule: "max_holding",
				Reason: fmt.Sprintf("持仓已达 %s，超过上限 %s，强制平仓",
					held.Truncate(time.Second), r.cfg.MaxHoldingPeriod),
				Detail: map[string]any{
					"held":  held.String(),
					"limit": r.cfg.MaxHoldingPeriod.String(),
				},
			}
		}
	}

	return RiskDecision{Verdict: RiskAllow}
}

// RecordRealized 记录一次已实现盈亏，用于单日亏损统计。
func (r *RiskManager) RecordRealized(now time.Time, pnl decimal.Decimal) {
	r.rollDay(now)
	if pnl.IsNegative() {
		r.dayRealizedLoss = r.dayRealizedLoss.Add(pnl.Neg())
	}
}

// Halt 暂停该标的的交易。
func (r *RiskManager) Halt(rule string) {
	r.halted = true
	r.haltRule = rule
}

// Resume 解除暂停。这是人工操作的入口，系统自身绝不调用。
func (r *RiskManager) Resume() {
	r.halted = false
	r.haltRule = ""
}

// rollDay 跨日时重置当日统计。
func (r *RiskManager) rollDay(now time.Time) {
	key := now.UTC().Format("2006-01-02")
	if key != r.dayKey {
		r.dayKey = key
		r.dayRealizedLoss = decimal.Zero
		// 注意：跨日不自动解除暂停。风控触发过的标的必须由人确认后才恢复。
	}
}

func (r *RiskManager) dailyLossBreached() bool {
	return r.cfg.MaxDailyLossQuote.IsPositive() &&
		r.dayRealizedLoss.GreaterThanOrEqual(r.cfg.MaxDailyLossQuote)
}

// ResolveStopLossPrice 在开仓那一刻算出止损的绝对价格阈值，pct 为 0（未设置止损）时
// 返回零值、不报错。support_resistance 模式下找不到可用的关键位（附近没有探测到、
// 或该信号本次是降级产物）时报错——调用方（Worker.openPosition）应当据此拒绝开仓，
// 不能在用户明确要求止损保护的情况下，因为算不出价格就假装没有这回事。
func ResolveStopLossPrice(
	cfg types.RiskConfig, direction types.Direction, entryPrice decimal.Decimal, signals []types.Signal,
) (decimal.Decimal, error) {
	switch cfg.StopLossMode.EffectiveOrPct() {
	case types.RiskLevelModePct:
		if cfg.StopLossPct <= 0 {
			return decimal.Zero, nil
		}
		return adversePctPrice(direction, entryPrice, cfg.StopLossPct), nil
	case types.RiskLevelModeSupportResistance:
		// 多头止损设在支撑位（价格下方），空头止损设在阻力位（价格上方）。
		return levelPrice(signals, direction != types.DirectionShort, "止损")
	case types.RiskLevelModePOC:
		return pocPrice(signals, "止损")
	default:
		return decimal.Zero, fmt.Errorf("不支持的止损模式 %q", cfg.StopLossMode)
	}
}

// ResolveTakeProfitPrice 是 ResolveStopLossPrice 的止盈版本：多头止盈设在阻力位，
// 空头止盈设在支撑位——跟止损方向相反。
func ResolveTakeProfitPrice(
	cfg types.RiskConfig, direction types.Direction, entryPrice decimal.Decimal, signals []types.Signal,
) (decimal.Decimal, error) {
	switch cfg.TakeProfitMode.EffectiveOrPct() {
	case types.RiskLevelModePct:
		if cfg.TakeProfitPct <= 0 {
			return decimal.Zero, nil
		}
		return favorablePctPrice(direction, entryPrice, cfg.TakeProfitPct), nil
	case types.RiskLevelModeSupportResistance:
		return levelPrice(signals, direction == types.DirectionShort, "止盈")
	case types.RiskLevelModePOC:
		return pocPrice(signals, "止盈")
	default:
		return decimal.Zero, fmt.Errorf("不支持的止盈模式 %q", cfg.TakeProfitMode)
	}
}

// ResolvePositionSizeQuote 算出开仓的名义金额（计价货币）。fixed_quote 模式下就是
// MaxPositionSizeQuote 本身，跟止损无关；risk_pct 模式下按"账户权益 × 单笔风险比例 ÷
// 止损距离百分比"算，依赖调用方已经解析好的止损绝对价格（stopLossPrice）——止损没
// 设置或算不出来、或止损价等于入场价（距离为 0）时报错，调用方应据此拒绝开仓，理由
// 跟 ResolveStopLossPrice 一样：用户明确要求按风险百分比开仓，算不出来就不该假装能开。
//
// 这里刻意不对结果做 MaxPositionSizeQuote 上限的裁剪——上限检查交给 RiskManager.CheckOpen
// 做，超限就整笔拒绝，不做静默缩小：静默缩小会破坏"这笔仓位对应 N% 权益风险"这个用户
// 明确要的语义，用户需要知道自己配的风险比例/止损距离在当前上限下开不出这么大的仓位。
func ResolvePositionSizeQuote(
	cfg types.RiskConfig, entryPrice, stopLossPrice decimal.Decimal,
) (decimal.Decimal, error) {
	switch cfg.PositionSizingMode.EffectiveOrFixed() {
	case types.PositionSizingModeFixedQuote:
		return cfg.MaxPositionSizeQuote, nil
	case types.PositionSizingModeRiskPct:
		if !stopLossPrice.IsPositive() {
			return decimal.Zero, fmt.Errorf("按风险百分比开仓需要先有可用的止损价，但本次止损未设置或算不出来")
		}
		stopDistance := entryPrice.Sub(stopLossPrice).Abs()
		if stopDistance.IsZero() {
			return decimal.Zero, fmt.Errorf("止损价与入场价相同，止损距离为 0，无法据此计算仓位")
		}
		stopDistancePct := stopDistance.Div(entryPrice)
		riskAmount := cfg.AccountEquityQuote.Mul(decimal.NewFromFloat(cfg.RiskPerTradePct))
		return riskAmount.Div(stopDistancePct), nil
	default:
		return decimal.Zero, fmt.Errorf("不支持的仓位模式 %q", cfg.PositionSizingMode)
	}
}

func adversePctPrice(direction types.Direction, entry decimal.Decimal, pct float64) decimal.Decimal {
	factor := decimal.NewFromFloat(pct)
	if direction == types.DirectionShort {
		return entry.Mul(decimal.NewFromInt(1).Add(factor))
	}
	return entry.Mul(decimal.NewFromInt(1).Sub(factor))
}

func favorablePctPrice(direction types.Direction, entry decimal.Decimal, pct float64) decimal.Decimal {
	factor := decimal.NewFromFloat(pct)
	if direction == types.DirectionShort {
		return entry.Mul(decimal.NewFromInt(1).Sub(factor))
	}
	return entry.Mul(decimal.NewFromInt(1).Add(factor))
}

// levelPrice 从本次决策的信号里取 support_resistance 模块检测到的最近支撑/阻力位。
// wantSupport 为 true 时取支撑位，否则取阻力位。
func levelPrice(signals []types.Signal, wantSupport bool, purpose string) (decimal.Decimal, error) {
	sig, ok := findSignal(signals, "support_resistance")
	if !ok {
		return decimal.Zero, fmt.Errorf("%s 需要 support_resistance 模块的数据，但本次决策的信号里没有", purpose)
	}
	if sig.Degraded {
		return decimal.Zero, fmt.Errorf("%s 需要 support_resistance 模块的数据，但该模块本次是降级信号", purpose)
	}

	key, label := "nearest_resistance", "阻力位"
	if wantSupport {
		key, label = "nearest_support", "支撑位"
	}

	raw, ok := sig.Raw[key]
	if !ok || raw == nil {
		return decimal.Zero, fmt.Errorf("%s：当前价格附近没有检测到%s，无法据此设置%s", purpose, label, purpose)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return decimal.Zero, fmt.Errorf("%s：%s 数据格式异常（%T）", purpose, label, raw)
	}
	priceStr, ok := m["price"].(string)
	if !ok {
		return decimal.Zero, fmt.Errorf("%s：%s 数据里缺少 price 字段", purpose, label)
	}
	price, err := decimal.NewFromString(priceStr)
	if err != nil {
		return decimal.Zero, fmt.Errorf("%s：解析%s价格 %q 失败：%w", purpose, label, priceStr, err)
	}
	return price, nil
}

// pocPrice 从本次决策的信号里取 poc 模块算出的成交量分布重心。POC 只有一个价格，
// 不像支撑/阻力位分上下两个，所以不需要 wantSupport 这样的方向参数——
// 止损止盈用的是同一个值，多空双方都一样。
func pocPrice(signals []types.Signal, purpose string) (decimal.Decimal, error) {
	sig, ok := findSignal(signals, "poc")
	if !ok {
		return decimal.Zero, fmt.Errorf("%s 需要 poc 模块的数据，但本次决策的信号里没有", purpose)
	}
	if sig.Degraded {
		return decimal.Zero, fmt.Errorf("%s 需要 poc 模块的数据，但该模块本次是降级信号", purpose)
	}
	priceStr, ok := sig.Raw["poc_price"].(string)
	if !ok || priceStr == "" {
		return decimal.Zero, fmt.Errorf("%s：poc 模块的信号里没有算出有效的 poc_price，无法据此设置%s", purpose, purpose)
	}
	price, err := decimal.NewFromString(priceStr)
	if err != nil {
		return decimal.Zero, fmt.Errorf("%s：解析 POC 价格 %q 失败：%w", purpose, priceStr, err)
	}
	return price, nil
}

func findSignal(signals []types.Signal, module string) (types.Signal, bool) {
	for _, s := range signals {
		if s.Module == module {
			return s, true
		}
	}
	return types.Signal{}, false
}
