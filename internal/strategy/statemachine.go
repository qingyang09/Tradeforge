package strategy

import (
	"errors"
	"fmt"
	"time"

	"tradeforge/internal/i18n"
	"tradeforge/pkg/types"
)

// The state machine is the platform's safety gate, not a UX flourish.
//
// The legal path is fixed:
//
//	DRAFT → BACKTESTED → PAPER_TRADING → LIVE_ELIGIBLE → LIVE
//
// Three rules that may never be bypassed:
//  1. Every step must advance one level at a time, no skipping (especially DRAFT
//     straight to LIVE)
//  2. Every step except the last must be backed by data (a backtest result / paper
//     trading statistics)
//  3. LIVE_ELIGIBLE → LIVE must be the user's explicit manual action, the system may
//     never advance this automatically

// allowedTransitions is the single whitelist of legal transitions. Anything not in
// this table is rejected.
var allowedTransitions = map[types.StrategyState][]types.StrategyState{
	types.StateDraft:        {types.StateBacktested},
	types.StateBacktested:   {types.StatePaperTrading, types.StateDraft},
	types.StatePaperTrading: {types.StateLiveEligible, types.StateSuspended},
	types.StateLiveEligible: {types.StateLive, types.StateSuspended},
	types.StateLive:         {types.StateSuspended},
	// After a pause, the only way back is through paper trading again -- a
	// strategy that tripped a risk control has to re-prove itself, it can't
	// resume live trading directly.
	types.StateSuspended: {types.StatePaperTrading},
}

// Actor identifies who initiated a transition.
type Actor string

const (
	// ActorSystem means the system advanced this automatically based on data
	// (backtest completed, paper trading cleared the gate, etc.).
	ActorSystem Actor = "system"
	// ActorUser means a human did this manually.
	ActorUser Actor = "user"
)

// Gate is the set of thresholds required to advance to a given state.
type Gate struct {
	// MinOutOfSampleSharpe is the minimum out-of-sample Sharpe ratio required to
	// enter PAPER_TRADING.
	MinOutOfSampleSharpe float64
	// MaxDrawdownLimit is the allowed max-drawdown ceiling (0.5 means 50%).
	MaxDrawdownLimit float64
	// MinOutOfSampleTrades is the minimum trade count required in the
	// out-of-sample segment. Metrics from one or two trades aren't
	// statistically meaningful -- that's equivalent to not having been
	// verified at all.
	MinOutOfSampleTrades int
	// MaxFeeDragRatio is the cap on what fraction of out-of-sample gross
	// profit fees are allowed to consume (0.5 means 50%). 0 disables this
	// check. Sharpe/return are already fee-adjusted numbers on their own;
	// this is a separate, independent signal: a strategy with very high
	// turnover trading on thin edges might just barely clear the other gates
	// on net numbers while being extremely sensitive to fee/slippage
	// changes, which is worth flagging on its own.
	MaxFeeDragRatio float64
	// MinPaperDuration is the minimum paper-trading run duration.
	MinPaperDuration time.Duration
	// MinPaperTrades is the minimum number of paper-trading fills.
	MinPaperTrades int
}

// DefaultGate is the platform's default set of thresholds.
//
// Conservative defaults are intentional: a gate that's too loose is
// equivalent to no gate at all, and a user can always raise it in their own
// config -- but shouldn't casually be able to lower it.
func DefaultGate() Gate {
	return Gate{
		MinOutOfSampleSharpe: 0,
		MaxDrawdownLimit:     0.5,
		MinOutOfSampleTrades: 5,
		MaxFeeDragRatio:      0.5,
		MinPaperDuration:     7 * 24 * time.Hour,
		MinPaperTrades:       10,
	}
}

// PaperStats is a strategy's paper-trading run statistics, used to decide
// whether it can advance to LIVE_ELIGIBLE.
type PaperStats struct {
	StartedAt  time.Time
	Now        time.Time
	TradeCount int
}

// Duration returns how long paper trading has been running.
func (p PaperStats) Duration() time.Duration {
	if p.StartedAt.IsZero() || p.Now.IsZero() {
		return 0
	}
	return p.Now.Sub(p.StartedAt)
}

// TransitionRequest is one request to advance a strategy's state.
type TransitionRequest struct {
	From types.StrategyState
	To   types.StrategyState
	// Actor is who initiated this. LIVE_ELIGIBLE → LIVE only accepts ActorUser.
	Actor Actor
	// ActorID is the specific operator identity, written to the audit log.
	ActorID string
	// Reason is why this transition is happening; required.
	Reason string
	// Backtest backs a DRAFT → BACKTESTED transition.
	Backtest *types.BacktestResult
	// Paper backs a PAPER_TRADING → LIVE_ELIGIBLE transition.
	Paper *PaperStats
}

// TransitionError means a transition was rejected. Reason is the structured,
// translatable explanation -- render it with internal/i18n.Render(lang, ...)
// for a user-facing message; Error() renders it in English as a stable,
// language-independent string for logs and Go error-handling code that just
// wants *a* message, not a UI-quality one.
type TransitionError struct {
	From, To types.StrategyState
	Reason   types.Message
	// wrapped lets errors.Is/As see through to a sentinel (e.g. ErrManualOnly)
	// when this error was built around one.
	wrapped error
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("transition rejected from %s to %s: %s", e.From, e.To, i18n.Render(i18n.LangEN, e.Reason))
}

func (e *TransitionError) Unwrap() error { return e.wrapped }

// ErrManualOnly means this transition must be initiated manually by a user.
var ErrManualOnly = errors.New("this transition must be an explicit manual user action; the system may not advance it automatically")

// Evidence is the data snapshot a transition was decided on, written to the
// audit log.
type Evidence map[string]any

// CheckTransition validates whether a transition is allowed, and returns the
// evidence snapshot to write to the audit log.
//
// It's a pure function: it never touches the database or mutates state. That
// means every rule in the state machine can be covered directly by unit
// tests, without standing up any infrastructure.
func CheckTransition(req TransitionRequest, gate Gate) (Evidence, error) {
	if !req.From.Valid() {
		return nil, &TransitionError{req.From, req.To, types.Msg("strategy.transition.invalid_from_state", "state", string(req.From)), nil}
	}
	if !req.To.Valid() {
		return nil, &TransitionError{req.From, req.To, types.Msg("strategy.transition.invalid_to_state", "state", string(req.To)), nil}
	}
	if req.From == req.To {
		return nil, &TransitionError{req.From, req.To, types.Msg("strategy.transition.same_state"), nil}
	}
	if req.Reason == "" {
		return nil, &TransitionError{req.From, req.To, types.Msg("strategy.transition.reason_required"), nil}
	}

	if !isAllowed(req.From, req.To) {
		return nil, &TransitionError{req.From, req.To, types.Msg("strategy.transition.illegal_path",
			"from", string(req.From), "allowed", fmt.Sprint(allowedTransitions[req.From])), nil}
	}

	switch {
	case req.From == types.StateDraft && req.To == types.StateBacktested:
		return checkBacktestGate(req, gate)

	case req.To == types.StateLive:
		// The platform's most important gate: live trading can only be
		// nodded through by a human.
		if req.Actor != ActorUser {
			return nil, &TransitionError{req.From, req.To,
				types.Msg("strategy.transition.manual_only", "from", string(req.From), "to", string(req.To)), ErrManualOnly}
		}
		if req.ActorID == "" {
			return nil, &TransitionError{req.From, req.To, types.Msg("strategy.transition.live_requires_actor_id"), nil}
		}
		return Evidence{
			"unlocked_by": req.ActorID,
			"unlocked_at": time.Now().UTC().Format(time.RFC3339),
		}, nil

	case req.From == types.StatePaperTrading && req.To == types.StateLiveEligible:
		return checkPaperGate(req, gate)

	default:
		// Every other transition (entering paper trading, pausing, reverting
		// to draft) has no data gate.
		return Evidence{"actor": string(req.Actor), "actor_id": req.ActorID}, nil
	}
}

func isAllowed(from, to types.StrategyState) bool {
	for _, s := range allowedTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// GateCriterion is one specific check within a gate: its current value,
// required value, and whether it passes.
//
// EvaluateBacktestGate/EvaluatePaperGate is the exact same computation
// checkBacktestGate/checkPaperGate uses to make the real pass/fail call --
// every row the UI shows under "what's left before the next step" has to
// come from the same code that decides whether the state can actually
// advance, or the two will quietly drift apart from each other.
type GateCriterion struct {
	// Label is the name of this check, e.g. "Out-of-sample Sharpe ratio".
	Label types.Message
	// Current is the display text for the current value.
	Current types.Message
	// Required is the display text for the threshold, e.g. "> 0.000".
	Required types.Message
	Pass     bool
	// Reason only matters when Pass=false -- it's the specific explanation a
	// TransitionError uses when this criterion is what rejected the transition.
	Reason types.Message
}

// plainValue wraps an already-formatted number/duration/etc. with no
// translatable words around it -- the same text in every language, just
// routed through the catalog for a consistent rendering path.
func plainValue(v string) types.Message { return types.Msg("strategy.gate.plain", "value", v) }

// EvaluateBacktestGate works out the backtest gate's checks one at a time.
//
// Only out-of-sample metrics are considered: good-looking in-sample numbers
// might just be parameters overfit to that segment's data -- using them as
// the gate would be equivalent to having no gate.
func EvaluateBacktestGate(bt *types.BacktestResult, gate Gate) []GateCriterion {
	oos := bt.OutOfSample
	criteria := []GateCriterion{
		{
			Label:    types.Msg("strategy.gate.oos_trades.label"),
			Current:  types.Msg("strategy.gate.count_trades", "count", oos.TradeCount),
			Required: types.Msg("strategy.gate.min_trades", "count", gate.MinOutOfSampleTrades),
			Pass:     oos.TradeCount >= gate.MinOutOfSampleTrades,
			Reason: types.Msg("strategy.gate.oos_trades.reason",
				"count", oos.TradeCount, "min", gate.MinOutOfSampleTrades),
		},
		{
			Label:    types.Msg("strategy.gate.oos_sharpe.label"),
			Current:  plainValue(fmt.Sprintf("%.3f", oos.SharpeRatio)),
			Required: types.Msg("strategy.gate.gt", "value", fmt.Sprintf("%.3f", gate.MinOutOfSampleSharpe)),
			Pass:     oos.SharpeRatio > gate.MinOutOfSampleSharpe,
			Reason: types.Msg("strategy.gate.oos_sharpe.reason",
				"value", fmt.Sprintf("%.3f", oos.SharpeRatio), "min", fmt.Sprintf("%.3f", gate.MinOutOfSampleSharpe)),
		},
	}
	if gate.MaxDrawdownLimit > 0 {
		criteria = append(criteria, GateCriterion{
			Label:    types.Msg("strategy.gate.oos_drawdown.label"),
			Current:  types.Msg("strategy.gate.pct", "pct", fmt.Sprintf("%.2f", oos.MaxDrawdown*100)),
			Required: types.Msg("strategy.gate.lte_pct", "pct", fmt.Sprintf("%.2f", gate.MaxDrawdownLimit*100)),
			Pass:     oos.MaxDrawdown <= gate.MaxDrawdownLimit,
			Reason: types.Msg("strategy.gate.oos_drawdown.reason",
				"pct", fmt.Sprintf("%.2f", oos.MaxDrawdown*100), "max", fmt.Sprintf("%.2f", gate.MaxDrawdownLimit*100)),
		})
	}
	if gate.MaxFeeDragRatio > 0 {
		if feeDrag, ok := oos.FeeDragRatio(); ok {
			criteria = append(criteria, GateCriterion{
				Label:    types.Msg("strategy.gate.fee_drag.label"),
				Current:  types.Msg("strategy.gate.pct", "pct", fmt.Sprintf("%.1f", feeDrag*100)),
				Required: types.Msg("strategy.gate.lte_pct", "pct", fmt.Sprintf("%.1f", gate.MaxFeeDragRatio*100)),
				Pass:     feeDrag <= gate.MaxFeeDragRatio,
				Reason: types.Msg("strategy.gate.fee_drag.reason",
					"pct", fmt.Sprintf("%.1f", feeDrag*100), "max", fmt.Sprintf("%.1f", gate.MaxFeeDragRatio*100)),
			})
		} else {
			// No positive gross profit means no ratio to compute -- this
			// doesn't block the gate (matches the check below), but the
			// criterion still needs to be shown; the UI shouldn't quietly
			// drop a gate row.
			criteria = append(criteria, GateCriterion{
				Label:    types.Msg("strategy.gate.fee_drag.label"),
				Current:  types.Msg("strategy.gate.fee_drag.unknown"),
				Required: types.Msg("strategy.gate.lte_pct", "pct", fmt.Sprintf("%.1f", gate.MaxFeeDragRatio*100)),
				Pass:     true,
			})
		}
	}
	return criteria
}

// checkBacktestGate validates whether a backtest result clears the gate.
func checkBacktestGate(req TransitionRequest, gate Gate) (Evidence, error) {
	if req.Backtest == nil {
		return nil, &TransitionError{req.From, req.To, types.Msg("strategy.transition.missing_backtest"), nil}
	}
	oos := req.Backtest.OutOfSample

	ev := Evidence{
		"backtest_id":          req.Backtest.ID,
		"out_of_sample_sharpe": oos.SharpeRatio,
		"out_of_sample_trades": oos.TradeCount,
		"out_of_sample_maxdd":  oos.MaxDrawdown,
		"out_of_sample_return": oos.TotalReturn,
		"engine_version":       req.Backtest.EngineVersion,
		"gate_min_sharpe":      gate.MinOutOfSampleSharpe,
		"gate_min_trades":      gate.MinOutOfSampleTrades,
		"gate_max_drawdown":    gate.MaxDrawdownLimit,
		"gate_max_fee_drag":    gate.MaxFeeDragRatio,
	}
	if feeDrag, ok := oos.FeeDragRatio(); ok {
		ev["out_of_sample_fee_drag"] = feeDrag
	}

	for _, c := range EvaluateBacktestGate(req.Backtest, gate) {
		if !c.Pass {
			return nil, &TransitionError{req.From, req.To, c.Reason, nil}
		}
	}

	return ev, nil
}

// EvaluatePaperGate works out the paper-trading gate's checks one at a time.
//
// Both duration and trade count must be satisfied: clearing the duration bar
// with zero fills means the strategy essentially never triggers in real
// market conditions; clearing the trade-count bar in too short a time means
// it's only ever seen one kind of market.
func EvaluatePaperGate(paper *PaperStats, gate Gate) []GateCriterion {
	elapsed := paper.Duration()
	// Rounded to the second for display -- elapsed is "now minus start time"
	// and carries a meaningless string of nanosecond digits (e.g.
	// 72h0m17.1800002s); second-level precision is plenty for a human to
	// read. Pass/fail still uses the unrounded elapsed, unaffected by this
	// display formatting.
	elapsedDisplay := elapsed.Round(time.Second)
	return []GateCriterion{
		{
			Label:    types.Msg("strategy.gate.paper_duration.label"),
			Current:  plainValue(elapsedDisplay.String()),
			Required: types.Msg("strategy.gate.gte_value", "value", gate.MinPaperDuration.String()),
			Pass:     elapsed >= gate.MinPaperDuration,
			Reason: types.Msg("strategy.gate.paper_duration.reason",
				"duration", elapsedDisplay.String(), "min", gate.MinPaperDuration.String()),
		},
		{
			Label:    types.Msg("strategy.gate.paper_trades.label"),
			Current:  types.Msg("strategy.gate.count_trades", "count", paper.TradeCount),
			Required: types.Msg("strategy.gate.min_trades", "count", gate.MinPaperTrades),
			Pass:     paper.TradeCount >= gate.MinPaperTrades,
			Reason: types.Msg("strategy.gate.paper_trades.reason",
				"count", paper.TradeCount, "min", gate.MinPaperTrades),
		},
	}
}

// checkPaperGate validates whether paper trading has run long enough and
// filled enough trades.
func checkPaperGate(req TransitionRequest, gate Gate) (Evidence, error) {
	if req.Paper == nil {
		return nil, &TransitionError{req.From, req.To, types.Msg("strategy.transition.missing_paper_stats"), nil}
	}
	elapsed := req.Paper.Duration()

	ev := Evidence{
		"paper_started_at":  req.Paper.StartedAt.UTC().Format(time.RFC3339),
		"paper_duration":    elapsed.String(),
		"paper_trade_count": req.Paper.TradeCount,
		"gate_min_duration": gate.MinPaperDuration.String(),
		"gate_min_trades":   gate.MinPaperTrades,
	}

	for _, c := range EvaluatePaperGate(req.Paper, gate) {
		if !c.Pass {
			return nil, &TransitionError{req.From, req.To, c.Reason, nil}
		}
	}

	return ev, nil
}

// CanTradeLive reports whether a state is allowed to place real orders.
//
// The execution layer should call this before every single order placement.
// Centralizing this check in one place exists specifically to avoid "one
// branch forgot to check the state" turning directly into a real financial
// loss.
func CanTradeLive(state types.StrategyState) bool {
	return state == types.StateLive
}

// CanTradePaper reports whether a state is allowed to run paper trading.
func CanTradePaper(state types.StrategyState) bool {
	return state == types.StatePaperTrading || state == types.StateLiveEligible
}

// NextStates returns every legal successor of a state, for the UI to show
// which actions are available.
func NextStates(state types.StrategyState) []types.StrategyState {
	out := make([]types.StrategyState, len(allowedTransitions[state]))
	copy(out, allowedTransitions[state])
	return out
}
