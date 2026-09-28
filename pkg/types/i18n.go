package types

import "encoding/json"

// Message is a translatable piece of text. Any package that produces text a
// viewer might eventually see, but that doesn't itself know the viewer's
// language (a signal module's Evaluate, the risk-control layer, the strategy
// state machine -- all headless, request-less code), should build one of
// these with Msg(...) instead of a final fmt.Sprintf'd sentence, and let it
// be rendered later, at display time, by whoever actually knows what
// language the viewer wants (internal/i18n.Render).
//
// Exactly one of two shapes is populated:
//   - Key/Args: the normal case, a symbolic catalog key plus named
//     interpolation arguments, resolved through internal/i18n's catalog.
//   - Literal: set only by UnmarshalJSON, when decoding a bare JSON string
//     written by code from before this type existed. That data has no key to
//     look up and can't be retroactively translated, so it's preserved and
//     rendered exactly as originally written, regardless of the viewer's
//     language -- an honest reflection of "this is what was recorded", not a
//     lossy guess. New code should never set Literal directly; use Msg.
type Message struct {
	Key     string         `json:"key,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	Literal string         `json:"-"`
}

// Msg builds a Message from a symbolic key and alternating name/value
// arguments, e.g. Msg("risk.daily_loss_exceeded", "symbol", "BTCUSDT",
// "limit", limit). Named rather than positional arguments on purpose: the
// English and Chinese renderings of the same key often need to reorder them
// relative to each other, and a name survives that reordering while a
// position doesn't.
func Msg(key string, args ...any) Message {
	m := Message{Key: key}
	if len(args) > 0 {
		m.Args = make(map[string]any, len(args)/2)
		for i := 0; i+1 < len(args); i += 2 {
			name, ok := args[i].(string)
			if !ok {
				continue
			}
			m.Args[name] = args[i+1]
		}
	}
	return m
}

// IsZero reports whether this Message carries no text at all -- neither a
// key nor a literal.
func (m Message) IsZero() bool { return m.Key == "" && m.Literal == "" }

// MarshalJSON writes the structured {"key": ..., "args": ...} form for a
// normal Message. A Literal-only Message (see UnmarshalJSON) marshals back
// out as the same bare JSON string it was read in as, so re-saving
// untouched legacy data doesn't silently upgrade its shape.
func (m Message) MarshalJSON() ([]byte, error) {
	if m.Key == "" && m.Literal != "" {
		return json.Marshal(m.Literal)
	}
	type alias Message
	return json.Marshal(alias(m))
}

// UnmarshalJSON accepts either shape a database row (or Kafka message) might
// contain: a bare JSON string written before Message existed, preserved
// verbatim into Literal, or the structured {"key": ..., "args": ...} object
// written by current code.
func (m *Message) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*m = Message{Literal: s}
		return nil
	}
	type alias Message
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*m = Message(a)
	return nil
}
