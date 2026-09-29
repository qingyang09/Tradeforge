package agent

import (
	"tradeforge/internal/i18n"
	"tradeforge/internal/modules"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// Schema is the JSON Schema handed to the LLM (represented as a map, for
// direct serialization).
type Schema map[string]any

// BuildSchema dynamically generates the Agent's output JSON Schema from the
// module registry, with every "description" hint rendered in lang -- so the
// LLM is steered to answer restatement/questions text in the language the
// caller requested.
//
// Key point: the module-name enum, and each module's parameter names and
// allowed ranges, are all read live from the registry rather than
// hand-written. That way "what modules the platform has" has a single source
// of truth — the schema automatically stays current when a module is added,
// leaving the LLM no room to invent one.
func BuildSchema(reg *modules.Registry, lang i18n.Lang) Schema {
	return Schema{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"outcome", "restatement", "config", "questions"},
		"properties": map[string]any{
			"outcome": map[string]any{
				"type":        "string",
				"enum":        []string{string(OutcomeConfig), string(OutcomeClarify)},
				"description": i18n.T(lang, "agent.schema.root.outcome"),
			},
			"restatement": map[string]any{
				"type":        "string",
				"description": i18n.T(lang, "agent.schema.root.restatement"),
			},
			"questions": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": i18n.T(lang, "agent.schema.root.questions"),
			},
			"config": map[string]any{
				"description": i18n.T(lang, "agent.schema.root.config"),
				"anyOf": []any{
					strategyConfigSchema(reg, lang),
					map[string]any{"type": "null"},
				},
			},
		},
	}
}

func strategyConfigSchema(reg *modules.Registry, lang i18n.Lang) map[string]any {
	timeframes := make([]string, 0, len(types.SupportedTimeframes))
	for _, tf := range types.SupportedTimeframes {
		timeframes = append(timeframes, string(tf))
	}

	moduleBranches := make([]any, 0, len(reg.All()))
	for _, m := range reg.All() {
		moduleBranches = append(moduleBranches, moduleConfigSchema(m, timeframes, lang))
	}

	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"name", "symbol", "timeframe", "modules", "combine", "risk"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": i18n.T(lang, "agent.schema.strategy.name"),
			},
			"symbol": map[string]any{
				"type":        "string",
				"pattern":     "^[A-Z0-9]{4,20}$",
				"description": i18n.T(lang, "agent.schema.strategy.symbol"),
			},
			"timeframe": map[string]any{
				"type":        "string",
				"enum":        timeframes,
				"description": i18n.T(lang, "agent.schema.strategy.timeframe"),
			},
			"combine": map[string]any{
				"type":        "string",
				"enum":        []string{string(types.CombineAll), string(types.CombineWeighted)},
				"description": i18n.T(lang, "agent.schema.strategy.combine"),
			},
			"threshold": map[string]any{
				"type": "number", "minimum": 0, "maximum": 1,
				"description": i18n.T(lang, "agent.schema.strategy.threshold"),
			},
			"modules": map[string]any{
				"type":        "array",
				"minItems":    1,
				"maxItems":    strategy.MaxModulesPerStrategy,
				"items":       map[string]any{"oneOf": moduleBranches},
				"description": i18n.T(lang, "agent.schema.strategy.modules"),
			},
			"risk": riskSchema(lang),
		},
	}
}

// moduleConfigSchema generates one schema branch for a single module: the
// module field is pinned with const, and the params property table is
// generated live from that module's RequiredParams.
//
// Using oneOf + const instead of one generic "params: object" is so that
// mistakes like "a lookback parameter written under cvd_orderflow" get
// blocked at the schema level, rather than only erroring at runtime.
func moduleConfigSchema(m modules.SignalModule, timeframes []string, lang i18n.Lang) map[string]any {
	props := map[string]any{}
	for _, spec := range m.RequiredParams() {
		props[spec.Name] = paramSchema(spec, lang)
	}

	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"module", "params"},
		"description":          i18n.Render(lang, m.Description()),
		"properties": map[string]any{
			"module": map[string]any{"const": m.Name()},
			"weight": map[string]any{
				"type": "number", "exclusiveMinimum": 0, "maximum": 1,
				"description": i18n.T(lang, "agent.schema.module.weight"),
			},
			"timeframe": map[string]any{
				"type":        "string",
				"enum":        timeframes,
				"description": i18n.T(lang, "agent.schema.module.timeframe"),
			},
			"params": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           props,
				"description":          i18n.T(lang, "agent.schema.module.params"),
			},
		},
	}
}

func paramSchema(spec types.ParamSpec, lang i18n.Lang) map[string]any {
	out := map[string]any{"description": i18n.Render(lang, spec.Description)}

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

func riskSchema(lang i18n.Lang) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"max_position_size_quote"},
		"description":          i18n.T(lang, "agent.schema.risk.root"),
		"properties": map[string]any{
			"max_position_size_quote": map[string]any{
				"type":        "string",
				"description": i18n.T(lang, "agent.schema.risk.max_position_size_quote"),
			},
			"max_daily_loss_quote": map[string]any{
				"type":        "string",
				"description": i18n.T(lang, "agent.schema.risk.max_daily_loss_quote"),
			},
			"max_holding_period": map[string]any{
				"type":        "string",
				"pattern":     `^\d+(\.\d+)?(ms|s|m|h)$`,
				"description": i18n.T(lang, "agent.schema.risk.max_holding_period"),
			},
			"stop_loss_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"pct", "support_resistance", "poc"},
				"description": i18n.T(lang, "agent.schema.risk.stop_loss_mode"),
			},
			"stop_loss_pct": map[string]any{
				"type": "number", "minimum": 0, "exclusiveMaximum": 1,
				"description": i18n.T(lang, "agent.schema.risk.stop_loss_pct"),
			},
			"take_profit_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"pct", "support_resistance", "poc"},
				"description": i18n.T(lang, "agent.schema.risk.take_profit_mode"),
			},
			"take_profit_pct": map[string]any{
				"type": "number", "minimum": 0,
				"description": i18n.T(lang, "agent.schema.risk.take_profit_pct"),
			},
			"position_sizing_mode": map[string]any{
				"type":        "string",
				"enum":        []string{"fixed_quote", "risk_pct"},
				"description": i18n.T(lang, "agent.schema.risk.position_sizing_mode"),
			},
			"account_equity_quote": map[string]any{
				"type":        "string",
				"description": i18n.T(lang, "agent.schema.risk.account_equity_quote"),
			},
			"risk_per_trade_pct": map[string]any{
				"type": "number", "exclusiveMinimum": 0, "exclusiveMaximum": 1,
				"description": i18n.T(lang, "agent.schema.risk.risk_per_trade_pct"),
			},
		},
	}
}

// ModuleCatalog generates the module-catalog text shown to the LLM, used as
// part of the system prompt, rendered in lang.
//
// The schema handles machine-checkable hard constraints; this catalog is
// what lets the model understand the meaning of each module and parameter —
// both are generated from the registry, so they never fall out of sync with
// each other.
func ModuleCatalog(reg *modules.Registry, lang i18n.Lang) string {
	var b []byte
	appendf := func(key string, args ...any) {
		b = append(b, i18n.T(lang, key, args...)...)
	}

	appendf("agent.schema.catalog.header", "count", len(reg.All()))
	for _, m := range reg.All() {
		appendf("agent.schema.catalog.module", "name", m.Name(), "description", i18n.Render(lang, m.Description()))
		for _, p := range m.RequiredParams() {
			appendf("agent.schema.catalog.param",
				"name", p.Name, "type", p.Type, "default", p.Default, "range", p.AllowedDesc(),
				"description", i18n.Render(lang, p.Description))
		}
	}
	return string(b)
}
