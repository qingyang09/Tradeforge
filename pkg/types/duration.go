package types

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration wraps time.Duration so it serializes to JSON as a human-readable
// string ("24h", or "168h" for the equivalent of "7d").
//
// The standard library's time.Duration marshals to an integer of nanoseconds,
// which is unreadable in a strategy config and makes it easy for an LLM to
// generate a value that's off by orders of magnitude.
type Duration time.Duration

// D is sugar for constructing a Duration.
func D(d time.Duration) Duration { return Duration(d) }

// Std returns the underlying time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String implements fmt.Stringer.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON writes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON accepts either the string form ("90m") or a raw nanosecond integer.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch x := v.(type) {
	case string:
		parsed, err := time.ParseDuration(x)
		if err != nil {
			return fmt.Errorf("could not parse duration %q: %w", x, err)
		}
		*d = Duration(parsed)
		return nil
	case float64:
		*d = Duration(time.Duration(x))
		return nil
	case nil:
		*d = 0
		return nil
	default:
		return fmt.Errorf("duration field expected a string or number, got %T", v)
	}
}
