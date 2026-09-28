package types

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
	// EnglishFallback is a "{name}"-style template (same substitution syntax
	// as the internal/i18n catalog) rendered by RenderFallback when no
	// catalog is reachable. It exists for packages under pkg/ that can't
	// import internal/i18n themselves -- internal/i18n already imports
	// pkg/types to use this very type, so the reverse import would be a
	// cycle. pkg/types.ParamError is the motivating case: its Error() method
	// needs *some* readable rendering without depending on a catalog.
	// internal/i18n.Render still prefers its own registered catalog entry
	// for Key when one exists (so the actual bilingual wording only has to
	// live in one place); this is strictly a safety net, not a substitute
	// for registering the key.
	EnglishFallback string `json:"-"`
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

// MsgF is like Msg but also attaches an English fallback template (see
// Message.EnglishFallback) -- for call sites in pkg/ that need their own
// Error()-style method to produce readable text without a catalog.
func MsgF(key, englishFallback string, args ...any) Message {
	m := Msg(key, args...)
	m.EnglishFallback = englishFallback
	return m
}

// RenderFallback renders this Message using EnglishFallback (or Literal, if
// that's what's set) instead of a catalog lookup -- see EnglishFallback's
// doc comment for when this is the right tool. Returns the raw Key as a last
// resort if neither is set, same "never blank, never panic" philosophy as
// internal/i18n.Render.
func (m Message) RenderFallback() string {
	if m.Key == "" {
		return m.Literal
	}
	if m.EnglishFallback == "" {
		return m.Key
	}
	return Interpolate(m.EnglishFallback, m.Args)
}

// Interpolate substitutes each "{name}" placeholder in tmpl with
// fmt.Sprint(args["name"]). A placeholder with no matching arg is left
// as-is (visibly wrong and easy to spot, rather than silently dropped).
// Exported so internal/i18n's catalog-backed Render can reuse the exact
// same substitution logic instead of maintaining a second copy of it.
func Interpolate(tmpl string, args map[string]any) string {
	if len(args) == 0 || !strings.Contains(tmpl, "{") {
		return tmpl
	}
	var b strings.Builder
	b.Grow(len(tmpl))
	for i := 0; i < len(tmpl); {
		open := strings.IndexByte(tmpl[i:], '{')
		if open == -1 {
			b.WriteString(tmpl[i:])
			break
		}
		open += i
		b.WriteString(tmpl[i:open])
		closeIdx := strings.IndexByte(tmpl[open:], '}')
		if closeIdx == -1 {
			b.WriteString(tmpl[open:])
			break
		}
		closeIdx += open
		name := tmpl[open+1 : closeIdx]
		if v, ok := args[name]; ok {
			fmt.Fprint(&b, v)
		} else {
			b.WriteString(tmpl[open : closeIdx+1])
		}
		i = closeIdx + 1
	}
	return b.String()
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
