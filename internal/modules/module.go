// Package modules 定义信号模块的统一接口与注册表。
//
// 设计约束：各模块实现包只依赖 pkg/types，互不引用，也不引用本包。
// 本包反过来引用它们并组装默认注册表，依赖方向单向、无环。
package modules

import (
	"context"
	"fmt"
	"sort"

	"tradeforge/pkg/types"
)

// SignalModule 是所有交易信号模块必须实现的接口。
//
// 模块之间互不耦合：唯一的对外契约就是"吃 MarketData + 参数，吐 Signal"。
// 组合引擎只依赖这个接口，不关心模块内部算法。
type SignalModule interface {
	// Name 返回模块的唯一标识，如 "support_resistance"。
	// 这个名字会出现在策略配置里，是 Agent 翻译层唯一允许引用的模块标识。
	Name() string

	// Description 返回一句中立的功能描述，用于展示与 Agent 的模块清单提示词。
	// 只描述"这个模块算什么"，不得包含任何适用场景推荐或效果承诺。
	Description() string

	// RequiredParams 返回模块的参数规格：名称、类型、默认值、取值范围。
	// 它是参数校验与 JSON Schema 生成的唯一事实来源。
	RequiredParams() []types.ParamSpec

	// Evaluate 基于行情数据与参数计算信号。
	//
	// 实现约定：
	//   - 数据不足时返回中性信号 + nil error，不要报错（这是正常情况）
	//   - 参数非法时返回错误（这是调用方的问题，必须暴露）
	//   - 必须尊重 ctx 的取消与超时
	//   - 不得修改传入的 MarketData
	Evaluate(ctx context.Context, md types.MarketData, params map[string]any) (types.Signal, error)
}

// Registry 是模块注册表，把模块名映射到实现。
//
// 它是"平台内置了哪些模块"的唯一权威来源：Agent 翻译层从这里取可选模块清单，
// 组合引擎从这里取实现，两者看到的必须是同一份。
type Registry struct {
	byName map[string]SignalModule
}

// NewRegistry 返回一个空注册表。
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]SignalModule)}
}

// Register 登记一个模块。重复注册同名模块会 panic：
// 这只可能是启动期的编程错误，静默覆盖会让线上跑的模块与预期不符。
func (r *Registry) Register(m SignalModule) {
	name := m.Name()
	if name == "" {
		panic("modules: 模块名不能为空")
	}
	if _, dup := r.byName[name]; dup {
		panic(fmt.Sprintf("modules: 模块 %q 重复注册", name))
	}
	r.byName[name] = m
}

// Get 按名字取模块。未注册时返回 error 而不是零值，
// 让"Agent 幻觉出一个不存在的模块"这类问题在配置校验期就炸出来。
func (r *Registry) Get(name string) (SignalModule, error) {
	m, ok := r.byName[name]
	if !ok {
		return nil, &UnknownModuleError{Name: name, Available: r.Names()}
	}
	return m, nil
}

// Has 报告模块是否已注册。
func (r *Registry) Has(name string) bool {
	_, ok := r.byName[name]
	return ok
}

// Names 返回全部已注册模块名，按字典序排列（保证输出稳定）。
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// All 返回全部已注册模块，按名字排序。
func (r *Registry) All() []SignalModule {
	out := make([]SignalModule, 0, len(r.byName))
	for _, n := range r.Names() {
		out = append(out, r.byName[n])
	}
	return out
}

// UnknownModuleError 表示引用了未注册的模块。
type UnknownModuleError struct {
	Name      string
	Available []string
}

func (e *UnknownModuleError) Error() string {
	return fmt.Sprintf("未知模块 %q；平台当前提供的模块为：%v", e.Name, e.Available)
}

// ResolveParams 用模块自己的参数规格校验并规范化一组实参。
// 这是模块实现在 Evaluate 开头应当调用的第一件事。
func ResolveParams(m SignalModule, params map[string]any) (map[string]any, error) {
	return types.ResolveParams(m.Name(), m.RequiredParams(), params)
}
