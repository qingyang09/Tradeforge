package agent

import (
	"fmt"

	"tradeforge/internal/modules"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// Schema 是传给 LLM 的 JSON Schema（用 map 表示，便于直接序列化）。
type Schema map[string]any

// BuildSchema 依据模块注册表动态生成 Agent 输出的 JSON Schema。
//
// 关键点：模块名的枚举、每个模块的参数名与取值范围，全部从注册表现场读取，
// 而不是手写一份。这样"平台有哪些模块"只有一个事实来源，
// 新增模块时 schema 自动跟上，LLM 也就没有发明模块的空间。
func BuildSchema(reg *modules.Registry) Schema {
	return Schema{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"outcome", "restatement", "config", "questions"},
		"properties": map[string]any{
			"outcome": map[string]any{
				"type": "string",
				"enum": []string{string(OutcomeConfig), string(OutcomeClarify)},
				"description": "config 表示用户的描述足够明确、已翻译成配置；" +
					"clarification_needed 表示描述存在歧义，必须先向用户提问。",
			},
			"restatement": map[string]any{
				"type": "string",
				"description": "用大白话复述你对用户规则的理解，供用户确认。" +
					"只描述规则本身，不得包含任何对策略优劣的评价或投资建议。" +
					"outcome 为 clarification_needed 时，这里写你目前能确定的部分。",
			},
			"questions": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "需要用户澄清的问题列表。outcome 为 config 时必须为空数组；" +
					"为 clarification_needed 时必须至少有一个问题。",
			},
			"config": map[string]any{
				"description": "翻译出的策略配置。outcome 为 clarification_needed 时为 null。",
				"anyOf": []any{
					strategyConfigSchema(reg),
					map[string]any{"type": "null"},
				},
			},
		},
	}
}

func strategyConfigSchema(reg *modules.Registry) map[string]any {
	timeframes := make([]string, 0, len(types.SupportedTimeframes))
	for _, tf := range types.SupportedTimeframes {
		timeframes = append(timeframes, string(tf))
	}

	moduleBranches := make([]any, 0, len(reg.All()))
	for _, m := range reg.All() {
		moduleBranches = append(moduleBranches, moduleConfigSchema(m, timeframes))
	}

	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"name", "symbol", "timeframe", "modules", "combine", "risk"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "策略名称，用简短的中文概括这条规则。",
			},
			"symbol": map[string]any{
				"type":        "string",
				"pattern":     "^[A-Z0-9]{4,20}$",
				"description": "交易标的，交易所格式的大写符号，如 BTCUSDT、ETHUSDT。",
			},
			"timeframe": map[string]any{
				"type": "string",
				"enum": timeframes,
				"description": "策略的触发周期：决策在这个周期收盘时计算一次，必须是所有模块" +
					"所用周期中最快的那个（如果某些模块通过各自的 timeframe 字段声明了更慢的周期）。",
			},
			"combine": map[string]any{
				"type": "string",
				"enum": []string{string(types.CombineAll), string(types.CombineWeighted)},
				"description": "模块信号的组合方式。ALL：所有模块同向才触发；" +
					"WEIGHTED：按权重加权，超过阈值才触发。",
			},
			"threshold": map[string]any{
				"type": "number", "minimum": 0, "maximum": 1,
				"description": "WEIGHTED 组合的触发阈值，取值 (0, 1]。ALL 组合时省略。",
			},
			"modules": map[string]any{
				"type":     "array",
				"minItems": 1,
				"maxItems": strategy.MaxModulesPerStrategy,
				"items":    map[string]any{"oneOf": moduleBranches},
				"description": "参与该策略的模块。只能从下面列出的模块中选择，" +
					"每个模块最多出现一次。",
			},
			"risk": riskSchema(),
		},
	}
}

// moduleConfigSchema 为单个模块生成一个 schema 分支：
// module 字段被 const 锁死，params 的属性表由该模块的 RequiredParams 现场生成。
//
// 用 oneOf + const 而不是一个笼统的 "params: object"，是为了让
// "cvd_orderflow 上写了 lookback 参数"这类错误在 schema 层就被挡住，
// 而不是等到运行期才报错。
func moduleConfigSchema(m modules.SignalModule, timeframes []string) map[string]any {
	props := map[string]any{}
	for _, spec := range m.RequiredParams() {
		props[spec.Name] = paramSchema(spec)
	}

	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"module", "params"},
		"description":          m.Description(),
		"properties": map[string]any{
			"module": map[string]any{"const": m.Name()},
			"weight": map[string]any{
				"type": "number", "exclusiveMinimum": 0, "maximum": 1,
				"description": "该模块在 WEIGHTED 组合中的权重，取值 (0, 1]。ALL 组合时省略。",
			},
			"timeframe": map[string]any{
				"type": "string", "enum": timeframes,
				"description": "该模块使用的周期；不填则跟随策略顶层 timeframe（触发周期）。" +
					"填了就不能比顶层 timeframe 更快——比如用户描述'1 小时判断背景、" +
					"15 分钟判断入场'，背景模块填 \"1h\"，顶层 timeframe 填 \"15m\"。",
			},
			"params": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           props,
				"description": "模块参数。用户没有明确指定的参数请直接省略，" +
					"系统会填入默认值；不要自行编造数值。",
			},
		},
	}
}

func paramSchema(spec types.ParamSpec) map[string]any {
	out := map[string]any{"description": spec.Description}

	switch spec.Type {
	case types.ParamInt:
		out["type"] = "integer"
	case types.ParamFloat:
		out["type"] = "number"
	case types.ParamString:
		out["type"] = "string"
		if len(spec.Enum) > 0 {
			out["enum"] = spec.Enum
		}
	case types.ParamBool:
		out["type"] = "boolean"
	}

	if spec.Min != nil {
		out["minimum"] = *spec.Min
	}
	if spec.Max != nil {
		out["maximum"] = *spec.Max
	}
	if spec.Default != nil {
		out["default"] = spec.Default
	}
	return out
}

func riskSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"max_position_size_quote"},
		"description":          "该标的的风控参数。用户没提到的项请省略。",
		"properties": map[string]any{
			"max_position_size_quote": map[string]any{
				"type": "string",
				"description": "单笔仓位的硬上限，以计价货币（如 USDT）计的金额字符串，例如 \"1000\"。" +
					"用字符串而非数字，避免浮点精度问题。不管 position_sizing_mode 是哪种都必须填：" +
					"fixed_quote 模式下用户没说时填 \"1000\"；risk_pct 模式下如果用户没有额外说一个" +
					"具体上限金额，默认填 account_equity_quote 的值本身（相当于'单笔最多不超过全部" +
					"本金'这个最宽松的兜底），并在 restatement 里说明这是系统默认的上限、不是用户" +
					"主动要求的数字。",
			},
			"max_daily_loss_quote": map[string]any{
				"type":        "string",
				"description": "单日最大亏损金额字符串。用户没说时省略。",
			},
			"max_holding_period": map[string]any{
				"type":        "string",
				"pattern":     `^\d+(\.\d+)?(ms|s|m|h)$`,
				"description": "最大持仓时间，如 \"4h\"、\"90m\"。用户没说时省略。",
			},
			"stop_loss_mode": map[string]any{
				"type": "string", "enum": []string{"pct", "support_resistance", "poc"},
				"description": "止损怎么算：\"pct\"（默认，固定百分比，见 stop_loss_pct）、" +
					"\"support_resistance\"（用 support_resistance 模块在开仓那一刻检测到的最近" +
					"支撑/阻力位当止损价，此时 modules 里必须包含该模块）、或 " +
					"\"poc\"（用 poc 模块算出的成交量分布重心当止损价，modules 里必须包含 poc 模块）。" +
					"非 pct 模式下都不应再填 stop_loss_pct。用户没说时省略，等同 \"pct\"。",
			},
			"stop_loss_pct": map[string]any{
				"type": "number", "minimum": 0, "exclusiveMaximum": 1,
				"description": "止损比例，0.02 表示 2%。仅 stop_loss_mode 为 pct 时填写。用户没说时省略。",
			},
			"take_profit_mode": map[string]any{
				"type": "string", "enum": []string{"pct", "support_resistance", "poc"},
				"description": "止盈怎么算，取值与 stop_loss_mode 相同（support_resistance 模式下" +
					"取最近阻力位当止盈价，poc 模式下取成交量分布重心）。跟 stop_loss_mode 相互独立，" +
					"可以一个用支撑/阻力位或 POC、另一个用固定百分比。用户没说时省略，等同 \"pct\"。",
			},
			"take_profit_pct": map[string]any{
				"type": "number", "minimum": 0,
				"description": "止盈比例，0.05 表示 5%。仅 take_profit_mode 为 pct 时填写。用户没说时省略。",
			},
			"position_sizing_mode": map[string]any{
				"type": "string", "enum": []string{"fixed_quote", "risk_pct"},
				"description": "单笔开仓名义金额怎么算：\"fixed_quote\"（默认，固定金额，见 " +
					"max_position_size_quote）、或 \"risk_pct\"（按 账户权益 × 单笔风险比例 ÷ 止损距离" +
					"百分比 动态计算，仓位会随每次的止损距离变化）。risk_pct 模式下必须同时填 " +
					"account_equity_quote、risk_per_trade_pct，且策略必须配置了真的会生效的止损" +
					"（stop_loss_pct 大于 0，或 stop_loss_mode 用 support_resistance / poc）——没有" +
					"止损就算不出止损距离，也算不出仓位。max_position_size_quote 在任何模式下都仍然" +
					"生效，是算出来的仓位的硬上限：超过它这笔交易会被直接拒绝，不会被静默缩小。" +
					"用户没说时省略，等同 \"fixed_quote\"。",
			},
			"account_equity_quote": map[string]any{
				"type": "string",
				"description": "用户自报的账户权益，以计价货币（如 USDT）计的金额字符串。这是一个" +
					"用户声明的静态数字，不是平台实时读取的交易所余额，运行期间不会自动更新。仅 " +
					"position_sizing_mode 为 risk_pct 时需要。用户没有明确说过自己的账户权益/本金是" +
					"多少时，绝不能凭空填一个数字——必须走 clarification_needed 向用户询问，" +
					"跟止损止盈从不编造数字是同一条铁律。",
			},
			"risk_per_trade_pct": map[string]any{
				"type": "number", "exclusiveMinimum": 0, "exclusiveMaximum": 1,
				"description": "单笔愿意承担的账户权益风险比例，0.01 表示每笔最多亏账户权益的 1%" +
					"（触发止损的假设下）。仅 position_sizing_mode 为 risk_pct 时需要。",
			},
		},
	}
}

// ModuleCatalog 生成给 LLM 看的模块清单文本，作为 system prompt 的一部分。
//
// schema 负责机器可校验的硬约束，这份清单负责让模型理解每个模块与参数的含义——
// 两者都从注册表生成，不会互相打架。
func ModuleCatalog(reg *modules.Registry) string {
	var b []byte
	appendf := func(format string, args ...any) {
		b = append(b, fmt.Sprintf(format, args...)...)
	}

	appendf("平台当前提供以下 %d 个信号模块，你只能从中选择：\n", len(reg.All()))
	for _, m := range reg.All() {
		appendf("\n模块名：%s\n  作用：%s\n  参数：\n", m.Name(), m.Description())
		for _, p := range m.RequiredParams() {
			appendf("    - %s（%s，默认 %v，允许范围 %s）：%s\n",
				p.Name, p.Type, p.Default, p.AllowedDesc(), p.Description)
		}
	}
	return string(b)
}
