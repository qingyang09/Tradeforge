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

// 编译期确认各实现确实满足 SignalModule 接口。
// 断言放在本包而不是实现包里，是为了让实现包只依赖 pkg/types，保持依赖单向。
var (
	_ SignalModule = (*supportresistance.Module)(nil)
	_ SignalModule = (*volumebreakout.Module)(nil)
	_ SignalModule = (*cvdorderflow.Module)(nil)
	_ SignalModule = (*macdrsi.Module)(nil)
	_ SignalModule = (*newssentiment.Module)(nil)
	_ SignalModule = (*fakeout.Module)(nil)
	_ SignalModule = (*poc.Module)(nil)
)

// NewDefaultRegistry 返回装好全部内置模块的注册表。
//
// 这是"平台内置了哪些模块"的唯一权威定义：Agent 翻译层的可选模块清单、
// 组合引擎的模块查找、界面的模块列表，都必须从这里取，不允许各自维护一份。
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
