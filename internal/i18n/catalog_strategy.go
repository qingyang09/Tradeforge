package i18n

// Catalog entries for internal/strategy's state machine: transition
// rejections and gate-criterion labels/reasons.
func init() {
	register(LangEN, map[string]string{
		"strategy.transition.invalid_from_state":     "Source state {state} is invalid",
		"strategy.transition.invalid_to_state":       "Target state {state} is invalid",
		"strategy.transition.same_state":             "Source and target state are the same",
		"strategy.transition.reason_required":        "A transition reason is required; the audit log doesn't accept an empty one",
		"strategy.transition.illegal_path":           "Not a legal transition path; {from} can only advance to {allowed}",
		"strategy.transition.manual_only":            "Transition from {from} to {to}: must be a manual user action, the system may not advance this automatically",
		"strategy.transition.live_requires_actor_id": "Manually unlocking live trading must record who performed it",
		"strategy.transition.missing_backtest":       "Missing backtest result, can't determine whether it clears the gate",
		"strategy.transition.missing_paper_stats":    "Missing paper-trading statistics, can't determine whether it clears the gate",

		// Generic, reusable value formatters -- these carry no translatable
		// words of their own (just a number/percentage/comparison operator),
		// so the same template text works for every language.
		"strategy.gate.plain":        "{value}",
		"strategy.gate.gt":           "> {value}",
		"strategy.gate.gte_value":    ">= {value}",
		"strategy.gate.pct":          "{pct}%",
		"strategy.gate.lte_pct":      "<= {pct}%",
		"strategy.gate.count_trades": "{count} trades",
		"strategy.gate.min_trades":   ">= {count} trades",

		"strategy.gate.oos_trades.label":  "Out-of-sample trade count",
		"strategy.gate.oos_trades.reason": "Only {count} out-of-sample trades, below the required {min}; a sample this small has no statistical meaning, equivalent to unverified",

		"strategy.gate.oos_sharpe.label":  "Out-of-sample Sharpe ratio",
		"strategy.gate.oos_sharpe.reason": "Out-of-sample Sharpe {value} does not exceed the gate of {min}",

		"strategy.gate.oos_drawdown.label":  "Out-of-sample max drawdown",
		"strategy.gate.oos_drawdown.reason": "Out-of-sample max drawdown {pct}% exceeds the cap of {max}%",

		"strategy.gate.fee_drag.label":   "Fees as a share of gross profit",
		"strategy.gate.fee_drag.unknown": "— (no positive gross profit yet, can't compute a ratio)",
		"strategy.gate.fee_drag.reason":  "{pct}% of out-of-sample gross profit was eaten by fees, above the cap of {max}%; high turnover makes this strategy very sensitive to fees/slippage",

		"strategy.gate.paper_duration.label":  "Paper trading run duration",
		"strategy.gate.paper_duration.reason": "Paper trading has only run for {duration}, short of the required {min}",

		"strategy.gate.paper_trades.label":  "Paper trading fill count",
		"strategy.gate.paper_trades.reason": "Paper trading has only filled {count} trades, short of the required {min}",
	})

	register(LangZH, map[string]string{
		"strategy.transition.invalid_from_state":     "源状态 {state} 不合法",
		"strategy.transition.invalid_to_state":       "目标状态 {state} 不合法",
		"strategy.transition.same_state":             "源状态与目标状态相同",
		"strategy.transition.reason_required":        "必须说明推进理由，审计日志不接受空理由",
		"strategy.transition.illegal_path":           "不是合法的流转路径；{from} 只能推进到 {allowed}",
		"strategy.transition.manual_only":            "从 {from} 推进到 {to}：该流转必须由用户显式手动操作，系统不得自动推进",
		"strategy.transition.live_requires_actor_id": "手动解锁实盘必须记录操作者身份",
		"strategy.transition.missing_backtest":       "缺少回测结果，无法判断是否达标",
		"strategy.transition.missing_paper_stats":    "缺少模拟盘统计，无法判断是否达标",

		"strategy.gate.plain":        "{value}",
		"strategy.gate.gt":           "> {value}",
		"strategy.gate.gte_value":    ">= {value}",
		"strategy.gate.pct":          "{pct}%",
		"strategy.gate.lte_pct":      "<= {pct}%",
		"strategy.gate.count_trades": "{count} 笔",
		"strategy.gate.min_trades":   ">= {count} 笔",

		"strategy.gate.oos_trades.label":  "样本外交易笔数",
		"strategy.gate.oos_trades.reason": "样本外只有 {count} 笔交易，低于要求的 {min} 笔；样本太少时的指标没有统计意义，等同于未验证",

		"strategy.gate.oos_sharpe.label":  "样本外夏普比率",
		"strategy.gate.oos_sharpe.reason": "样本外夏普 {value} 未超过门槛 {min}",

		"strategy.gate.oos_drawdown.label":  "样本外最大回撤",
		"strategy.gate.oos_drawdown.reason": "样本外最大回撤 {pct}% 超过上限 {max}%",

		"strategy.gate.fee_drag.label":   "手续费占毛利润比例",
		"strategy.gate.fee_drag.unknown": "—（还没有正的毛利润，暂时算不出比例）",
		"strategy.gate.fee_drag.reason":  "样本外毛利润里有 {pct}% 被手续费吃掉，超过上限 {max}%；换手率过高，对手续费/滑点太敏感",

		"strategy.gate.paper_duration.label":  "模拟盘运行时长",
		"strategy.gate.paper_duration.reason": "模拟盘只运行了 {duration}，未达到要求的 {min}",

		"strategy.gate.paper_trades.label":  "模拟盘成交笔数",
		"strategy.gate.paper_trades.reason": "模拟盘只成交了 {count} 笔，未达到要求的 {min} 笔",
	})
}
