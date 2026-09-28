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
type ValidationError struct {
	Issues []string
}

func (e *ValidationError) Error() string {
	if len(e.Issues) == 1 {
		return "策略配置校验失败：" + e.Issues[0]
	}
	return fmt.Sprintf("策略配置校验失败（%d 项）：\n  - %s",
		len(e.Issues), strings.Join(e.Issues, "\n  - "))
}

// Validate validates a strategy config and returns each module's normalized
// parameters (keyed by module name).
//
// The normalized parameters already have defaults filled in and have been
// type-converted and range-checked, so the engine can use them directly
// without re-parsing on every candle.
func Validate(cfg types.StrategyConfig, reg *modules.Registry) (map[string]map[string]any, error) {
	v := &ValidationError{}
	add := func(format string, args ...any) {
		v.Issues = append(v.Issues, fmt.Sprintf(format, args...))
	}

	if strings.TrimSpace(cfg.Name) == "" {
		add("策略名称不能为空")
	}
	if strings.TrimSpace(cfg.Symbol) == "" {
		add("交易标的（symbol）不能为空")
	}
	if !cfg.Timeframe.Valid() {
		add("周期 %q 不受支持，可选值为 %v", cfg.Timeframe, types.SupportedTimeframes)
	}
	if !cfg.Combine.Valid() {
		add("组合逻辑 %q 不受支持，可选值为 %s / %s", cfg.Combine, types.CombineAll, types.CombineWeighted)
	}
	if cfg.State != "" && !cfg.State.Valid() {
		add("策略状态 %q 不是合法状态", cfg.State)
	}

	switch {
	case len(cfg.Modules) == 0:
		add("策略至少要包含一个模块")
	case len(cfg.Modules) > MaxModulesPerStrategy:
		add("模块数量 %d 超过上限 %d", len(cfg.Modules), MaxModulesPerStrategy)
	}

	resolved := make(map[string]map[string]any, len(cfg.Modules))
	seen := make(map[string]bool, len(cfg.Modules))

	for i, mc := range cfg.Modules {
		if seen[mc.Module] {
			add("模块 %q 重复出现；同一模块的不同参数组合暂不支持，请只保留一份", mc.Module)
			continue
		}
		seen[mc.Module] = true

		m, err := reg.Get(mc.Module)
		if err != nil {
			add("modules[%d]：%v", i, err)
			continue
		}
		params, err := modules.ResolveParams(m, mc.Params)
		if err != nil {
			add("modules[%d]（%s）：%v", i, mc.Module, err)
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
				add("modules[%d]（%s）：周期 %q 不受支持，可选值为 %v",
					i, mc.Module, mc.Timeframe, types.SupportedTimeframes)
			} else if mc.Timeframe.Duration() < cfg.Timeframe.Duration() {
				add("modules[%d]（%s）：周期 %s 比策略触发周期 %s 更快；"+
					"触发周期必须是所有模块里最快（或并列最快）的那个",
					i, mc.Module, mc.Timeframe, cfg.Timeframe)
			}
		}

		// Weight only matters under WEIGHTED, but an invalid value is still
		// reported here, so the user doesn't end up thinking their weight
		// took effect when it didn't.
		if mc.Weight < 0 || mc.Weight > 1 {
			add("modules[%d]（%s）：权重 %v 超出 (0, 1] 范围", i, mc.Module, mc.Weight)
		}
		if cfg.Combine == types.CombineWeighted && mc.Weight <= 0 {
			add("modules[%d]（%s）：WEIGHTED 组合下每个模块都必须配置大于 0 的权重", i, mc.Module)
		}
	}

	if cfg.Combine == types.CombineWeighted {
		if cfg.Threshold <= 0 || cfg.Threshold > 1 {
			add("WEIGHTED 组合下触发阈值必须落在 (0, 1]，当前为 %v", cfg.Threshold)
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
		add("风控：单笔最大仓位（max_position_size_quote）必须大于 0")
	}
	if r.MaxDailyLossQuote.IsNegative() {
		add("风控：单日最大亏损（max_daily_loss_quote）不能为负数，它表示亏损额度的绝对值")
	}
	if r.MaxHoldingPeriod < 0 {
		add("风控：最大持仓时间不能为负")
	}

	moduleNames := make(map[string]bool, len(modules))
	for _, mc := range modules {
		moduleNames[mc.Module] = true
	}

	validateLevelMode(r.StopLossMode, "止损", "stop_loss_mode", r.StopLossPct, moduleNames, add)
	if r.StopLossMode.EffectiveOrPct() == types.RiskLevelModePct && (r.StopLossPct < 0 || r.StopLossPct >= 1) {
		add("风控：止损比例必须落在 [0, 1)，当前为 %v", r.StopLossPct)
	}

	validateLevelMode(r.TakeProfitMode, "止盈", "take_profit_mode", r.TakeProfitPct, moduleNames, add)
	if r.TakeProfitMode.EffectiveOrPct() == types.RiskLevelModePct && r.TakeProfitPct < 0 {
		add("风控：止盈比例不能为负，当前为 %v", r.TakeProfitPct)
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
		add("风控：仓位模式（position_sizing_mode）取值 %q 不受支持，可选 \"%s\" / \"%s\"",
			r.PositionSizingMode, types.PositionSizingModeFixedQuote, types.PositionSizingModeRiskPct)
		return
	}

	switch r.PositionSizingMode.EffectiveOrFixed() {
	case types.PositionSizingModeFixedQuote:
		if r.AccountEquityQuote.IsPositive() || r.RiskPerTradePct != 0 {
			add("风控：仓位模式为 fixed_quote 时不应该填账户权益（account_equity_quote）或" +
				"单笔风险比例（risk_per_trade_pct），两种模式的字段不能混填")
		}
	case types.PositionSizingModeRiskPct:
		if !r.AccountEquityQuote.IsPositive() {
			add("风控：仓位模式为 risk_pct 时必须填一个大于 0 的账户权益（account_equity_quote）")
		}
		if r.RiskPerTradePct <= 0 || r.RiskPerTradePct >= 1 {
			add("风控：单笔风险比例（risk_per_trade_pct）必须落在 (0, 1)，当前为 %v", r.RiskPerTradePct)
		}
		// risk_pct depends on the stop-loss distance to compute the position
		// size; under pct mode, StopLossPct=0 means no stop loss is set, so
		// the distance can't be computed — this must be caught at config
		// time.
		if r.StopLossMode.EffectiveOrPct() == types.RiskLevelModePct && r.StopLossPct <= 0 {
			add("风控：仓位模式为 risk_pct 时必须同时设置止损" +
				"（stop_loss_pct 大于 0，或 stop_loss_mode 用 support_resistance / poc），" +
				"否则无法据此计算仓位")
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
func validateLevelMode(
	mode types.RiskLevelMode, label, field string, pct float64, moduleNames map[string]bool,
	add func(string, ...any),
) {
	if !mode.Valid() {
		add("风控：%s（%s）取值 %q 不受支持，可选 \"%s\" / \"%s\" / \"%s\"",
			label, field, mode, types.RiskLevelModePct, types.RiskLevelModeSupportResistance, types.RiskLevelModePOC)
		return
	}
	required := mode.RequiredModule()
	if required == "" {
		return
	}
	if !moduleNames[required] {
		add("风控：%s 设为 %s 模式，但策略的模块列表里没有 %s，没有它就算不出对应的价位",
			label, mode, required)
	}
	if pct != 0 {
		add("风控：%s 已设为 %s 模式，不应该再同时填百分比，两者只能生效一个", label, mode)
	}
}

// AsValidationError extracts a *ValidationError from an error chain.
func AsValidationError(err error) (*ValidationError, bool) {
	var ve *ValidationError
	ok := errors.As(err, &ve)
	return ve, ok
}
