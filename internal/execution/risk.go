package execution

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// RiskVerdict is the outcome of a single risk-control check.
type RiskVerdict int

const (
	// RiskAllow permits the action.
	RiskAllow RiskVerdict = iota
	// RiskReject rejects opening a position, but does not suspend the strategy (e.g. per-trade size exceeded).
	RiskReject
	// RiskForceClose closes the position immediately and suspends the strategy for this symbol.
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

// RiskDecision is a risk-control verdict together with its rationale.
type RiskDecision struct {
	Verdict RiskVerdict
	// Rule is the name of the triggered rule; empty when Verdict is RiskAllow.
	Rule string
	// Reason is a human-readable explanation.
	Reason types.Message
	// Detail is a snapshot of the data at trigger time, written to the audit log.
	Detail map[string]any
}

// Allowed reports whether the action is permitted.
func (d RiskDecision) Allowed() bool { return d.Verdict == RiskAllow }

// RiskManager is the risk controller for a single symbol.
//
// One instance per symbol, sharing no state between them — this is precisely
// how "risk control is independent per symbol" is implemented. BTC maxing
// out its daily loss allowance must not affect ETH continuing to trade.
type RiskManager struct {
	cfg    types.RiskConfig
	symbol string

	// dayKey is the current accounting day (UTC date), reset automatically across days.
	dayKey string
	// dayRealizedLoss is the day's cumulative realized loss so far (a positive number is a loss amount).
	dayRealizedLoss decimal.Decimal
	// halted being true means this symbol has been suspended by risk control and needs manual intervention to resume.
	halted bool
	// haltRule records the rule that caused the suspension.
	haltRule string
}

// NewRiskManager creates a risk controller for a symbol.
func NewRiskManager(symbol string, cfg types.RiskConfig) *RiskManager {
	return &RiskManager{symbol: symbol, cfg: cfg}
}

// Halted reports whether this symbol has been suspended by risk control.
func (r *RiskManager) Halted() bool { return r.halted }

// HaltRule returns the name of the rule that caused the suspension.
func (r *RiskManager) HaltRule() string { return r.haltRule }

// DayRealizedLoss returns the day's cumulative realized loss so far.
func (r *RiskManager) DayRealizedLoss() decimal.Decimal { return r.dayRealizedLoss }

// CheckOpen decides whether opening a position of the given notional amount is allowed.
func (r *RiskManager) CheckOpen(now time.Time, notional decimal.Decimal) RiskDecision {
	r.rollDay(now)

	if r.halted {
		return RiskDecision{
			Verdict: RiskReject, Rule: r.haltRule,
			Reason: types.Msg("execution.risk.halted", "symbol", r.symbol, "rule", r.haltRule),
		}
	}

	if r.cfg.MaxPositionSizeQuote.IsPositive() && notional.GreaterThan(r.cfg.MaxPositionSizeQuote) {
		return RiskDecision{
			Verdict: RiskReject, Rule: "max_position_size",
			Reason: types.Msg("execution.risk.max_position_size",
				"notional", notional.String(), "limit", r.cfg.MaxPositionSizeQuote.String()),
			Detail: map[string]any{
				"requested_notional": notional.String(),
				"limit":              r.cfg.MaxPositionSizeQuote.String(),
			},
		}
	}

	// No new positions once the daily loss cap is reached. This only rejects
	// opening — it does not force-close; handling an existing position is
	// left to CheckPosition, to avoid duplicate actions on the same candle.
	if r.dailyLossBreached() {
		return RiskDecision{
			Verdict: RiskReject, Rule: "max_daily_loss",
			Reason: types.Msg("execution.risk.max_daily_loss_open",
				"loss", r.dayRealizedLoss.String(), "limit", r.cfg.MaxDailyLossQuote.String()),
			Detail: map[string]any{
				"day_realized_loss": r.dayRealizedLoss.String(),
				"limit":             r.cfg.MaxDailyLossQuote.String(),
			},
		}
	}

	return RiskDecision{Verdict: RiskAllow}
}

// CheckPosition decides whether an existing position must be force-closed.
//
// The order here is the priority: daily loss > stop-loss > take-profit >
// holding timeout. Daily loss comes first because it's the only rule that
// also suspends the whole symbol.
func (r *RiskManager) CheckPosition(
	now time.Time, pos types.Position, price decimal.Decimal,
) RiskDecision {
	r.rollDay(now)

	if !pos.IsOpen() {
		return RiskDecision{Verdict: RiskAllow}
	}

	// The daily loss check also counts unrealized P&L: waiting until an
	// unrealized loss becomes realized before suspending would be too late.
	if r.cfg.MaxDailyLossQuote.IsPositive() {
		unrealized := pos.UnrealizedPnL(price)
		projected := r.dayRealizedLoss
		if unrealized.IsNegative() {
			projected = projected.Add(unrealized.Neg())
		}
		if projected.GreaterThanOrEqual(r.cfg.MaxDailyLossQuote) {
			return RiskDecision{
				Verdict: RiskForceClose, Rule: "max_daily_loss",
				Reason: types.Msg("execution.risk.max_daily_loss_force_close",
					"realized", r.dayRealizedLoss.String(), "unrealized", unrealized.Neg().String(), "limit", r.cfg.MaxDailyLossQuote.String()),
				Detail: map[string]any{
					"day_realized_loss": r.dayRealizedLoss.String(),
					"unrealized_pnl":    unrealized.String(),
					"limit":             r.cfg.MaxDailyLossQuote.String(),
				},
			}
		}
	}

	// The absolute stop-loss/take-profit price thresholds were already
	// computed and stored on pos at the moment the position was opened
	// (Worker.openPosition, see ResolveStopLossPrice/ResolveTakeProfitPrice),
	// regardless of whether that used a fixed percentage or
	// support_resistance mode — here we just compare the current price
	// against those two prices; both modes go through exactly the same
	// comparison logic, no need to distinguish between them here.
	if pos.StopLossPrice.IsPositive() {
		triggered := price.LessThanOrEqual(pos.StopLossPrice)
		if pos.Direction == types.DirectionShort {
			triggered = price.GreaterThanOrEqual(pos.StopLossPrice)
		}
		if triggered {
			return RiskDecision{
				Verdict: RiskForceClose, Rule: "stop_loss",
				Reason: types.Msg("execution.risk.stop_loss_triggered",
					"entry", pos.EntryPrice.String(), "price", price.String(), "stop_loss_price", pos.StopLossPrice.String()),
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
				Reason: types.Msg("execution.risk.take_profit_triggered",
					"entry", pos.EntryPrice.String(), "price", price.String(), "take_profit_price", pos.TakeProfitPrice.String()),
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
				Reason: types.Msg("execution.risk.max_holding_triggered",
					"held", held.Truncate(time.Second).String(), "limit", r.cfg.MaxHoldingPeriod.String()),
				Detail: map[string]any{
					"held":  held.String(),
					"limit": r.cfg.MaxHoldingPeriod.String(),
				},
			}
		}
	}

	return RiskDecision{Verdict: RiskAllow}
}

// RecordRealized records a single realized P&L event, for daily loss tracking.
func (r *RiskManager) RecordRealized(now time.Time, pnl decimal.Decimal) {
	r.rollDay(now)
	if pnl.IsNegative() {
		r.dayRealizedLoss = r.dayRealizedLoss.Add(pnl.Neg())
	}
}

// Halt suspends trading for this symbol.
func (r *RiskManager) Halt(rule string) {
	r.halted = true
	r.haltRule = rule
}

// Resume lifts the suspension. This is a manual-operation entry point; the system itself never calls it.
func (r *RiskManager) Resume() {
	r.halted = false
	r.haltRule = ""
}

// rollDay resets the day's stats when the accounting day rolls over.
func (r *RiskManager) rollDay(now time.Time) {
	key := now.UTC().Format("2006-01-02")
	if key != r.dayKey {
		r.dayKey = key
		r.dayRealizedLoss = decimal.Zero
		// Note: rolling over to a new day does NOT auto-lift a suspension. A
		// symbol that tripped risk control must be confirmed by a human before it resumes.
	}
}

func (r *RiskManager) dailyLossBreached() bool {
	return r.cfg.MaxDailyLossQuote.IsPositive() &&
		r.dayRealizedLoss.GreaterThanOrEqual(r.cfg.MaxDailyLossQuote)
}

// ResolveStopLossPrice, ResolveTakeProfitPrice, ResolvePositionSizeQuote, and
// their level-lookup helpers below deliberately still return plain Chinese
// error text (not a types.Message) -- unlike RiskDecision.Reason above, these
// errors are not currently displayed anywhere in the webui (a rejected-open
// caused by one of them ends with the order simply not being placed; there is
// no order/risk-event page rendering this text today). Worker wraps them as
// types.Message{Literal: err.Error()} at the point they become a
// RiskDecision.Reason, the same legacy-literal path used for pre-migration
// audit rows, so nothing is lost -- just not yet worth the added catalog
// surface for text nobody currently sees rendered.
//
// ResolveStopLossPrice computes the absolute stop-loss price threshold at the
// moment a position is opened; it returns the zero value with no error when
// pct is 0 (stop-loss not set). In support_resistance mode it errors when no
// usable key level can be found (none detected nearby, or this signal is a
// degraded product this time) — the caller (Worker.openPosition) should
// reject opening the position on that basis; it must not pretend everything
// is fine just because the price couldn't be computed, when the user
// explicitly asked for stop-loss protection.
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
		// A long's stop-loss sits at the support level (below price); a short's sits at the resistance level (above price).
		return levelPrice(signals, direction != types.DirectionShort, "止损")
	case types.RiskLevelModePOC:
		return pocPrice(signals, "止损")
	default:
		return decimal.Zero, fmt.Errorf("不支持的止损模式 %q", cfg.StopLossMode)
	}
}

// ResolveTakeProfitPrice is the take-profit counterpart of
// ResolveStopLossPrice: a long's take-profit sits at the resistance level, a
// short's at the support level — the opposite direction from stop-loss.
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

// ResolvePositionSizeQuote computes the notional amount (in the quote
// currency) to open. In fixed_quote mode it is simply MaxPositionSizeQuote,
// unrelated to the stop-loss; in risk_pct mode it's computed as "account
// equity × per-trade risk percentage ÷ stop-loss distance percentage",
// relying on the caller having already resolved the absolute stop-loss price
// (stopLossPrice) — it errors when the stop-loss is unset, unresolvable, or
// equal to the entry price (zero distance), and the caller should reject
// opening the position on that basis, for the same reason as
// ResolveStopLossPrice: the user explicitly asked to size by risk
// percentage, so we must not pretend we can size the position when we can't.
//
// This deliberately does NOT clip the result against the MaxPositionSizeQuote
// cap — that check is left to RiskManager.CheckOpen, which rejects the whole
// trade outright when the cap is exceeded rather than silently shrinking it:
// silently shrinking would break the "this position corresponds to N% equity
// risk" semantics the user explicitly asked for; the user needs to know that
// their configured risk percentage/stop-loss distance can't open a position
// this large under the current cap.
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

// levelPrice pulls the nearest support/resistance level detected by the
// support_resistance module from this decision's signals. wantSupport true
// selects the support level, otherwise the resistance level.
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

// pocPrice pulls the volume-profile point of control computed by the poc
// module from this decision's signals. POC is a single price, unlike
// support/resistance which splits into an upper and lower level, so it needs
// no direction parameter like wantSupport — stop-loss and take-profit use the
// same value, for both longs and shorts alike.
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
