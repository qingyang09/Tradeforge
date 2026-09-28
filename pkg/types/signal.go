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
	// Reason is a human-readable explanation for the trigger, used for explainability.
	// It must only describe "what happened" — no investment-advice wording allowed.
	Reason string `json:"reason"`
	// Raw holds intermediate values from the module's computation (e.g. support/resistance
	// levels, volume multiple), used for audit and UI display.
	Raw map[string]any `json:"raw,omitempty"`
	// Degraded is true when this signal is a degraded fallback (a neutral signal filled
	// in after the module timed out or errored); it must be treated differently during
	// aggregation and audit.
	Degraded bool `json:"degraded,omitempty"`
	// Err records the reason for degradation; only set when Degraded is true.
	Err string `json:"err,omitempty"`
}

// NeutralSignal builds a neutral signal for a module to return when data is insufficient.
func NeutralSignal(module, symbol, reason string, ts time.Time) Signal {
	return Signal{
		Module:     module,
		Symbol:     symbol,
		Direction:  DirectionNeutral,
		Confidence: 0,
		Timestamp:  ts,
		Reason:     reason,
	}
}

// DegradedSignal builds a degraded signal for the combination engine to fill in
// when a module times out or errors.
func DegradedSignal(module, symbol string, err error, ts time.Time) Signal {
	s := NeutralSignal(module, symbol, "module produced no signal; degraded to neutral", ts)
	s.Degraded = true
	if err != nil {
		s.Err = err.Error()
	}
	return s
}
