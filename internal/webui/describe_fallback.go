package webui

import (
	"fmt"
	"sort"
	"strings"

	"tradeforge/pkg/types"
)

// describePlain 是没有配置 LLM key 时的复述兜底：纯 Go 拼句子，不调用任何模型。
//
// 可视化建策的配置本来就是用户在画板上亲手摆出来的结构化数据，没有需要"翻译"的
// 歧义——一份确定性的复述在这里不比 LLM 版本差，还顺带让这条建策路径不再依赖
// 是否配置了 LLM key（文字向导那条路径没有这个选项，因为自然语言本身就需要模型
// 来理解）。语气跟 Agent 版复述对齐：只陈述事实，不加任何评价性词汇。
func describePlain(cfg types.StrategyConfig) string {
	var b strings.Builder

	fmt.Fprintf(&b, "标的 %s，%s 周期。", cfg.Symbol, cfg.Timeframe)

	switch cfg.Combine {
	case types.CombineWeighted:
		fmt.Fprintf(&b, "组合方式为 WEIGHTED（按权重加权置信度，超过阈值 %.2f 才触发）。", cfg.Threshold)
	default:
		b.WriteString("组合方式为 ALL（所有模块同方向才触发）。")
	}

	b.WriteString("包含以下模块：")
	names := make([]string, len(cfg.Modules))
	for i, m := range cfg.Modules {
		names[i] = m.Module
	}
	b.WriteString(strings.Join(names, "、"))
	b.WriteString("。")

	for _, m := range cfg.Modules {
		fmt.Fprintf(&b, " %s 的参数：%s。", m.Module, formatParams(m.Params))
	}

	b.WriteString(describeRisk(cfg.Risk))
	return b.String()
}

func formatParams(params map[string]any) string {
	if len(params) == 0 {
		return "全部使用系统默认值"
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
	return strings.Join(parts, "，")
}

func describeRisk(risk types.RiskConfig) string {
	var b strings.Builder
	if risk.PositionSizingMode.EffectiveOrFixed() == types.PositionSizingModeRiskPct {
		fmt.Fprintf(&b, " 风控：按风险百分比开仓——账户权益 %s，单笔风险 %.2f%%，"+
			"仓位硬上限 %s（算出来的仓位超过它会被直接拒绝，不会自动缩小）。",
			risk.AccountEquityQuote, risk.RiskPerTradePct*100, risk.MaxPositionSizeQuote)
	} else {
		fmt.Fprintf(&b, " 风控：单笔最大仓位 %s。", risk.MaxPositionSizeQuote)
	}
	if risk.MaxDailyLossQuote.IsPositive() {
		fmt.Fprintf(&b, "单日最大亏损 %s。", risk.MaxDailyLossQuote)
	}
	switch risk.StopLossMode.EffectiveOrPct() {
	case types.RiskLevelModeSupportResistance:
		b.WriteString("止损：跌破开仓时最近的支撑位。")
	case types.RiskLevelModePOC:
		b.WriteString("止损：收回开仓时的成交量分布重心（POC）。")
	default:
		if risk.StopLossPct > 0 {
			fmt.Fprintf(&b, "止损 %.2f%%。", risk.StopLossPct*100)
		}
	}
	switch risk.TakeProfitMode.EffectiveOrPct() {
	case types.RiskLevelModeSupportResistance:
		b.WriteString("止盈：触及开仓时最近的阻力位。")
	case types.RiskLevelModePOC:
		b.WriteString("止盈：触及开仓时的成交量分布重心（POC）。")
	default:
		if risk.TakeProfitPct > 0 {
			fmt.Fprintf(&b, "止盈 %.2f%%。", risk.TakeProfitPct*100)
		}
	}
	if risk.MaxHoldingPeriod.Std() > 0 {
		fmt.Fprintf(&b, "最长持仓 %s。", risk.MaxHoldingPeriod)
	}
	return b.String()
}
