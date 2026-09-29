package i18n

// Catalog entries for the strategy detail page: internal/webui/templates/
// strategy_detail.html, its partials (decisions_table.html/orders_table.html
// -- only the parts not already converted in an earlier migration phase --
// backtest_summary.html, transitions_table.html, confirm_backtest_form.html,
// run_backtest_form.html, start_paper_trading_form.html,
// live_unlock_form.html), and the banners/gate text built in
// handlers_strategy.go, handlers_backtest.go, handlers_promote.go and
// handlers_live.go.
func init() {
	register(LangEN, map[string]string{
		"webui.strategy_detail.not_found": "Strategy not found",

		"webui.strategy_detail.header.threshold":        " (threshold {value})",
		"webui.strategy_detail.header.created_at":       "created {time}",
		"webui.strategy_detail.header.view_builder":     "View in builder",
		"webui.strategy_detail.header.source_utterance": "Original description: \"{text}\"",

		"webui.strategy_detail.nav.next_step":        "Next Step",
		"webui.strategy_detail.nav.modules":          "Modules & Risk",
		"webui.strategy_detail.nav.run_backtest":     "Run Backtest",
		"webui.strategy_detail.nav.backtest_results": "Backtest Results",
		"webui.strategy_detail.nav.paper_stats":      "Paper Trading Stats",
		"webui.strategy_detail.nav.orders":           "Orders",
		"webui.strategy_detail.nav.decisions":        "Decisions",
		"webui.strategy_detail.nav.transitions":      "State Transitions",

		"webui.strategy_detail.next_step.heading":        "What's left before \"{target}\"",
		"webui.strategy_detail.next_step.table.check":    "Check",
		"webui.strategy_detail.next_step.table.current":  "Current",
		"webui.strategy_detail.next_step.table.required": "Required",
		"webui.strategy_detail.next_step.table.pass":     "Pass",
		"webui.strategy_detail.next_step.pass_yes":       "✓ Pass",
		"webui.strategy_detail.next_step.pass_no":        "✗ Not yet",
		"webui.strategy_detail.next_step.all_pass":       "All checks pass -- ready to advance to the next step.",

		"webui.strategy_detail.modules.heading":        "Module Combination",
		"webui.strategy_detail.modules.table.module":   "Module",
		"webui.strategy_detail.modules.table.weight":   "Weight",
		"webui.strategy_detail.modules.table.params":   "Params",
		"webui.strategy_detail.modules.default_params": "default",

		"webui.strategy_detail.risk.heading":        "Risk Controls",
		"webui.strategy_detail.risk.max_position":   "Max Position Size per Trade",
		"webui.strategy_detail.risk.max_daily_loss": "Max Daily Loss",
		"webui.strategy_detail.risk.stop_loss":      "Stop Loss",
		"webui.strategy_detail.risk.take_profit":    "Take Profit",

		"webui.strategy_detail.run_backtest.heading":     "Run Backtest",
		"webui.strategy_detail.backtest_results.heading": "Backtest Results",

		"webui.strategy_detail.paper_stats.heading":     "Paper Trading Run Stats",
		"webui.strategy_detail.paper_stats.started_at":  "Started At",
		"webui.strategy_detail.paper_stats.duration":    "Running For",
		"webui.strategy_detail.paper_stats.trade_count": "Trade Count",

		"webui.strategy_detail.orders.heading":         "Order History",
		"webui.strategy_detail.orders.empty_suffix":    " (none yet)",
		"webui.strategy_detail.decisions.heading":      "Decision History",
		"webui.strategy_detail.decisions.empty_suffix": " (none yet)",
		"webui.strategy_detail.transitions.heading":    "State Transition History",

		"webui.strategy_detail.confirm_backtest.heading": "Confirm Backtest Result",
		"webui.strategy_detail.start_paper.heading":      "Start Paper Trading",
		"webui.strategy_detail.live_unlock.heading":      "Unlock Live Trading",

		"webui.strategy_detail.delete.heading": "Delete Strategy",
		"webui.strategy_detail.delete.warning": "Only a strategy in DRAFT state (no backtest/paper/live history yet) can be deleted. " +
			"Deleting it permanently removes its backtest results, orders, decisions and state-transition records too -- this cannot be undone.",
		"webui.strategy_detail.delete.confirm_js": "Are you sure you want to permanently delete \"{name}\"? This cannot be undone.",
		"webui.strategy_detail.delete.button":     "Permanently Delete This Strategy",

		"webui.strategy_detail.table.module":             "Module",
		"webui.strategy_detail.table.direction":          "Direction",
		"webui.strategy_detail.table.confidence":         "Confidence",
		"webui.strategy_detail.table.reason":             "Reason",
		"webui.strategy_detail.table.effective_params":   "Effective Params",
		"webui.strategy_detail.table.degraded_paren":     "(degraded)",
		"webui.strategy_detail.table.degraded_paren_err": "(degraded: {err})",

		"webui.strategy_detail.decisions_table.empty":     "No decisions recorded yet.",
		"webui.strategy_detail.decisions_table.time":      "Market Time",
		"webui.strategy_detail.decisions_table.score":     "Score",
		"webui.strategy_detail.decisions_table.triggered": "Triggered",
		"webui.strategy_detail.decisions_table.signals":   "Module Signals",
		"webui.strategy_detail.decisions_table.yes":       "Yes",
		"webui.strategy_detail.decisions_table.no":        "No",
		"webui.strategy_detail.decision_signals.count":    "{count} modules",

		"webui.strategy_detail.orders_table.empty":        "No orders yet.",
		"webui.strategy_detail.orders_table.time":         "Time",
		"webui.strategy_detail.orders_table.mode":         "Mode",
		"webui.strategy_detail.orders_table.status":       "Status",
		"webui.strategy_detail.orders_table.quantity":     "Quantity",
		"webui.strategy_detail.orders_table.filled_price": "Filled Price",
		"webui.strategy_detail.orders_table.fee":          "Fee",
		"webui.strategy_detail.orders_table.provenance":   "Trigger Basis",
		"webui.strategy_detail.orders_table.reject_paren": " ({reason})",

		"webui.strategy_detail.provenance.score": "score {value}",

		"webui.strategy_detail.transitions_table.empty":      "No transition records yet.",
		"webui.strategy_detail.transitions_table.time":       "Time",
		"webui.strategy_detail.transitions_table.transition": "Transition",
		"webui.strategy_detail.transitions_table.actor":      "Actor",
		"webui.strategy_detail.transitions_table.reason":     "Reason",

		"webui.strategy_detail.backtest.empty": "No backtest result yet.",
		"webui.strategy_detail.backtest.meta": "Data range {start} ~ {end}, engine version {version}, run at {ranAt}. " +
			"Taker fee {fee}, slippage {bps} bps.",
		"webui.strategy_detail.backtest.oos.heading":           "Out-of-Sample -- the Only Basis for Deciding Whether to Enter Paper Trading",
		"webui.strategy_detail.backtest.oos.no_trades_warning": "The out-of-sample segment produced no trades -- the metrics below are all zero and do not represent a validated result.",
		"webui.strategy_detail.backtest.in_sample.heading":     "In-Sample",
		"webui.strategy_detail.backtest.in_sample.note":        "If parameters were tuned, they were tuned on this segment's data -- it cannot be taken as expected performance.",
		"webui.strategy_detail.backtest.overall.heading":       "Full Range",

		"webui.strategy_detail.backtest.chart.heading_real":   "Equity / Drawdown Curve",
		"webui.strategy_detail.backtest.chart.heading_approx": "Cumulative P&L (approximate)",
		"webui.strategy_detail.backtest.chart.real_note": "Gray is the in-sample, blue is the out-of-sample real equity curve (includes unrealized P&L while a position is open, sampled candle by candle); " +
			"the dashed line is the running-high envelope, and the red shading is the drawdown range (how far equity sits below its running high). Range {min} ~ {max}.",
		"webui.strategy_detail.backtest.chart.approx_note": "This backtest predates the equity-curve persistence feature, so it falls back to an approximate drawing: gray is the in-sample, blue is the out-of-sample cumulative realized P&L " +
			"(per-trade P&L summed in entry-time order), which cannot show intra-trade unrealized drawdown. Range {min} ~ {max}.",
		"webui.strategy_detail.backtest.chart.no_trades": "No trade records, so no curve can be drawn.",

		"webui.strategy_detail.backtest.monthly_returns.heading": "Monthly Returns",
		"webui.strategy_detail.backtest.trades.heading":          "Trade-by-Trade",
		"webui.strategy_detail.backtest.trades.col_entry_time":   "Entry Time",
		"webui.strategy_detail.backtest.trades.col_exit_time":    "Exit Time",
		"webui.strategy_detail.backtest.trades.col_hold_time":    "Holding Time",
		"webui.strategy_detail.backtest.trades.col_direction":    "Direction",
		"webui.strategy_detail.backtest.trades.col_entry_price":  "Entry Price",
		"webui.strategy_detail.backtest.trades.col_exit_price":   "Exit Price",
		"webui.strategy_detail.backtest.trades.col_quantity":     "Quantity",
		"webui.strategy_detail.backtest.trades.col_pnl":          "P&L",
		"webui.strategy_detail.backtest.trades.col_fees":         "Fees",
		"webui.strategy_detail.backtest.trades.col_exit_reason":  "Exit Reason",
		"webui.strategy_detail.backtest.trades.col_segment":      "Segment",
		"webui.strategy_detail.backtest.trades.empty":            "No trade detail.",

		"webui.strategy_detail.backtest.metrics.col_total_return":  "Total Return",
		"webui.strategy_detail.backtest.metrics.col_annualized":    "Annualized",
		"webui.strategy_detail.backtest.metrics.col_sharpe":        "Sharpe",
		"webui.strategy_detail.backtest.metrics.col_sortino":       "Sortino",
		"webui.strategy_detail.backtest.metrics.col_max_drawdown":  "Max Drawdown",
		"webui.strategy_detail.backtest.metrics.col_win_rate":      "Win Rate",
		"webui.strategy_detail.backtest.metrics.col_profit_factor": "Profit Factor",
		"webui.strategy_detail.backtest.metrics.col_trade_count":   "Trade Count",
		"webui.strategy_detail.backtest.metrics.col_total_fees":    "Total Fees",
		"webui.strategy_detail.backtest.metrics.col_fee_drag":      "Fee / Gross Profit",

		"webui.strategy_detail.confirm_backtest.note": "Confirming re-checks whether the latest backtest's out-of-sample metrics clear the gate -- passing advances the strategy to BACKTESTED; " +
			"not passing tells you exactly what's short, as-is.",
		"webui.strategy_detail.form.actor_id_label": "Operator ID",
		"webui.strategy_detail.form.reason_label":   "Reason",

		"webui.strategy_detail.run_backtest.note": "Clicking this pulls real historical candles from OKX live, replays the signals locally, then hands them to the matching engine to compute the full result and write it to the database -- " +
			"no need to manually run the backtest-runner/tradeforge_backtest command-line tools. It may take anywhere from tens of seconds to a few minutes, depending on the lookback size and network.",
		"webui.strategy_detail.run_backtest.lookback_label":       "Lookback Candles",
		"webui.strategy_detail.run_backtest.lookback_placeholder": "default 1000",

		"webui.strategy_detail.start_paper.note": "Once in paper trading, the execution layer (cmd/executor) will load this strategy on its next start and run simulated trades against real-time market data -- no real orders are placed.",

		"webui.strategy_detail.live_unlock.warning": "Once unlocked, this strategy's triggered signals will send LIVE alerts (not paper-trading preview alerts) through whichever alert channels you've enabled. " +
			"This step alone does not place any orders automatically -- if you separately run cmd/executor -state LIVE with exchange credentials configured for this user, it will independently start placing real orders; " +
			"if it isn't running, you'll only get alerts, no orders. The system never does this step on its own -- it must be you who confirms it.",

		"webui.strategy_detail.gate.target.backtest":          "Out-of-sample backtest passing",
		"webui.strategy_detail.gate.note.no_backtest_yet":     "No backtest result yet -- run one above first.",
		"webui.strategy_detail.gate.target.paper":             "Paper trading run passing",
		"webui.strategy_detail.gate.note.no_paper_stats_yet":  "No paper trading stats yet -- once simulated trading starts, the system records the running time and trade count.",
		"webui.strategy_detail.gate.target.start_paper":       "Advance to paper trading",
		"webui.strategy_detail.gate.note.no_gate_start_paper": "This step has no additional data gate -- use the form below to continue.",
		"webui.strategy_detail.gate.target.live":              "Start placing real orders",
		"webui.strategy_detail.gate.note.no_gate_live":        "This step has no data gate -- you must confirm it manually below; the system will never advance it automatically.",

		"webui.strategy_detail.banner.fetch_candles_failed":         "Failed to fetch historical market data: {err}",
		"webui.strategy_detail.banner.replay_failed":                "Signal replay failed: {err}",
		"webui.strategy_detail.banner.tmpdir_failed":                "Failed to create temp directory: {err}",
		"webui.strategy_detail.banner.marshal_strategy_failed":      "Failed to serialize strategy config: {err}",
		"webui.strategy_detail.banner.write_strategy_failed":        "Failed to write strategy config: {err}",
		"webui.strategy_detail.banner.create_candles_file_failed":   "Failed to create candle file: {err}",
		"webui.strategy_detail.banner.write_candles_failed":         "Failed to write candle data: {err}",
		"webui.strategy_detail.banner.create_decisions_file_failed": "Failed to create decisions file: {err}",
		"webui.strategy_detail.banner.write_decisions_failed":       "Failed to write decision data: {err}",
		"webui.strategy_detail.banner.python_engine_failed":         "Backtest matching engine run failed: {msg}",
		"webui.strategy_detail.banner.run_backtest_success":         "Backtest complete: fetched {count} real {timeframe} historical candles, replayed {triggers} triggered signals; full matching result below.",

		"webui.strategy_detail.banner.no_backtest_result":       "No backtest result yet, so it can't be confirmed. Run backtest-runner + tradeforge_backtest first to write one.",
		"webui.strategy_detail.banner.read_backtest_failed":     "Failed to read backtest result: {err}",
		"webui.strategy_detail.banner.update_state_failed":      "Failed to advance state: {err}",
		"webui.strategy_detail.banner.confirm_backtest_success": "Backtest result confirmed; the strategy is now BACKTESTED.",
		"webui.strategy_detail.banner.start_paper_success":      "Now in paper trading. The next cmd/executor start will load this strategy.",
		"webui.strategy_detail.banner.delete_wrong_state":       "Only a strategy in DRAFT state can be deleted; the current state is {state}.",
		"webui.strategy_detail.banner.unlock_live_success":      "Live trading unlocked.",
		"webui.strategy_detail.banner.transition_rejected":      "Transition rejected: {err}",
	})
	register(LangZH, map[string]string{
		"webui.strategy_detail.not_found": "策略不存在",

		"webui.strategy_detail.header.threshold":        "（阈值 {value}）",
		"webui.strategy_detail.header.created_at":       "创建于 {time}",
		"webui.strategy_detail.header.view_builder":     "去画板查看",
		"webui.strategy_detail.header.source_utterance": "原始描述：\"{text}\"",

		"webui.strategy_detail.nav.next_step":        "下一步",
		"webui.strategy_detail.nav.modules":          "模块/风控",
		"webui.strategy_detail.nav.run_backtest":     "运行回测",
		"webui.strategy_detail.nav.backtest_results": "回测结果",
		"webui.strategy_detail.nav.paper_stats":      "模拟盘统计",
		"webui.strategy_detail.nav.orders":           "订单",
		"webui.strategy_detail.nav.decisions":        "决策",
		"webui.strategy_detail.nav.transitions":      "状态流转",

		"webui.strategy_detail.next_step.heading":        "距离\"{target}\"还差什么",
		"webui.strategy_detail.next_step.table.check":    "检查项",
		"webui.strategy_detail.next_step.table.current":  "当前值",
		"webui.strategy_detail.next_step.table.required": "门槛要求",
		"webui.strategy_detail.next_step.table.pass":     "是否达标",
		"webui.strategy_detail.next_step.pass_yes":       "✓ 达标",
		"webui.strategy_detail.next_step.pass_no":        "✗ 未达标",
		"webui.strategy_detail.next_step.all_pass":       "全部达标，可以推进到下一步了。",

		"webui.strategy_detail.modules.heading":        "模块组合",
		"webui.strategy_detail.modules.table.module":   "模块",
		"webui.strategy_detail.modules.table.weight":   "权重",
		"webui.strategy_detail.modules.table.params":   "参数",
		"webui.strategy_detail.modules.default_params": "默认值",

		"webui.strategy_detail.risk.heading":        "风控",
		"webui.strategy_detail.risk.max_position":   "单笔最大仓位",
		"webui.strategy_detail.risk.max_daily_loss": "单日最大亏损",
		"webui.strategy_detail.risk.stop_loss":      "止损",
		"webui.strategy_detail.risk.take_profit":    "止盈",

		"webui.strategy_detail.run_backtest.heading":     "运行回测",
		"webui.strategy_detail.backtest_results.heading": "回测结果",

		"webui.strategy_detail.paper_stats.heading":     "模拟盘运行统计",
		"webui.strategy_detail.paper_stats.started_at":  "开始时间",
		"webui.strategy_detail.paper_stats.duration":    "已运行",
		"webui.strategy_detail.paper_stats.trade_count": "成交笔数",

		"webui.strategy_detail.orders.heading":         "订单记录",
		"webui.strategy_detail.orders.empty_suffix":    "（暂无）",
		"webui.strategy_detail.decisions.heading":      "决策记录",
		"webui.strategy_detail.decisions.empty_suffix": "（暂无）",
		"webui.strategy_detail.transitions.heading":    "状态流转历史",

		"webui.strategy_detail.confirm_backtest.heading": "确认回测结果",
		"webui.strategy_detail.start_paper.heading":      "进入模拟盘",
		"webui.strategy_detail.live_unlock.heading":      "解锁实盘",

		"webui.strategy_detail.delete.heading": "删除策略",
		"webui.strategy_detail.delete.warning": "只能删除 DRAFT 状态的策略（还没有回测/模拟盘/实盘历史）。删除后关联的回测结果、" +
			"订单、决策、状态流转记录会一并永久删除，无法恢复。",
		"webui.strategy_detail.delete.confirm_js": "确定要永久删除《{name}》吗？此操作不可撤销。",
		"webui.strategy_detail.delete.button":     "永久删除这条策略",

		"webui.strategy_detail.table.module":             "模块",
		"webui.strategy_detail.table.direction":          "方向",
		"webui.strategy_detail.table.confidence":         "置信度",
		"webui.strategy_detail.table.reason":             "原因",
		"webui.strategy_detail.table.effective_params":   "生效参数",
		"webui.strategy_detail.table.degraded_paren":     "（降级）",
		"webui.strategy_detail.table.degraded_paren_err": "（降级：{err}）",

		"webui.strategy_detail.decisions_table.empty":     "暂无决策记录。",
		"webui.strategy_detail.decisions_table.time":      "行情时间",
		"webui.strategy_detail.decisions_table.score":     "强度",
		"webui.strategy_detail.decisions_table.triggered": "是否触发",
		"webui.strategy_detail.decisions_table.signals":   "各模块信号",
		"webui.strategy_detail.decisions_table.yes":       "是",
		"webui.strategy_detail.decisions_table.no":        "否",
		"webui.strategy_detail.decision_signals.count":    "{count} 个模块",

		"webui.strategy_detail.orders_table.empty":        "暂无订单。",
		"webui.strategy_detail.orders_table.time":         "时间",
		"webui.strategy_detail.orders_table.mode":         "模式",
		"webui.strategy_detail.orders_table.status":       "状态",
		"webui.strategy_detail.orders_table.quantity":     "数量",
		"webui.strategy_detail.orders_table.filled_price": "成交价",
		"webui.strategy_detail.orders_table.fee":          "手续费",
		"webui.strategy_detail.orders_table.provenance":   "触发依据",
		"webui.strategy_detail.orders_table.reject_paren": "（{reason}）",

		"webui.strategy_detail.provenance.score": "强度 {value}",

		"webui.strategy_detail.transitions_table.empty":      "暂无流转记录。",
		"webui.strategy_detail.transitions_table.time":       "时间",
		"webui.strategy_detail.transitions_table.transition": "流转",
		"webui.strategy_detail.transitions_table.actor":      "操作者",
		"webui.strategy_detail.transitions_table.reason":     "理由",

		"webui.strategy_detail.backtest.empty": "尚无回测结果。",
		"webui.strategy_detail.backtest.meta": "数据区间 {start} ~ {end}，引擎版本 {version}，运行于 {ranAt}。" +
			"手续费 taker {fee}，滑点 {bps} bps。",
		"webui.strategy_detail.backtest.oos.heading":           "样本外——判断能否进入模拟盘的唯一依据",
		"webui.strategy_detail.backtest.oos.no_trades_warning": "样本外区间没有产生任何交易，以下指标均为零值，不代表已验证。",
		"webui.strategy_detail.backtest.in_sample.heading":     "样本内",
		"webui.strategy_detail.backtest.in_sample.note":        "参数如经过调整，是在这段数据上调的，不能作为预期表现。",
		"webui.strategy_detail.backtest.overall.heading":       "全区间",

		"webui.strategy_detail.backtest.chart.heading_real":   "权益 / 回撤曲线",
		"webui.strategy_detail.backtest.chart.heading_approx": "累计盈亏（近似）",
		"webui.strategy_detail.backtest.chart.real_note": "灰色为样本内、蓝色为样本外的真实权益曲线（含持仓浮动盈亏，逐根K线采样）；" +
			"虚线是历史新高包络线，红色阴影是回撤区间（权益比历史新高低多少）。区间 {min} ~ {max}。",
		"webui.strategy_detail.backtest.chart.approx_note": "本次回测早于权益曲线持久化上线，只能退回近似画法：灰色为样本内、蓝色为样本外的" +
			"累计已实现盈亏（按开仓时间排序的逐笔 PnL 累加），看不出持仓中途的浮动回撤。区间 {min} ~ {max}。",
		"webui.strategy_detail.backtest.chart.no_trades": "没有交易记录，无法绘制曲线。",

		"webui.strategy_detail.backtest.monthly_returns.heading": "月度收益",
		"webui.strategy_detail.backtest.trades.heading":          "逐笔交易",
		"webui.strategy_detail.backtest.trades.col_entry_time":   "开仓时间",
		"webui.strategy_detail.backtest.trades.col_exit_time":    "平仓时间",
		"webui.strategy_detail.backtest.trades.col_hold_time":    "持仓时长",
		"webui.strategy_detail.backtest.trades.col_direction":    "方向",
		"webui.strategy_detail.backtest.trades.col_entry_price":  "开仓价",
		"webui.strategy_detail.backtest.trades.col_exit_price":   "平仓价",
		"webui.strategy_detail.backtest.trades.col_quantity":     "数量",
		"webui.strategy_detail.backtest.trades.col_pnl":          "盈亏",
		"webui.strategy_detail.backtest.trades.col_fees":         "手续费",
		"webui.strategy_detail.backtest.trades.col_exit_reason":  "平仓原因",
		"webui.strategy_detail.backtest.trades.col_segment":      "区间",
		"webui.strategy_detail.backtest.trades.empty":            "没有交易明细。",

		"webui.strategy_detail.backtest.metrics.col_total_return":  "总收益",
		"webui.strategy_detail.backtest.metrics.col_annualized":    "年化",
		"webui.strategy_detail.backtest.metrics.col_sharpe":        "夏普",
		"webui.strategy_detail.backtest.metrics.col_sortino":       "索提诺",
		"webui.strategy_detail.backtest.metrics.col_max_drawdown":  "最大回撤",
		"webui.strategy_detail.backtest.metrics.col_win_rate":      "胜率",
		"webui.strategy_detail.backtest.metrics.col_profit_factor": "盈亏比",
		"webui.strategy_detail.backtest.metrics.col_trade_count":   "交易笔数",
		"webui.strategy_detail.backtest.metrics.col_total_fees":    "总手续费",
		"webui.strategy_detail.backtest.metrics.col_fee_drag":      "手续费/毛利润",

		"webui.strategy_detail.confirm_backtest.note": "确认后会重新核对最新一次回测的样本外指标是否达标，达标才会推进到 BACKTESTED；" +
			"不达标会原样告诉你差在哪。",
		"webui.strategy_detail.form.actor_id_label": "操作者标识",
		"webui.strategy_detail.form.reason_label":   "理由",

		"webui.strategy_detail.run_backtest.note": "点击后会实时向 OKX 拉取真实历史K线、在本机重放一遍信号、再交给撮合引擎算出完整" +
			"结果并写库——不需要手动跑 backtest-runner/tradeforge_backtest 这些命令行工具。" +
			"运行期间可能需要几十秒到几分钟，取决于回看根数和网络。",
		"webui.strategy_detail.run_backtest.lookback_label":       "回看根数",
		"webui.strategy_detail.run_backtest.lookback_placeholder": "默认 1000",

		"webui.strategy_detail.start_paper.note": "进入模拟盘后，执行层（cmd/executor）下次启动会加载这个策略，用实时行情跑模拟交易，不下真实单。",

		"webui.strategy_detail.live_unlock.warning": "解锁后，该策略触发信号时会通过你已开启的提醒渠道发送实盘提醒（而不是模拟盘" +
			"预览提醒）。这一步本身不会自动下单——如果你另外运行了 cmd/executor -state LIVE 并为" +
			"这个用户配置了交易所凭据，它会独立地开始下真实单；如果没有运行，就只有提醒，没有下单。" +
			"这一步系统不会自动做，必须由你确认。",

		"webui.strategy_detail.gate.target.backtest":          "样本外回测达标",
		"webui.strategy_detail.gate.note.no_backtest_yet":     "还没有回测结果，先在上面运行一次回测。",
		"webui.strategy_detail.gate.target.paper":             "模拟盘运行达标",
		"webui.strategy_detail.gate.note.no_paper_stats_yet":  "还没有模拟盘运行统计——开始跑模拟交易后系统会记录运行时长和成交笔数。",
		"webui.strategy_detail.gate.target.start_paper":       "推进到模拟交易阶段",
		"webui.strategy_detail.gate.note.no_gate_start_paper": "这一步没有额外的数据门槛，用下方表单即可继续。",
		"webui.strategy_detail.gate.target.live":              "开始真实下单",
		"webui.strategy_detail.gate.note.no_gate_live":        "这一步没有数据门槛——必须由你在下方手动确认，系统不会自动推进。",

		"webui.strategy_detail.banner.fetch_candles_failed":         "拉取历史行情失败：{err}",
		"webui.strategy_detail.banner.replay_failed":                "信号重放失败：{err}",
		"webui.strategy_detail.banner.tmpdir_failed":                "创建临时目录失败：{err}",
		"webui.strategy_detail.banner.marshal_strategy_failed":      "序列化策略配置失败：{err}",
		"webui.strategy_detail.banner.write_strategy_failed":        "写入策略配置失败：{err}",
		"webui.strategy_detail.banner.create_candles_file_failed":   "创建K线文件失败：{err}",
		"webui.strategy_detail.banner.write_candles_failed":         "写入K线数据失败：{err}",
		"webui.strategy_detail.banner.create_decisions_file_failed": "创建决策文件失败：{err}",
		"webui.strategy_detail.banner.write_decisions_failed":       "写入决策数据失败：{err}",
		"webui.strategy_detail.banner.python_engine_failed":         "回测撮合引擎运行失败：{msg}",
		"webui.strategy_detail.banner.run_backtest_success":         "回测已完成：拉取了 {count} 根 {timeframe} 周期真实历史K线，重放出 {triggers} 次触发信号，完整撮合结果见下方。",

		"webui.strategy_detail.banner.no_backtest_result":       "尚无回测结果，无法确认。请先跑一遍 backtest-runner + tradeforge_backtest 写入结果。",
		"webui.strategy_detail.banner.read_backtest_failed":     "读取回测结果失败：{err}",
		"webui.strategy_detail.banner.update_state_failed":      "推进失败：{err}",
		"webui.strategy_detail.banner.confirm_backtest_success": "回测结果已确认，策略进入 BACKTESTED。",
		"webui.strategy_detail.banner.start_paper_success":      "已进入模拟盘。下一次启动 cmd/executor 会加载这个策略。",
		"webui.strategy_detail.banner.delete_wrong_state":       "只能删除 DRAFT 状态的策略，当前状态是 {state}。",
		"webui.strategy_detail.banner.unlock_live_success":      "已解锁实盘。",
		"webui.strategy_detail.banner.transition_rejected":      "流转被拒绝：{err}",
	})
}
