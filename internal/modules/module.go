// Package modules defines the common interface for signal modules and their registry.
//
// Design constraint: each module implementation package depends only on pkg/types,
// never on each other or on this package. This package depends on them in turn and
// assembles the default registry, keeping the dependency direction one-way and acyclic.
package modules

import (
	"context"
	"fmt"
	"sort"

	"tradeforge/internal/i18n"
	"tradeforge/pkg/types"
)

// SignalModule is the interface every trading signal module must implement.
//
// Modules are decoupled from one another: the only external contract is "take
// MarketData + params, emit a Signal". The composition engine depends only on
// this interface and knows nothing about a module's internal algorithm.
type SignalModule interface {
	// Name returns the module's unique identifier, e.g. "support_resistance".
	// This name appears in strategy configs and is the only module identifier
	// the Agent translation layer is allowed to reference.
	Name() string

	// Description returns a neutral one-line description of what the module does,
	// used for display and in the Agent's module-list prompt.
	// It must describe only "what this module computes" — no recommendations
	// about when to use it and no performance claims.
	Description() types.Message

	// RequiredParams returns the module's parameter spec: name, type, default
	// value, and valid range. It is the single source of truth for both
	// parameter validation and JSON Schema generation.
	RequiredParams() []types.ParamSpec

	// Evaluate computes a signal from market data and parameters.
	//
	// Implementation contract:
	//   - Insufficient data returns a neutral signal + nil error, not an error
	//     (this is a normal condition, not a failure)
	//   - Invalid parameters return an error (this is the caller's problem and
	//     must be surfaced)
	//   - Must respect ctx cancellation and deadlines
	//   - Must not mutate the MarketData passed in
	Evaluate(ctx context.Context, md types.MarketData, params map[string]any) (types.Signal, error)
}

// Registry is the module registry, mapping module names to implementations.
//
// It is the single authoritative source for "which modules the platform ships":
// the Agent translation layer pulls its list of selectable modules from here,
// and the composition engine pulls implementations from here — both must see
// the same set.
type Registry struct {
	byName map[string]SignalModule
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]SignalModule)}
}

// Register adds a module. Registering the same name twice panics: this can
// only be a startup-time programming error, and silently overwriting the
// entry would make the module running in production diverge from what was
// intended.
func (r *Registry) Register(m SignalModule) {
	name := m.Name()
	if name == "" {
		panic("modules: module name must not be empty")
	}
	if _, dup := r.byName[name]; dup {
		panic(fmt.Sprintf("modules: module %q registered twice", name))
	}
	r.byName[name] = m
}

// Get looks up a module by name. Returns an error rather than a zero value
// when unregistered, so an "Agent hallucinated a module that doesn't exist"
// bug surfaces during config validation instead of later.
func (r *Registry) Get(name string) (SignalModule, error) {
	m, ok := r.byName[name]
	if !ok {
		return nil, &UnknownModuleError{Name: name, Available: r.Names()}
	}
	return m, nil
}

// Has reports whether a module is registered.
func (r *Registry) Has(name string) bool {
	_, ok := r.byName[name]
	return ok
}

// Names returns all registered module names, lexically sorted (so the output is stable).
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// All returns every registered module, sorted by name.
func (r *Registry) All() []SignalModule {
	out := make([]SignalModule, 0, len(r.byName))
	for _, n := range r.Names() {
		out = append(out, r.byName[n])
	}
	return out
}

// UnknownModuleError indicates a reference to a module that isn't registered.
type UnknownModuleError struct {
	Name      string
	Available []string
}

func (e *UnknownModuleError) Error() string {
	return i18n.Render(i18n.LangEN, e.Reason())
}

// Reason returns this error as a translatable types.Message.
func (e *UnknownModuleError) Reason() types.Message {
	return types.Msg("modules.unknown_module", "name", e.Name, "available", fmt.Sprint(e.Available))
}

// ResolveParams validates and normalizes a set of arguments against a module's
// own parameter spec. This is the first thing a module implementation should
// call at the top of Evaluate.
func ResolveParams(m SignalModule, params map[string]any) (map[string]any, error) {
	return types.ResolveParams(m.Name(), m.RequiredParams(), params)
}
