package types

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ParamType 是模块参数的类型标签。
type ParamType string

// 支持的参数类型。刻意保持极简：模块参数应当是可被 JSON Schema 直接表达的标量。
const (
	ParamInt    ParamType = "int"
	ParamFloat  ParamType = "float"
	ParamString ParamType = "string"
	ParamBool   ParamType = "bool"
)

// ParamSpec 描述模块的一个参数：名字、类型、默认值、取值范围。
//
// 它同时是三处的唯一事实来源：
//   - 模块自身的入参校验
//   - Agent 翻译层生成 JSON Schema 时的取值约束
//   - 界面上的参数编辑控件
type ParamSpec struct {
	Name        string    `json:"name"`
	Type        ParamType `json:"type"`
	Description string    `json:"description"`
	// Default 是缺省值。Required 为 false 时必须提供。
	Default any `json:"default,omitempty"`
	// Required 为 true 时调用方必须显式提供该参数。
	Required bool `json:"required,omitempty"`
	// Min/Max 是数值型参数的闭区间边界，仅当对应指针非 nil 时生效。
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`
	// Enum 是字符串型参数的允许取值集合，为空表示不限制。
	Enum []string `json:"enum,omitempty"`
}

// F 是构造 *float64 的语法糖，用于填写 Min/Max。
func F(v float64) *float64 { return &v }

// ParamError 描述一次参数校验失败。
//
// 它带足了上下文，因此 Agent 翻译层可以把它原样转述给用户
// （"你说的回看窗口 5000 超出了 20~500 的允许范围"），
// 而不需要再让 LLM 编造解释。
type ParamError struct {
	Module string
	Param  string
	Reason string
	// Given 是用户/LLM 给出的原始值。
	Given any
	// Allowed 是人类可读的允许范围描述。
	Allowed string
}

func (e *ParamError) Error() string {
	var b strings.Builder
	if e.Module != "" {
		fmt.Fprintf(&b, "模块 %s 的", e.Module)
	}
	fmt.Fprintf(&b, "参数 %s 非法：%s", e.Param, e.Reason)
	if e.Allowed != "" {
		fmt.Fprintf(&b, "（允许范围：%s）", e.Allowed)
	}
	return b.String()
}

// AllowedDesc 返回该参数允许取值的人类可读描述。
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
		return "字符串"
	case ParamBool:
		return "true / false"
	}
	return string(p.Type)
}

// Coerce 把一个来自 JSON 的原始值转换成该参数的规范化 Go 类型并做范围校验。
//
// JSON 解码后数字一律是 float64，因此 int 型参数需要检查它确实是整数，
// 而不是静悄悄地把 20.7 截断成 20——那属于"尽力修复"，是平台明确禁止的行为。
func (p ParamSpec) Coerce(module string, v any) (any, error) {
	fail := func(reason string) error {
		return &ParamError{Module: module, Param: p.Name, Reason: reason, Given: v, Allowed: p.AllowedDesc()}
	}

	switch p.Type {
	case ParamInt:
		f, ok := toFloat(v)
		if !ok {
			return nil, fail(fmt.Sprintf("期望整数，得到 %T", v))
		}
		if f != math.Trunc(f) {
			return nil, fail(fmt.Sprintf("期望整数，得到 %v", v))
		}
		if err := p.checkRange(module, f); err != nil {
			return nil, err
		}
		return int(f), nil

	case ParamFloat:
		f, ok := toFloat(v)
		if !ok {
			return nil, fail(fmt.Sprintf("期望数字，得到 %T", v))
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fail("数值必须是有限数")
		}
		if err := p.checkRange(module, f); err != nil {
			return nil, err
		}
		return f, nil

	case ParamString:
		s, ok := v.(string)
		if !ok {
			return nil, fail(fmt.Sprintf("期望字符串，得到 %T", v))
		}
		if len(p.Enum) > 0 {
			for _, e := range p.Enum {
				if e == s {
					return s, nil
				}
			}
			return nil, fail(fmt.Sprintf("%q 不在允许的取值集合内", s))
		}
		return s, nil

	case ParamBool:
		b, ok := v.(bool)
		if !ok {
			return nil, fail(fmt.Sprintf("期望布尔值，得到 %T", v))
		}
		return b, nil
	}

	return nil, fail(fmt.Sprintf("未知的参数类型 %q", p.Type))
}

func (p ParamSpec) checkRange(module string, f float64) error {
	if p.Min != nil && f < *p.Min {
		return &ParamError{Module: module, Param: p.Name,
			Reason: fmt.Sprintf("%g 小于允许的最小值 %g", f, *p.Min), Given: f, Allowed: p.AllowedDesc()}
	}
	if p.Max != nil && f > *p.Max {
		return &ParamError{Module: module, Param: p.Name,
			Reason: fmt.Sprintf("%g 大于允许的最大值 %g", f, *p.Max), Given: f, Allowed: p.AllowedDesc()}
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

// ResolveParams 用一组 ParamSpec 校验并规范化实参：
// 填充缺省值、拒绝未知参数、逐项做类型与范围校验。
//
// 返回的 map 中每个值都已是规范化的 Go 类型（int / float64 / string / bool），
// 模块内部可以安全断言。任何一项失败都直接返回错误，不做部分修复。
func ResolveParams(module string, specs []ParamSpec, given map[string]any) (map[string]any, error) {
	byName := make(map[string]ParamSpec, len(specs))
	for _, s := range specs {
		byName[s.Name] = s
	}

	// 先拒绝未知参数：LLM 幻觉出的参数名必须显式暴露，而不是被静默忽略。
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
			Reason: "该模块不存在此参数",
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
					Reason: "缺少必填参数", Allowed: s.AllowedDesc()}
			}
			if s.Default == nil {
				return nil, &ParamError{Module: module, Param: s.Name,
					Reason: "参数未提供且没有默认值", Allowed: s.AllowedDesc()}
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

// 以下取值助手假设 params 已经过 ResolveParams 规范化。
// 若断言失败说明调用方跳过了校验，属于编程错误，直接 panic 比返回零值更安全。

// MustInt 取出一个 int 参数。
func MustInt(params map[string]any, name string) int {
	v, ok := params[name].(int)
	if !ok {
		panic(fmt.Sprintf("参数 %q 不是 int（是否忘了调用 ResolveParams？）", name))
	}
	return v
}

// MustFloat 取出一个 float64 参数。
func MustFloat(params map[string]any, name string) float64 {
	v, ok := params[name].(float64)
	if !ok {
		panic(fmt.Sprintf("参数 %q 不是 float64（是否忘了调用 ResolveParams？）", name))
	}
	return v
}

// MustString 取出一个 string 参数。
func MustString(params map[string]any, name string) string {
	v, ok := params[name].(string)
	if !ok {
		panic(fmt.Sprintf("参数 %q 不是 string（是否忘了调用 ResolveParams？）", name))
	}
	return v
}

// MustBool 取出一个 bool 参数。
func MustBool(params map[string]any, name string) bool {
	v, ok := params[name].(bool)
	if !ok {
		panic(fmt.Sprintf("参数 %q 不是 bool（是否忘了调用 ResolveParams？）", name))
	}
	return v
}
