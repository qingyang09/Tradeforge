package i18n

// Catalog entries for internal/engine's decision aggregation
// (Decision.Reason).
func init() {
	register(LangEN, map[string]string{
		"engine.decision.unknown_combine_mode":     "unknown combine mode {mode}, not triggering",
		"engine.decision.no_signals":               "no module signals",
		"engine.decision.blocker.degraded":         "{module} is degraded ({err})",
		"engine.decision.blocker.neutral":          "{module} is neutral",
		"engine.decision.blocker.opposing":         "{module}'s direction is {direction}, opposite the rest of the modules' {majority}",
		"engine.decision.all_blocked":              "ALL combine requires all {count} modules to agree, but: {blockers}",
		"engine.decision.all_triggered":            "ALL combine: all {count} modules gave a {direction} signal, average confidence {score}",
		"engine.decision.zero_total_weight":        "sum of all module weights is 0, cannot weight",
		"engine.decision.degraded_note":            " ({modules} degraded, counted as neutral in the denominator)",
		"engine.decision.weighted_below_threshold": "WEIGHTED combine: weighted net strength {score}, below threshold {threshold}{note}",
		"engine.decision.weighted_triggered":       "WEIGHTED combine: weighted net strength {score} ({direction} direction), meets threshold {threshold}{note}",
	})
	register(LangZH, map[string]string{
		"engine.decision.unknown_combine_mode": "未知的组合方式 {mode}，不触发",
		"engine.decision.no_signals":           "没有任何模块信号",
		"engine.decision.blocker.degraded":     "{module} 已降级（{err}）",
		"engine.decision.blocker.neutral":      "{module} 为中性",
		// 保留"相反"字样：engine_test.go 的 TestAggregateAllBlockedByOpposingModule
		// 断言这个子字符串。
		"engine.decision.blocker.opposing":         "{module} 方向为 {direction}，与其余模块的 {majority} 相反",
		"engine.decision.all_blocked":              "ALL 组合要求全部 {count} 个模块一致，但：{blockers}",
		"engine.decision.all_triggered":            "ALL 组合：全部 {count} 个模块都给出 {direction} 信号，平均置信度 {score}",
		"engine.decision.zero_total_weight":        "全部模块权重之和为 0，无法加权",
		"engine.decision.degraded_note":            "（{modules} 已降级，在分母中计为中性）",
		"engine.decision.weighted_below_threshold": "WEIGHTED 组合：加权净强度 {score}，未达到阈值 {threshold}{note}",
		"engine.decision.weighted_triggered":       "WEIGHTED 组合：加权净强度 {score}（{direction} 方向），已达到阈值 {threshold}{note}",
	})
}
