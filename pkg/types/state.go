package types

// StrategyState is a strategy's state within the verification lifecycle.
//
// The legal transition path is fixed:
//
//	DRAFT → BACKTESTED → PAPER_TRADING → LIVE_ELIGIBLE → LIVE
//
// The state machine itself (gate checks and audit logging) is implemented in
// internal/engine/statemachine.go. Only the constants live here, so lower-level
// packages can reference state names without depending on a higher-level package.
type StrategyState string

const (
	// StateDraft: a config just translated by the Agent, not yet backtested.
	StateDraft StrategyState = "DRAFT"
	// StateBacktested: backtest complete and out-of-sample results clear the gate.
	StateBacktested StrategyState = "BACKTESTED"
	// StatePaperTrading: running simulated trades against live market data, no real orders.
	StatePaperTrading StrategyState = "PAPER_TRADING"
	// StateLiveEligible: eligible for live trading — paper trading duration and trade
	// count both clear the gate, awaiting the user's manual unlock.
	StateLiveEligible StrategyState = "LIVE_ELIGIBLE"
	// StateLive: live trading, placing real orders.
	StateLive StrategyState = "LIVE"
	// StateSuspended: suspended — risk control was triggered or the user stopped it manually.
	StateSuspended StrategyState = "SUSPENDED"
)

// AllStates lists every state in pipeline order.
var AllStates = []StrategyState{
	StateDraft, StateBacktested, StatePaperTrading, StateLiveEligible, StateLive, StateSuspended,
}

// Valid reports whether the state is one of the defined values.
func (s StrategyState) Valid() bool {
	for _, v := range AllStates {
		if v == s {
			return true
		}
	}
	return false
}
