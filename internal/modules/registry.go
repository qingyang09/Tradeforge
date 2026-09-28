package modules

import (
	"tradeforge/internal/modules/cvdorderflow"
	"tradeforge/internal/modules/fakeout"
	"tradeforge/internal/modules/macdrsi"
	"tradeforge/internal/modules/newssentiment"
	"tradeforge/internal/modules/poc"
	"tradeforge/internal/modules/supportresistance"
	"tradeforge/internal/modules/volumebreakout"
)

// Compile-time check that each implementation actually satisfies SignalModule.
// These assertions live here rather than in the implementation packages so the
// implementation packages can depend only on pkg/types, keeping the dependency
// direction one-way.
var (
	_ SignalModule = (*supportresistance.Module)(nil)
	_ SignalModule = (*volumebreakout.Module)(nil)
	_ SignalModule = (*cvdorderflow.Module)(nil)
	_ SignalModule = (*macdrsi.Module)(nil)
	_ SignalModule = (*newssentiment.Module)(nil)
	_ SignalModule = (*fakeout.Module)(nil)
	_ SignalModule = (*poc.Module)(nil)
)

// NewDefaultRegistry returns a registry preloaded with all built-in modules.
//
// This is the single authoritative definition of "which modules the platform
// ships": the Agent translation layer's selectable module list, the
// composition engine's module lookup, and the UI's module list must all pull
// from here — none of them may maintain their own copy.
func NewDefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(supportresistance.New())
	r.Register(volumebreakout.New())
	r.Register(cvdorderflow.NewDefault())
	r.Register(macdrsi.New())
	r.Register(newssentiment.NewDefault())
	r.Register(fakeout.New())
	r.Register(poc.New())
	return r
}
