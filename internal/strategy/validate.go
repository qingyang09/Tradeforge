// Package strategy provides validation for StrategyConfig.
//
// Validation lives in its own package so the composition engine and the
// Agent translation layer can share exactly the same rule set: the
// validation the Agent runs after generating a config must match, byte for
// byte, the validation the engine runs before execution — otherwise you get
// the mismatch where "the Agent said it was fine, but the engine only
// errored out once it actually ran."
package strategy

import (
	"errors"
	"fmt"
	"strings"

	"tradeforge/internal/i18n"
	"tradeforge/internal/modules"
	"tradeforge/pkg/types"
)

// MaxModulesPerStrategy caps how many modules a single strategy can have.
// The cap isn't about performance — it's about explainability: past a
// certain number of modules, the user can no longer understand why a given
// trade fired.
const MaxModulesPerStrategy = 8

// ValidationError collects every problem found during one validation pass.
//
// Collecting everything instead of returning on the first failure is
// deliberate: the Agent needs to tell the user every problem at once,
// instead of making them fix one, resubmit, and get told about the next.
//
// Issues is []types.Message (not []string): each issue is a translatable
// key+args, resolved through internal/i18n at the point it's actually shown
// to the user — see pkg/types/i18n.go and the plan's Class A/B split.
type ValidationError struct {
	Issues []types.Message
}

func (e *ValidationError) Error() string {
	rendered := make([]string, len(e.Issues))
	for i, issue := range e.Issues {
		rendered[i] = i18n.Render(i18n.LangEN, issue)
	}
	if len(rendered) == 1 {
		return "strategy config validation failed: " + rendered[0]
	}
	return fmt.Sprintf("strategy config validation failed (%d issues):\n  - %s",
		len(rendered), strings.Join(rendered, "\n  - "))
}

// Validate validates a strategy config and returns each module's normalized
// parameters (keyed by module name).
//
// The normalized parameters already have defaults filled in and have been
// type-converted and range-checked, so the engine can use them directly
// without re-parsing on every candle.
func Validate(cfg types.StrategyConfig, reg *modules.Registry) (map[string]map[string]any, error) {
	v := &ValidationError{}
	add := func(key string, args ...any) {
		v.Issues = append(v.Issues, types.Msg(key, args...))
	}

	if strings.TrimSpace(cfg.Name) == "" {
		add("strategy.validate.name_required")
	}
	if strings.TrimSpace(cfg.Symbol) == "" {
		add("strategy.validate.symbol_required")
	}
	if !cfg.Timeframe.Valid() {
		add("strategy.validate.timeframe_unsupported", "value", cfg.Timeframe, "allowed", fmt.Sprint(types.SupportedTimeframes))
	}
	if !cfg.Combine.Valid() {
		add("strategy.validate.combine_unsupported", "value", cfg.Combine, "a", types.CombineAll, "b", types.CombineWeighted)
	}
	if cfg.State != "" && !cfg.State.Valid() {
		add("strategy.validate.state_invalid", "value", cfg.State)
	}

	switch {
	case len(cfg.Modules) == 0:
		add("strategy.validate.modules_required")
	case len(cfg.Modules) > MaxModulesPerStrategy:
		add("strategy.validate.modules_too_many", "count", len(cfg.Modules), "max", MaxModulesPerStrategy)
	}

	resolved := make(map[string]map[string]any, len(cfg.Modules))
	seen := make(map[string]bool, len(cfg.Modules))

	for i, mc := range cfg.Modules {
		if seen[mc.Module] {
			add("strategy.validate.module_duplicate", "module", mc.Module)
			continue
		}
		seen[mc.Module] = true

		m, err := reg.Get(mc.Module)
		if err != nil {
			add("strategy.validate.module_index_error", "index", i, "error", err.Error())
			continue
		}
		params, err := modules.ResolveParams(m, mc.Params)
		if err != nil {
			add("strategy.validate.module_params_error", "index", i, "module", mc.Module, "error", err.Error())
			continue
		}
		resolved[mc.Module] = params

		// An empty module timeframe means "follow the strategy's trigger
		// timeframe," which is always valid; if it's set, it must be a valid
		// timeframe and must not be faster than the trigger timeframe —
		// decisions are only made once, at the close of the trigger
		// timeframe, so a faster module update would never be seen in time;
		// such a config is inherently self-contradictory.
		if mc.Timeframe != "" {
			if !mc.Timeframe.Valid() {
				add("strategy.validate.module_timeframe_unsupported",
					"index", i, "module", mc.Module, "value", mc.Timeframe, "allowed", fmt.Sprint(types.SupportedTimeframes))
			} else if mc.Timeframe.Duration() < cfg.Timeframe.Duration() {
				add("strategy.validate.module_timeframe_too_fast",
					"index", i, "module", mc.Module, "value", mc.Timeframe, "trigger", cfg.Timeframe)
			}
		}

		// Weight only matters under WEIGHTED, but an invalid value is still
		// reported here, so the user doesn't end up thinking their weight
		// took effect when it didn't.
		if mc.Weight < 0 || mc.Weight > 1 {
			add("strategy.validate.module_weight_out_of_range", "index", i, "module", mc.Module, "value", mc.Weight)
		}
		if cfg.Combine == types.CombineWeighted && mc.Weight <= 0 {
			add("strategy.validate.module_weight_required_for_weighted", "index", i, "module", mc.Module)
		}
	}

	if cfg.Combine == types.CombineWeighted {
		if cfg.Threshold <= 0 || cfg.Threshold > 1 {
			add("strategy.validate.threshold_out_of_range", "value", cfg.Threshold)
		}
	}

	if err := validateRisk(cfg.Risk, cfg.Modules, add); err != nil {
		return nil, err
	}

	if len(v.Issues) > 0 {
		return nil, v
	}
	return resolved, nil
}

func validateRisk(r types.RiskConfig, modules []types.ModuleConfig, add func(string, ...any)) error {
	// Max position size per trade must be positive: without it there's no
	// position cap at all, and it's the one risk control the execution layer
	// strictly requires regardless of PositionSizingMode — under
	// fixed_quote it's the position size itself, under risk_pct it's the
	// hard ceiling on the computed position size.
	if !r.MaxPositionSizeQuote.IsPositive() {
		add("strategy.validate.risk.max_position_required")
	}
	if r.MaxDailyLossQuote.IsNegative() {
		add("strategy.validate.risk.max_daily_loss_negative")
	}
	if r.MaxHoldingPeriod < 0 {
		add("strategy.validate.risk.max_holding_negative")
	}

	moduleNames := make(map[string]bool, len(modules))
	for _, mc := range modules {
		moduleNames[mc.Module] = true
	}

	validateLevelMode(r.StopLossMode, "strategy.validate.risk.level.stop_loss", r.StopLossPct, moduleNames, add)
	if r.StopLossMode.EffectiveOrPct() == types.RiskLevelModePct && (r.StopLossPct < 0 || r.StopLossPct >= 1) {
		add("strategy.validate.risk.stop_loss_pct_out_of_range", "value", r.StopLossPct)
	}

	validateLevelMode(r.TakeProfitMode, "strategy.validate.risk.level.take_profit", r.TakeProfitPct, moduleNames, add)
	if r.TakeProfitMode.EffectiveOrPct() == types.RiskLevelModePct && r.TakeProfitPct < 0 {
		add("strategy.validate.risk.take_profit_pct_negative", "value", r.TakeProfitPct)
	}

	validatePositionSizingMode(r, add)
	return nil
}

// validatePositionSizingMode validates the position sizing mode: the value
// must be valid; under risk_pct there must be a positive account equity and
// a risk percentage within (0,1), and a stop loss that will actually take
// effect must already be configured (otherwise the stop distance can't be
// computed, and finding that out only at the moment of opening a position is
// too late); under fixed_quote, those fields must not be set — same stance
// as validateLevelMode: fields from the two modes must never be mixed;
// reject rather than pick one ambiguously.
func validatePositionSizingMode(r types.RiskConfig, add func(string, ...any)) {
	if !r.PositionSizingMode.Valid() {
		add("strategy.validate.risk.sizing_mode_invalid",
			"value", r.PositionSizingMode, "a", types.PositionSizingModeFixedQuote, "b", types.PositionSizingModeRiskPct)
		return
	}

	switch r.PositionSizingMode.EffectiveOrFixed() {
	case types.PositionSizingModeFixedQuote:
		if r.AccountEquityQuote.IsPositive() || r.RiskPerTradePct != 0 {
			add("strategy.validate.risk.fixed_quote_extra_fields_set")
		}
	case types.PositionSizingModeRiskPct:
		if !r.AccountEquityQuote.IsPositive() {
			add("strategy.validate.risk.risk_pct_equity_required")
		}
		if r.RiskPerTradePct <= 0 || r.RiskPerTradePct >= 1 {
			add("strategy.validate.risk.risk_per_trade_pct_out_of_range", "value", r.RiskPerTradePct)
		}
		// risk_pct depends on the stop-loss distance to compute the position
		// size; under pct mode, StopLossPct=0 means no stop loss is set, so
		// the distance can't be computed — this must be caught at config
		// time.
		if r.StopLossMode.EffectiveOrPct() == types.RiskLevelModePct && r.StopLossPct <= 0 {
			add("strategy.validate.risk.risk_pct_requires_stop_loss")
		}
	}
}

// validateLevelMode validates the stop-loss/take-profit mode field itself:
// the value must be valid; under a non-pct mode, the module that mode
// depends on (RiskLevelMode.RequiredModule, e.g. support_resistance or poc)
// must also be present in Modules (otherwise there's no corresponding price
// level data available), and the matching percentage field must be left
// unset — setting both at once creates ambiguity over which one actually
// takes effect; reject rather than pick one ambiguously.
//
// keyPrefix picks which stop-loss/take-profit catalog entries to use (e.g.
// "strategy.validate.risk.level.stop_loss") -- the label shown to the user
// ("stop loss" vs "take profit") differs, everything else about the check is
// identical, so the message keys are namespaced per call site instead of the
// message text being passed in as a runtime string.
func validateLevelMode(
	mode types.RiskLevelMode, keyPrefix string, pct float64, moduleNames map[string]bool,
	add func(string, ...any),
) {
	if !mode.Valid() {
		add(keyPrefix+".mode_invalid", "value", mode,
			"a", types.RiskLevelModePct, "b", types.RiskLevelModeSupportResistance, "c", types.RiskLevelModePOC)
		return
	}
	required := mode.RequiredModule()
	if required == "" {
		return
	}
	if !moduleNames[required] {
		add(keyPrefix+".missing_required_module", "mode", mode, "module", required)
	}
	if pct != 0 {
		add(keyPrefix+".pct_set_with_level_mode", "mode", mode)
	}
}

// AsValidationError extracts a *ValidationError from an error chain.
func AsValidationError(err error) (*ValidationError, bool) {
	var ve *ValidationError
	ok := errors.As(err, &ve)
	return ve, ok
}
