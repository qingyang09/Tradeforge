package types

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// ModuleConfig is a single reference to a signal module within a strategy: which
// module, what parameters, how much weight.
type ModuleConfig struct {
	// Module must be a name already registered in the module registry. The Agent
	// must not invent a module that doesn't exist.
	Module string `json:"module"`
	// Params are the module's parameters; keys must fall within that module's
	// RequiredParams() range.
	Params map[string]any `json:"params"`
	// Weight only matters under CombineWeighted, in (0, 1]; normalization across a
	// strategy's modules isn't required.
	Weight float64 `json:"weight,omitempty"`
	// Timeframe is the candle period this module uses; empty means it follows
	// StrategyConfig.Timeframe (the trigger timeframe). If set, it must not be faster
	// than the trigger timeframe — decisions are only computed once per close of the
	// trigger timeframe, so a faster module period would never actually be observed
	// in time. Multiple modules can each use a different (slower-than-trigger)
	// timeframe, e.g. 1-hour key levels for background context and 15-minute volume
	// spikes for the entry trigger.
	Timeframe Timeframe `json:"timeframe,omitempty"`
}

// CombineMode is how module signals get aggregated.
type CombineMode string

const (
	// CombineAll requires every module to give a signal in the same direction to trigger.
	CombineAll CombineMode = "ALL"
	// CombineWeighted aggregates weighted confidence; triggers once it exceeds the threshold.
	CombineWeighted CombineMode = "WEIGHTED"
)

// Valid reports whether the combine mode is supported.
func (c CombineMode) Valid() bool {
	return c == CombineAll || c == CombineWeighted
}

// RiskLevelMode determines how the stop-loss/take-profit price is computed.
type RiskLevelMode string

const (
	// RiskLevelModePct is the default mode: a fixed percentage (see StopLossPct/TakeProfitPct).
	RiskLevelModePct RiskLevelMode = "pct"
	// RiskLevelModeSupportResistance uses the nearest support/resistance level detected
	// by the support_resistance module at the moment the position opens as the
	// stop-loss/take-profit price threshold, instead of a fixed percentage. A long
	// position's stop-loss takes the support level and take-profit the resistance
	// level; a short position is the reverse. This price is computed once at open and
	// locked in — it is not recalculated as the market moves, matching the semantics
	// of a fixed-percentage stop (both pin down a threshold at the instant of opening);
	// it is not a candle-by-candle trailing stop.
	RiskLevelModeSupportResistance RiskLevelMode = "support_resistance"
	// RiskLevelModePOC uses the volume-weighted center of distribution (Point of
	// Control) computed by the poc module at the moment the position opens as the
	// stop-loss/take-profit price threshold. POC has only a single price — unlike
	// support/resistance, which has separate upper/lower levels — so it doesn't
	// distinguish long/short or stop-loss/take-profit direction; the same value is used
	// for both.
	RiskLevelModePOC RiskLevelMode = "poc"
)

// Valid reports whether this is a supported stop-loss/take-profit mode. An empty
// string is treated as RiskLevelModePct (the default).
func (m RiskLevelMode) Valid() bool {
	return m == "" || m == RiskLevelModePct || m == RiskLevelModeSupportResistance || m == RiskLevelModePOC
}

// RequiredModule returns the signal module name this mode depends on; the pct mode
// depends on no module and returns an empty string.
func (m RiskLevelMode) RequiredModule() string {
	switch m.EffectiveOrPct() {
	case RiskLevelModeSupportResistance:
		return "support_resistance"
	case RiskLevelModePOC:
		return "poc"
	default:
		return ""
	}
}

// EffectiveOrPct normalizes an empty value to RiskLevelModePct so callers don't
// have to check for the empty string everywhere.
func (m RiskLevelMode) EffectiveOrPct() RiskLevelMode {
	if m == "" {
		return RiskLevelModePct
	}
	return m
}

// PositionSizingMode determines how the notional size of a single position is computed.
type PositionSizingMode string

const (
	// PositionSizingModeFixedQuote is the default mode: notional size is fixed at
	// MaxPositionSizeQuote, independent of stop-loss distance.
	PositionSizingModeFixedQuote PositionSizingMode = "fixed_quote"
	// PositionSizingModeRiskPct computes size dynamically as "account equity x
	// per-trade risk percentage / stop-loss distance percentage", which depends on
	// the already-resolved absolute stop-loss price — the caller must compute the
	// stop-loss price first, then the position size (unlike the fixed-quote mode,
	// where size is entirely independent of the stop-loss, so either can be computed first).
	PositionSizingModeRiskPct PositionSizingMode = "risk_pct"
)

// Valid reports whether this is a supported position-sizing mode. An empty string
// is treated as PositionSizingModeFixedQuote (the default).
func (m PositionSizingMode) Valid() bool {
	return m == "" || m == PositionSizingModeFixedQuote || m == PositionSizingModeRiskPct
}

// EffectiveOrFixed normalizes an empty value to PositionSizingModeFixedQuote so
// callers don't have to check for the empty string everywhere.
func (m PositionSizingMode) EffectiveOrFixed() PositionSizingMode {
	if m == "" {
		return PositionSizingModeFixedQuote
	}
	return m
}

// RiskConfig holds symbol-level risk-control parameters. Each symbol is configured
// independently and none affect each other.
type RiskConfig struct {
	// MaxPositionSizeQuote is the hard cap on a single position, denominated in the
	// quote currency (e.g. USDT). This value always applies regardless of
	// PositionSizingMode: in fixed_quote mode it is the position size itself; in
	// risk_pct mode, if the size computed by the formula exceeds it, the trade is
	// rejected outright rather than silently clamped — silently shrinking the
	// position would break the semantics the user explicitly asked for ("this trade
	// risks only N% of equity"), which amounts to pretending the rule was faithfully
	// executed when it wasn't.
	MaxPositionSizeQuote decimal.Decimal `json:"max_position_size_quote"`
	// MaxDailyLossQuote is the maximum daily loss; once reached, this symbol's
	// strategy is suspended.
	MaxDailyLossQuote decimal.Decimal `json:"max_daily_loss_quote"`
	// MaxHoldingPeriod is the maximum holding time; a position is force-closed once
	// exceeded. 0 means unlimited.
	MaxHoldingPeriod Duration `json:"max_holding_period"`
	// StopLossMode/TakeProfitMode determine how the stop-loss/take-profit are
	// computed; the default (empty) is RiskLevelModePct. The two are independent of
	// each other, allowing a mix like "stop-loss via support level, take-profit via
	// fixed percentage".
	StopLossMode RiskLevelMode `json:"stop_loss_mode,omitempty"`
	// StopLossPct is the stop-loss percentage, e.g. 0.02 for 2%. Only meaningful when
	// StopLossMode is pct. 0 means unset.
	StopLossPct    float64       `json:"stop_loss_pct,omitempty"`
	TakeProfitMode RiskLevelMode `json:"take_profit_mode,omitempty"`
	// TakeProfitPct is the take-profit percentage. Only meaningful when
	// TakeProfitMode is pct. 0 means unset.
	TakeProfitPct float64 `json:"take_profit_pct,omitempty"`
	// PositionSizingMode determines how a single position's size is computed; the
	// default (empty) is fixed_quote.
	PositionSizingMode PositionSizingMode `json:"position_sizing_mode,omitempty"`
	// AccountEquityQuote is the user-reported account equity (in the quote currency).
	// This is a static number the user declares, not a balance pulled live from the
	// exchange — it does not auto-update while running; the user must come back and
	// update it themselves as their equity changes. Only used when PositionSizingMode
	// is risk_pct.
	AccountEquityQuote decimal.Decimal `json:"account_equity_quote,omitempty"`
	// RiskPerTradePct is the fraction of account equity the user is willing to risk
	// per trade, e.g. 0.01 for 1%. Only used when PositionSizingMode is risk_pct.
	RiskPerTradePct float64 `json:"risk_per_trade_pct,omitempty"`
}

// StrategyConfig is the complete definition of a strategy, and the only structure
// the AI Agent translation layer is allowed to output.
//
// It is "a faithful record of the user's rules": the system's job is only to
// execute it, never to judge whether it's any good.
type StrategyConfig struct {
	ID string `json:"id,omitempty"`
	// UserID is the user this strategy belongs to (introduced by the multi-user SaaS
	// rework). The database's user_id column is authoritative for storage/lookup;
	// this field's value inside the config JSONB may be a zero value or a stale
	// snapshot and must not be trusted (see the GetStrategy/ListStrategies comments
	// in internal/storage/postgres.go).
	UserID string `json:"user_id,omitempty"`
	Name   string `json:"name"`
	// Symbol is the trading pair, e.g. "BTCUSDT". Strategies on different symbols are
	// fully independent.
	Symbol string `json:"symbol"`
	// Timeframe is the strategy's trigger period: a decision is computed exactly once
	// per candle close of this period. Modules may declare a slower period via their
	// own ModuleConfig.Timeframe, in which case each decision feeds them the latest
	// data that has genuinely closed as of the trigger moment — never future data.
	Timeframe Timeframe `json:"timeframe"`
	// Modules is the set of modules participating in this strategy; at least one.
	Modules []ModuleConfig `json:"modules"`
	// Combine is the aggregation mode.
	Combine CombineMode `json:"combine"`
	// Threshold is only used under CombineWeighted: weighted confidence must exceed
	// it to trigger, in (0, 1].
	Threshold float64 `json:"threshold,omitempty"`
	// Risk holds this strategy's (this symbol's) risk-control parameters.
	Risk RiskConfig `json:"risk"`

	// State is the state machine's current state, see strategy_state.go. New
	// strategies always start at StateDraft.
	State StrategyState `json:"state"`

	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	// SourceUtterance stores the user's original natural-language input, for
	// auditing "where did this rule come from".
	SourceUtterance string `json:"source_utterance,omitempty"`
}

// RequiredTimeframes returns every timeframe this strategy actually uses (the
// trigger timeframe itself, plus each module's explicitly declared timeframe),
// deduplicated and sorted lexicographically. The engine, signal engine, and
// backtest replay all use this to decide which market data sources to prepare,
// instead of each re-walking Modules on its own.
func (cfg StrategyConfig) RequiredTimeframes() []Timeframe {
	seen := map[Timeframe]bool{cfg.Timeframe: true}
	for _, mc := range cfg.Modules {
		tf := mc.Timeframe
		if tf == "" {
			tf = cfg.Timeframe
		}
		seen[tf] = true
	}
	out := make([]Timeframe, 0, len(seen))
	for tf := range seen {
		out = append(out, tf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Decision is the combination engine's final aggregated decision, written to
// Kafka and persisted for audit.
type Decision struct {
	// ID is generated by the engine before persisting; an order's Provenance uses it
	// to trace back the triggering basis.
	ID         string    `json:"id"`
	StrategyID string    `json:"strategy_id"`
	Symbol     string    `json:"symbol"`
	Direction  Direction `json:"direction"`
	// Score is the aggregated strength: the mean confidence of participating modules
	// under ALL mode, or the weighted confidence under WEIGHTED mode.
	Score float64 `json:"score"`
	// Triggered being true means this decision met the trigger condition; the
	// execution layer should place an order based on it.
	Triggered bool `json:"triggered"`
	// Signals holds every module's signal that participated in this decision
	// (degraded ones included), for explainability.
	Signals []Signal `json:"signals"`
	// Reason is a purely factual explanation of why it triggered or didn't.
	//
	// A Message (not a string), same reasoning as Signal.Reason: the engine
	// computes this headlessly and it's persisted (Postgres, Kafka) for
	// display much later, possibly by a viewer with a different language
	// preference than whatever was default at compute time.
	Reason    Message         `json:"reason"`
	Price     decimal.Decimal `json:"price"`
	Timestamp time.Time       `json:"timestamp"`
	// EvaluatedAt is the wall-clock time the engine actually finished computing,
	// distinct from Timestamp (the market time).
	EvaluatedAt time.Time `json:"evaluated_at"`
}
