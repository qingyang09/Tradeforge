package types

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ParamType is the type tag for a module parameter.
type ParamType string

// Supported parameter types. Deliberately kept minimal: module parameters should
// be scalars that a JSON Schema can express directly.
const (
	ParamInt    ParamType = "int"
	ParamFloat  ParamType = "float"
	ParamString ParamType = "string"
	ParamBool   ParamType = "bool"
)

// ParamSpec describes one parameter of a module: name, type, default, and allowed range.
//
// It is the single source of truth for three things at once:
//   - the module's own input validation
//   - the value constraints the Agent translation layer bakes into its generated JSON Schema
//   - the parameter-editing widget in the UI
type ParamSpec struct {
	Name string    `json:"name"`
	Type ParamType `json:"type"`
	// Description feeds two different consumers that each need it in a
	// possibly-different language: the webui's parameter-editing widget, and
	// the JSON-Schema hint text sent to steer the LLM's own output. Both
	// resolve it through internal/i18n.Render at the point they actually use
	// it -- see internal/agent/schema.go and internal/webui/handlers_api.go.
	Description Message `json:"description"`
	// Default is the fallback value, required whenever Required is false.
	Default any `json:"default,omitempty"`
	// Required, when true, means the caller must supply this parameter explicitly.
	Required bool `json:"required,omitempty"`
	// Min/Max are the closed-interval bounds for numeric parameters, effective only
	// when the corresponding pointer is non-nil.
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// Enum is the allowed set of values for a string parameter; empty means unrestricted.
	Enum []string `json:"enum,omitempty"`
}

// F is sugar for constructing a *float64, used to fill in Min/Max.
func F(v float64) *float64 { return &v }

// ParamError describes a single parameter validation failure.
//
// It carries enough context that the Agent translation layer can relay it to the
// user verbatim ("the lookback window of 5000 you specified is outside the
// allowed range of 20~500") instead of having the LLM make up an explanation.
//
// Reason is a Message (not a string): pkg/types can't import internal/i18n's
// catalog (that package already imports pkg/types, so the reverse would be a
// cycle), so Reason is always built with MsgF -- a symbolic key plus an
// English fallback template baked in at the call site. internal/i18n.Render
// still prefers a registered catalog entry for the key when one exists (see
// internal/i18n/catalog_types.go); the fallback just guarantees Error()
// keeps working with readable English even without depending on the catalog.
type ParamError struct {
	Module string
	Param  string
	Reason Message
	// Given is the raw value supplied by the user/LLM.
	Given any
	// Allowed is a human-readable description of the allowed range.
	Allowed string
}

func (e *ParamError) Error() string {
	var b strings.Builder
	if e.Module != "" {
		fmt.Fprintf(&b, "module %s: ", e.Module)
	}
	fmt.Fprintf(&b, "parameter %s is invalid: %s", e.Param, e.Reason.RenderFallback())
	if e.Allowed != "" {
		fmt.Fprintf(&b, " (allowed: %s)", e.Allowed)
	}
	return b.String()
}

// AllowedDesc returns a human-readable description of this parameter's allowed values.
func (p ParamSpec) AllowedDesc() string {
	switch p.Type {
	case ParamInt, ParamFloat:
		switch {
		case p.Min != nil && p.Max != nil:
			return fmt.Sprintf("%g ~ %g", *p.Min, *p.Max)
		case p.Min != nil:
			return fmt.Sprintf(">= %g", *p.Min)
		case p.Max != nil:
			return fmt.Sprintf("<= %g", *p.Max)
		}
		return string(p.Type)
	case ParamString:
		if len(p.Enum) > 0 {
			return strings.Join(p.Enum, " / ")
		}
		return "string"
	case ParamBool:
		return "true / false"
	}
	return string(p.Type)
}

// Coerce converts a raw JSON-decoded value into this parameter's canonical Go
// type and range-checks it.
//
// JSON decoding always yields float64 for numbers, so an int parameter must be
// checked to actually be an integer rather than silently truncating 20.7 to
// 20 — that would be "best-effort repair", which the platform explicitly forbids.
func (p ParamSpec) Coerce(module string, v any) (any, error) {
	fail := func(key, fallback string, args ...any) error {
		return &ParamError{Module: module, Param: p.Name, Reason: MsgF(key, fallback, args...), Given: v, Allowed: p.AllowedDesc()}
	}

	switch p.Type {
	case ParamInt:
		f, ok := toFloat(v)
		if !ok {
			return nil, fail("types.param.expected_int_type", "expected an integer, got {type}", "type", fmt.Sprintf("%T", v))
		}
		if f != math.Trunc(f) {
			return nil, fail("types.param.expected_int_value", "expected an integer, got {value}", "value", fmt.Sprintf("%v", v))
		}
		if err := p.checkRange(module, f); err != nil {
			return nil, err
		}
		return int(f), nil

	case ParamFloat:
		f, ok := toFloat(v)
		if !ok {
			return nil, fail("types.param.expected_number_type", "expected a number, got {type}", "type", fmt.Sprintf("%T", v))
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fail("types.param.must_be_finite", "value must be finite")
		}
		if err := p.checkRange(module, f); err != nil {
			return nil, err
		}
		return f, nil

	case ParamString:
		s, ok := v.(string)
		if !ok {
			return nil, fail("types.param.expected_string_type", "expected a string, got {type}", "type", fmt.Sprintf("%T", v))
		}
		if len(p.Enum) > 0 {
			for _, e := range p.Enum {
				if e == s {
					return s, nil
				}
			}
			return nil, fail("types.param.not_in_enum", "{value} is not in the allowed set of values", "value", fmt.Sprintf("%q", s))
		}
		return s, nil

	case ParamBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fail("types.param.expected_bool_type", "expected a bool, got {type}", "type", fmt.Sprintf("%T", v))
		}
		return b, nil
	}

	return nil, fail("types.param.unknown_type", "unknown parameter type {type}", "type", fmt.Sprintf("%q", p.Type))
}

func (p ParamSpec) checkRange(module string, f float64) error {
	if p.Min != nil && f < *p.Min {
		return &ParamError{Module: module, Param: p.Name,
			Reason: MsgF("types.param.below_minimum", "{value} is below the allowed minimum of {min}",
				"value", fmt.Sprintf("%g", f), "min", fmt.Sprintf("%g", *p.Min)),
			Given: f, Allowed: p.AllowedDesc()}
	}
	if p.Max != nil && f > *p.Max {
		return &ParamError{Module: module, Param: p.Name,
			Reason: MsgF("types.param.above_maximum", "{value} is above the allowed maximum of {max}",
				"value", fmt.Sprintf("%g", f), "max", fmt.Sprintf("%g", *p.Max)),
			Given: f, Allowed: p.AllowedDesc()}
	}
	return nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// ResolveParams validates and normalizes actual arguments against a set of
// ParamSpecs: fills in defaults, rejects unknown parameters, and type/range
// checks each one in turn.
//
// Every value in the returned map is already a canonical Go type
// (int / float64 / string / bool), so modules can safely type-assert it.
// Any single failure returns an error immediately — no partial repair.
func ResolveParams(module string, specs []ParamSpec, given map[string]any) (map[string]any, error) {
	byName := make(map[string]ParamSpec, len(specs))
	for _, s := range specs {
		byName[s.Name] = s
	}

	// Reject unknown parameters first: an LLM-hallucinated parameter name must be
	// surfaced explicitly, not silently ignored.
	unknown := make([]string, 0)
	for k := range given {
		if _, ok := byName[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &ParamError{
			Module: module,
			Param:  strings.Join(unknown, ", "),
			Reason: MsgF("types.param.unknown_param", "this module has no such parameter"),
			Allowed: strings.Join(func() []string {
				names := make([]string, 0, len(specs))
				for _, s := range specs {
					names = append(names, s.Name)
				}
				sort.Strings(names)
				return names
			}(), ", "),
		}
	}

	out := make(map[string]any, len(specs))
	for _, s := range specs {
		raw, ok := given[s.Name]
		if !ok || raw == nil {
			if s.Required {
				return nil, &ParamError{Module: module, Param: s.Name,
					Reason: MsgF("types.param.required_missing", "missing required parameter"), Allowed: s.AllowedDesc()}
			}
			if s.Default == nil {
				return nil, &ParamError{Module: module, Param: s.Name,
					Reason: MsgF("types.param.no_default", "parameter not provided and has no default"), Allowed: s.AllowedDesc()}
			}
			raw = s.Default
		}
		v, err := s.Coerce(module, raw)
		if err != nil {
			return nil, err
		}
		out[s.Name] = v
	}
	return out, nil
}

// The accessor helpers below assume params has already been normalized by
// ResolveParams. A failed assertion means the caller skipped validation —
// that's a programming error, and panicking is safer than returning a zero value.

// MustInt extracts an int parameter.
func MustInt(params map[string]any, name string) int {
	v, ok := params[name].(int)
	if !ok {
		panic(fmt.Sprintf("parameter %q is not an int (did you forget to call ResolveParams?)", name))
	}
	return v
}

// MustFloat extracts a float64 parameter.
func MustFloat(params map[string]any, name string) float64 {
	v, ok := params[name].(float64)
	if !ok {
		panic(fmt.Sprintf("parameter %q is not a float64 (did you forget to call ResolveParams?)", name))
	}
	return v
}

// MustString extracts a string parameter.
func MustString(params map[string]any, name string) string {
	v, ok := params[name].(string)
	if !ok {
		panic(fmt.Sprintf("parameter %q is not a string (did you forget to call ResolveParams?)", name))
	}
	return v
}

// MustBool extracts a bool parameter.
func MustBool(params map[string]any, name string) bool {
	v, ok := params[name].(bool)
	if !ok {
		panic(fmt.Sprintf("parameter %q is not a bool (did you forget to call ResolveParams?)", name))
	}
	return v
}
