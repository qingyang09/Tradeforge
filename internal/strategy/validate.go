// Package strategy 提供 StrategyConfig 的校验。
//
// 校验单独成包，是为了让组合引擎和 Agent 翻译层共用同一套规则：
// Agent 生成配置后过的校验，必须和引擎执行前过的校验一字不差，
// 否则就会出现"Agent 说没问题、引擎跑起来才报错"的错位。
package strategy

import (
	"errors"
	"fmt"
	"strings"

	"tradeforge/internal/modules"
	"tradeforge/pkg/types"
)

// MaxModulesPerStrategy 限制单个策略的模块数量。
// 上限的意义不在性能，而在可解释性：模块过多时用户已无法理解一笔交易为何触发。
const MaxModulesPerStrategy = 8

// ValidationError 汇总一次校验中发现的全部问题。
//
// 刻意收集全部错误而不是遇到第一个就返回：Agent 需要一次性把所有问题
// 告诉用户，而不是让用户改一个、再被告知还有一个。
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

// Validate 校验策略配置，并返回各模块规范化后的参数（键为模块名）。
//
// 规范化后的参数已填好默认值、完成类型转换与范围检查，引擎可直接使用，
// 不必在每根 K 线上重复解析。
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

		// 模块周期留空表示跟随策略的触发周期，天然合法；填了就必须是合法周期，
		// 且不能比触发周期更快——决策只在触发周期收盘时算一次，更快的模块更新永远
		// 来不及被看到，这种配置本身就是矛盾的。
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

		// 权重只在 WEIGHTED 下有意义，但填了非法值仍要报出来，
		// 免得用户以为自己设置的权重生效了。
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
	// 单笔最大仓位必须为正：没有它就等于没有仓位上限，这是执行层唯一强制要求的
	// 风控项——不管 PositionSizingMode 是哪种都必须填：fixed_quote 模式下它就是
	// 仓位金额本身，risk_pct 模式下它是算出来的仓位的硬上限。
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

// validatePositionSizingMode 校验仓位模式：取值合法；risk_pct 模式下必须有正的账户
// 权益、落在 (0,1) 的风险比例，且必须已经配置了一个真的会生效的止损（否则算不出
// 止损距离，等真正开仓那一刻才发现算不出来就太晚了）；fixed_quote 模式下这些字段
// 不该被填——跟 validateLevelMode 是同一个态度：两个模式的字段不能混填，宁可拒绝
// 也不要含糊地择一使用。
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
		// risk_pct 模式依赖止损距离才能算出仓位；pct 模式下 StopLossPct=0 等于没设
		// 止损，此时算不出距离，必须在配置阶段就拦住。
		if r.StopLossMode.EffectiveOrPct() == types.RiskLevelModePct && r.StopLossPct <= 0 {
			add("风控：仓位模式为 risk_pct 时必须同时设置止损" +
				"（stop_loss_pct 大于 0，或 stop_loss_mode 用 support_resistance / poc），" +
				"否则无法据此计算仓位")
		}
	}
}

// validateLevelMode 校验止损/止盈的模式字段本身：取值必须合法；非 pct 模式下必须同时把
// 该模式依赖的模块（RiskLevelMode.RequiredModule，比如 support_resistance 或 poc）加入
// Modules（不然没有对应的价位数据可用），且对应的百分比字段必须留空——两个字段同时
// 设置会产生"到底哪个生效"的歧义，宁可拒绝也不要含糊地择一使用。
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

// AsValidationError 从错误链中取出 *ValidationError。
func AsValidationError(err error) (*ValidationError, bool) {
	var ve *ValidationError
	ok := errors.As(err, &ve)
	return ve, ok
}
