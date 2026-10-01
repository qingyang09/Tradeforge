// Package types defines the core data structures shared across modules.
//
// Convention: any field involving money, price, or quantity must use
// decimal.Decimal — float64 is forbidden. Dimensionless statistics like
// confidence and weight are allowed to use float64.
package types

import (
	"time"

	"github.com/shopspring/decimal"
)

// Direction represents the direction of a signal or order.
type Direction string

const (
	// DirectionLong is bullish.
	DirectionLong Direction = "LONG"
	// DirectionShort is bearish.
	DirectionShort Direction = "SHORT"
	// DirectionNeutral is neutral / no opinion. Also used when a module times out or degrades.
	DirectionNeutral Direction = "NEUTRAL"
)

// Valid reports whether the direction is one of the defined values.
func (d Direction) Valid() bool {
	switch d {
	case DirectionLong, DirectionShort, DirectionNeutral:
		return true
	default:
		return false
	}
}

// Opposite returns the opposite direction; the opposite of neutral is still neutral.
func (d Direction) Opposite() Direction {
	switch d {
	case DirectionLong:
		return DirectionShort
	case DirectionShort:
		return DirectionLong
	default:
		return DirectionNeutral
	}
}

// Signal is the standardized output of every signal module.
//
// Modules are decoupled from each other; this struct is the only contract
// between them — the combination engine only ever looks at Signal and
// doesn't care how a module computed it internally.
type Signal struct {
	// Module is the name of the module that produced this signal, matching SignalModule.Name().
	Module string `json:"module"`
	// Symbol is the trading pair, e.g. "BTCUSDT".
	Symbol string `json:"symbol"`
	// Direction is the signal's direction.
	Direction Direction `json:"direction"`
	// Confidence is in the range [0, 1]. A neutral signal's confidence should be 0.
	Confidence float64 `json:"confidence"`
	// Timestamp is the market time the signal corresponds to (not the module's compute time).
	Timestamp time.Time `json:"timestamp"`
	// Price is the reference price at signal time, usually the close of the last candle.
	Price decimal.Decimal `json:"price"`
	// Reason is a human-readable explanation for the trigger, used for
	// explainability. It must only describe "what happened" — no
	// investment-advice wording allowed.
	//
	// A Message (not a string): a module's Evaluate runs headlessly, with no
	// idea what language whoever eventually views this signal prefers, and
	// this value gets persisted (Kafka, and via Decision.Signals into
	// Postgres) for potentially much later display — see the plan's Class B
	// design for why that means "symbolic key + args, rendered at display
	// time" rather than a final sentence baked in now.
	Reason Message `json:"reason"`
	// Raw holds intermediate values from the module's computation (e.g. support/resistance
	// levels, volume multiple), used for audit and UI display.
	Raw map[string]any `json:"raw,omitempty"`
	// Degraded is true when this signal is a degraded fallback (a neutral signal filled
	// in after the module timed out or errored); it must be treated differently during
	// aggregation and audit.
	Degraded bool `json:"degraded,omitempty"`
	// Err records the reason for degradation; only set when Degraded is true.
	//
	// A Message, for the same reason as Reason above: this gets displayed
	// much later, by a viewer whose language DegradedSignal has no way to
	// know at construction time.
	Err Message `json:"err,omitempty"`
}

// NeutralSignal builds a neutral signal for a module to return when data is insufficient.
func NeutralSignal(module, symbol string, reason Message, ts time.Time) Signal {
	return Signal{
		Module:     module,
		Symbol:     symbol,
		Direction:  DirectionNeutral,
		Confidence: 0,
		Timestamp:  ts,
		Reason:     reason,
	}
}

// messageReasoner is implemented by error types that already carry a
// translatable types.Message (ComplianceError, TransitionError,
// *cvdorderflow.DataSourceError, and others following the same convention
// established across this codebase) -- DegradedSignal prefers this over the
// error's own Error() string so the degradation reason stays bilingual
// instead of freezing into whatever language the error happened to be built
// in.
type messageReasoner interface {
	Reason() Message
}

// DegradedSignal builds a degraded signal for the combination engine to fill in
// when a module times out or errors.
func DegradedSignal(module, symbol string, err error, ts time.Time) Signal {
	s := NeutralSignal(module, symbol,
		MsgF("types.signal.degraded", "module produced no signal; degraded to neutral"), ts)
	s.Degraded = true
	if err != nil {
		if mr, ok := err.(messageReasoner); ok {
			s.Err = mr.Reason()
		} else {
			// Legacy/boundary case: a plain error with no structured
			// reason (most commonly a stdlib context error, which is
			// already English, or a not-yet-converted module error) --
			// frozen in whatever language it was built in, the same
			// honest fallback used throughout this project for
			// not-yet-structured text.
			s.Err = Message{Literal: err.Error()}
		}
	}
	return s
}
