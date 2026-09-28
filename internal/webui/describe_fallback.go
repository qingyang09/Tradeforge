package webui

import (
	"fmt"
	"sort"
	"strings"

	"tradeforge/internal/i18n"
	"tradeforge/pkg/types"
)

// describePlain is the restatement fallback used when no LLM key is
// configured: it composes plain sentences in Go, no model call.
//
// Visually-built configs from the canvas builder are already structured
// data the user assembled by hand — there's no ambiguity to "translate," so
// a deterministic restatement here is no worse than the LLM version, and it
// means this path doesn't depend on whether an LLM key happens to be
// configured (the text wizard path doesn't have this option, since natural
// language genuinely needs a model to interpret). The tone matches the
// Agent's own restatement: state facts only, no evaluative language.
//
// lang selects which language each fragment renders in via internal/i18n --
// see render.go's msg template func doc comment for why callers currently
// always pass i18n.DefaultLang (Chinese) rather than a real per-request
// value; this function itself is already fully ready for that once Phase 4
// wires up the real language resolution.
func describePlain(lang i18n.Lang, cfg types.StrategyConfig) string {
	var b strings.Builder

	b.WriteString(i18n.T(lang, "webui.describe.symbol_timeframe", "symbol", cfg.Symbol, "timeframe", cfg.Timeframe))

	switch cfg.Combine {
	case types.CombineWeighted:
		b.WriteString(i18n.T(lang, "webui.describe.combine_weighted", "threshold", fmt.Sprintf("%.2f", cfg.Threshold)))
	default:
		b.WriteString(i18n.T(lang, "webui.describe.combine_all"))
	}

	names := make([]string, len(cfg.Modules))
	for i, m := range cfg.Modules {
		names[i] = m.Module
	}
	b.WriteString(i18n.T(lang, "webui.describe.modules_included", "names", strings.Join(names, i18n.T(lang, "webui.describe.list_sep"))))

	for _, m := range cfg.Modules {
		b.WriteString(i18n.T(lang, "webui.describe.module_params", "module", m.Module, "params", formatParams(lang, m.Params)))
	}

	b.WriteString(describeRisk(lang, cfg.Risk))
	return b.String()
}

func formatParams(lang i18n.Lang, params map[string]any) string {
	if len(params) == 0 {
		return i18n.T(lang, "webui.describe.default_params")
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%v", k, params[k])
	}
	return strings.Join(parts, i18n.T(lang, "webui.describe.param_sep"))
}

func describeRisk(lang i18n.Lang, risk types.RiskConfig) string {
	var b strings.Builder
	if risk.PositionSizingMode.EffectiveOrFixed() == types.PositionSizingModeRiskPct {
		b.WriteString(i18n.T(lang, "webui.describe.risk.sizing_risk_pct",
			"equity", risk.AccountEquityQuote, "pct", fmt.Sprintf("%.2f", risk.RiskPerTradePct*100), "cap", risk.MaxPositionSizeQuote))
	} else {
		b.WriteString(i18n.T(lang, "webui.describe.risk.sizing_fixed", "cap", risk.MaxPositionSizeQuote))
	}
	if risk.MaxDailyLossQuote.IsPositive() {
		b.WriteString(i18n.T(lang, "webui.describe.risk.max_daily_loss", "value", risk.MaxDailyLossQuote))
	}
	switch risk.StopLossMode.EffectiveOrPct() {
	case types.RiskLevelModeSupportResistance:
		b.WriteString(i18n.T(lang, "webui.describe.risk.stop_loss_support_resistance"))
	case types.RiskLevelModePOC:
		b.WriteString(i18n.T(lang, "webui.describe.risk.stop_loss_poc"))
	default:
		if risk.StopLossPct > 0 {
			b.WriteString(i18n.T(lang, "webui.describe.risk.stop_loss_pct", "pct", fmt.Sprintf("%.2f", risk.StopLossPct*100)))
		}
	}
	switch risk.TakeProfitMode.EffectiveOrPct() {
	case types.RiskLevelModeSupportResistance:
		b.WriteString(i18n.T(lang, "webui.describe.risk.take_profit_support_resistance"))
	case types.RiskLevelModePOC:
		b.WriteString(i18n.T(lang, "webui.describe.risk.take_profit_poc"))
	default:
		if risk.TakeProfitPct > 0 {
			b.WriteString(i18n.T(lang, "webui.describe.risk.take_profit_pct", "pct", fmt.Sprintf("%.2f", risk.TakeProfitPct*100)))
		}
	}
	if risk.MaxHoldingPeriod.Std() > 0 {
		b.WriteString(i18n.T(lang, "webui.describe.risk.max_holding", "value", risk.MaxHoldingPeriod))
	}
	return b.String()
}
