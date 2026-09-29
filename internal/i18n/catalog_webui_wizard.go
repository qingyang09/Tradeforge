package i18n

// Catalog entries for internal/webui/templates/wizard_*.html and
// batch_*.html, and the banner/error text handlers_wizard.go and
// handlers_batch.go build for them. Class A, same as
// catalog_webui_layout.go.
//
// The two flows (single-strategy wizard and multi-symbol batch scan) share
// most of their confirmation-screen chrome -- see wizard_confirm.html and
// batch_confirm.html, which are nearly identical templates -- so their keys
// live in one file and several are reused by both (webui.wizard.confirm.*,
// webui.wizard.clarify.*, webui.wizard.err.*).
//
// webui.wizard.err.* backs wizardErrorData.Message, which stays a plain
// string (not types.Message) rather than following the usual Class A/B
// split -- handlers_builder.go (the visual chart-builder page, owned by a
// different in-flight change) constructs wizardErrorData with bare Chinese
// literals too, and changing the field's type would break that file without
// touching it. i18n.T renders straight to a string at the call site instead,
// which is exactly the "one-off banner/error string" case its own doc
// comment describes.
func init() {
	register(LangEN, map[string]string{
		// ---- wizard_start.html ----
		"webui.wizard.start.title":                 "New Strategy",
		"webui.wizard.start.intro1":                "Describe the trading rule you want to execute in one sentence. The system's only job is to translate that sentence into a structured configuration and execute it faithfully -- it will never propose or modify the rule itself. Nothing is saved until you confirm it.",
		"webui.wizard.start.intro2_pre":            "This is a plain-text conversation. If you'd rather watch a real candlestick chart while dragging stop-loss/take-profit lines and module parameters, or run a backtest against real historical data at the same time, try ",
		"webui.wizard.start.intro2_link":           "Visual Builder",
		"webui.wizard.start.intro2_post":           " -- both paths end up producing the same kind of strategy configuration; use whichever feels more natural.",
		"webui.wizard.start.examples_hint":         "Click to fill the input below, then edit as needed:",
		"webui.wizard.start.chip1_label":           "Volume breakout long",
		"webui.wizard.start.chip1_example":         "BTCUSDT 1h, go long when volume breaks out to 2x the average and price reclaims resistance, stop-loss 2%, take-profit 5%",
		"webui.wizard.start.chip2_label":           "Support breakdown short",
		"webui.wizard.start.chip2_example":         "ETHUSDT 4h, go short when price breaks below support, stop-loss at the nearest resistance",
		"webui.wizard.start.chip3_label":           "Multi-timeframe combo",
		"webui.wizard.start.chip3_example":         "BTCUSDT 1h detects a false breakout of a consolidation range, 15m detects a high-volume drop, go short when both conditions are met, stop-loss 3%, take-profit 8%",
		"webui.wizard.start.agent_not_ready":       "The Agent translation layer isn't ready yet (usually ANTHROPIC_API_KEY isn't configured) -- the wizard is unavailable for now.",
		"webui.wizard.start.utterance_placeholder": "e.g.: BTCUSDT 1h, go long when volume breaks out to 2x the average and price reclaims resistance",
		"webui.wizard.start.submit":                "Translate",
		"webui.wizard.field.utterance":             "Trading rule",
		"webui.wizard.field.answer":                "Your answer",

		// ---- wizard_clarify.html / batch_clarify.html (shared) ----
		"webui.wizard.clarify.intro":  "A few more details are needed:",
		"webui.wizard.clarify.submit": "Continue",

		// ---- wizard_confirm.html / batch_confirm.html (shared rows) ----
		"webui.wizard.confirm.heading":            "Here's my understanding of your strategy",
		"webui.wizard.confirm.col_symbol":         "Symbol",
		"webui.wizard.confirm.col_timeframe":      "Timeframe",
		"webui.wizard.confirm.col_combine":        "Combination",
		"webui.wizard.confirm.threshold_suffix":   " (threshold {threshold})",
		"webui.wizard.confirm.modules_heading":    "Modules",
		"webui.wizard.confirm.col_module":         "Module",
		"webui.wizard.confirm.col_weight":         "Weight",
		"webui.wizard.confirm.col_params":         "Parameters",
		"webui.wizard.confirm.default_params":     "All parameters use system defaults",
		"webui.wizard.confirm.risk_heading":       "Risk Controls",
		"webui.wizard.confirm.row_max_position":   "Max position size per trade",
		"webui.wizard.confirm.row_max_daily_loss": "Max daily loss",
		"webui.wizard.confirm.row_stop_loss":      "Stop-loss",
		"webui.wizard.confirm.row_take_profit":    "Take-profit",
		"webui.wizard.confirm.row_max_holding":    "Max holding period",
		"webui.wizard.confirm.stop_loss_sr":       "breaking below the nearest support level as of entry",
		"webui.wizard.confirm.stop_loss_poc":      "reclaiming the volume point of control (POC) as of entry",
		"webui.wizard.confirm.take_profit_sr":     "touching the nearest resistance level as of entry",
		"webui.wizard.confirm.take_profit_poc":    "touching the volume point of control (POC) as of entry",
		"webui.wizard.confirm.note":               "Once confirmed, the configuration is stored in DRAFT state -- it must pass a backtest and then run in paper trading before it can go live.",
		"webui.wizard.confirm.submit":             "Looks right, save",
		"webui.wizard.confirm.cancel":             "Start over",

		// ---- batch_confirm.html (batch-only) ----
		"webui.batch.confirm.heading":         "Here's my understanding of your rule",
		"webui.batch.confirm.symbols_heading": "Will apply to the following {count} symbols",
		"webui.batch.confirm.symbols_note":    "Each symbol gets its own independent DRAFT strategy with exactly the same parameters as above -- they don't affect each other.",
		"webui.batch.confirm.note":            "Once confirmed, a DRAFT will be saved for each symbol above -- each one must pass a backtest and then run in paper trading before it can go live.",
		"webui.batch.confirm.submit":          "Looks right, save all",

		// ---- wizard_result.html ----
		"webui.wizard.result.saved":          "Saved. Strategy ID: {id} (current state: DRAFT). Next: run a backtest -- this strategy cannot go live until it passes both the backtest and paper trading.",
		"webui.wizard.result.view_detail":    "View strategy detail",
		"webui.wizard.result.create_another": "Create another",
		"webui.wizard.result.restart":        "Start over",
		"webui.wizard.result.cancelled":      "Cancelled -- the configuration was not written to the system.",
		"webui.wizard.result.confirm_failed": "Confirmation failed: {err}",
		"webui.wizard.result.save_failed":    "Failed to write to the database: {err}",

		// ---- batch_start.html ----
		"webui.batch.start.title":                 "Batch Scan",
		"webui.batch.start.intro1":                "The system ranks the top N symbols by 24-hour trading volume and applies the rule you describe to each one, unchanged -- identical parameters, only the symbol differs. Each symbol still has to pass its own backtest and paper trading, and you unlock it manually before it can actually go live; a strategy already in LIVE state still places orders automatically on its own signals, exactly like a single strategy does.",
		"webui.batch.start.intro2":                "Describe only the rule itself, without naming a specific symbol -- the symbols come from the scan results, and anything you write here for a symbol is ignored.",
		"webui.batch.start.agent_not_ready":       "The Agent translation layer isn't ready yet (usually ANTHROPIC_API_KEY isn't configured) -- batch scan is unavailable for now.",
		"webui.batch.start.count_label":           "How many symbols to scan (ranked by 24h volume, up to {max})",
		"webui.batch.start.utterance_placeholder": "e.g.: 1h, go long when volume breaks out to 2x the average and price reclaims resistance, stop-loss 2%, take-profit 5%",
		"webui.batch.start.submit":                "Scan and translate",

		// ---- batch_result.html ----
		"webui.batch.result.col_symbol":     "Symbol",
		"webui.batch.result.col_outcome":    "Result",
		"webui.batch.result.skipped_prefix": "Skipped: {reason}",
		"webui.batch.result.view_detail":    "View detail",
		"webui.batch.result.draft_suffix":   " (DRAFT)",
		"webui.batch.result.back_dashboard": "Back to dashboard",
		"webui.batch.result.scan_again":     "Scan again",
		"webui.batch.result.success":        "Created {created} drafts successfully, skipped {skipped}.",
		"webui.batch.result.cancelled":      "Cancelled -- no drafts were created.",

		// ---- errors shared by wizard + batch flows (wizardErrorData.Message,
		// rendered via i18n.T -- see package doc comment above) ----
		"webui.wizard.err.agent_not_ready":              "The Agent translation layer isn't ready yet -- please configure a model in Settings first.",
		"webui.wizard.err.empty_utterance":              "The trading rule cannot be empty.",
		"webui.wizard.err.translate_failed":             "Translation failed: {err}",
		"webui.wizard.err.session_expired":              "The session has expired, please start over.",
		"webui.wizard.err.empty_answer":                 "The answer cannot be empty.",
		"webui.wizard.err.too_many_clarifications":      "Too many rounds of clarification -- please describe the rule more completely and try again.",
		"webui.wizard.err.proposal_empty":               "the proposal is empty",
		"webui.wizard.err.proposal_needs_clarification": "this proposal is still waiting on clarification and cannot be confirmed directly",
		"webui.wizard.err.proposal_missing_config":      "the proposal has no configuration",
		"webui.wizard.err.validation_failed":            "validation failed on confirm: {err}",
		"webui.batch.err.invalid_count":                 "The scan count must be a positive integer.",
		"webui.batch.err.scan_failed":                   "Failed to scan market symbols: {err}",
		"webui.batch.err.scan_empty":                    "No symbols were found in the scan, please try again later.",
	})
	register(LangZH, map[string]string{
		// ---- wizard_start.html ----
		"webui.wizard.start.title":                 "新建策略",
		"webui.wizard.start.intro1":                "用一句话描述你想执行的交易规则。系统只负责把这句话翻译成结构化配置并忠实执行，不会对规则本身提出任何建议或修改意见——你确认之前，什么都不会被保存。",
		"webui.wizard.start.intro2_pre":            "这里是纯文字对话；如果想一边看真实K线图一边拖拽调整止损止盈线、模块参数，或者想同时用真实历史数据跑一次回测，可以试试 ",
		"webui.wizard.start.intro2_link":           "可视化建策",
		"webui.wizard.start.intro2_post":           "——两条路径最终生成的是同一种策略配置，殊途同归，挑你觉得顺手的用。",
		"webui.wizard.start.examples_hint":         "点一下直接填进下面的输入框，再按需要改：",
		"webui.wizard.start.chip1_label":           "放量突破做多",
		"webui.wizard.start.chip1_example":         "BTCUSDT 1 小时线，成交量突破 2 倍均量且价格站上阻力位时做多，止损 2%，止盈 5%",
		"webui.wizard.start.chip2_label":           "跌破支撑做空",
		"webui.wizard.start.chip2_example":         "ETHUSDT 4 小时线，跌破支撑位时做空，止损设在最近的阻力位",
		"webui.wizard.start.chip3_label":           "多周期组合",
		"webui.wizard.start.chip3_example":         "BTCUSDT 1 小时判断盘整区假突破，15 分钟判断放量下跌，两个条件都满足时做空，止损 3%，止盈 8%",
		"webui.wizard.start.agent_not_ready":       "Agent 翻译层未就绪（通常是没有配置 ANTHROPIC_API_KEY），向导暂不可用。",
		"webui.wizard.start.utterance_placeholder": "例如：BTCUSDT 1 小时线，成交量突破 2 倍均量且价格站上阻力位时做多",
		"webui.wizard.start.submit":                "翻译",
		"webui.wizard.field.utterance":             "交易规则",
		"webui.wizard.field.answer":                "你的回答",

		// ---- wizard_clarify.html / batch_clarify.html (shared) ----
		"webui.wizard.clarify.intro":  "还需要你补充几点信息：",
		"webui.wizard.clarify.submit": "继续",

		// ---- wizard_confirm.html / batch_confirm.html (shared rows) ----
		"webui.wizard.confirm.heading":            "我理解你的策略是这样",
		"webui.wizard.confirm.col_symbol":         "标的",
		"webui.wizard.confirm.col_timeframe":      "周期",
		"webui.wizard.confirm.col_combine":        "组合方式",
		"webui.wizard.confirm.threshold_suffix":   "（阈值 {threshold}）",
		"webui.wizard.confirm.modules_heading":    "模块",
		"webui.wizard.confirm.col_module":         "模块",
		"webui.wizard.confirm.col_weight":         "权重",
		"webui.wizard.confirm.col_params":         "参数",
		"webui.wizard.confirm.default_params":     "全部使用系统默认值",
		"webui.wizard.confirm.risk_heading":       "风控",
		"webui.wizard.confirm.row_max_position":   "单笔最大仓位",
		"webui.wizard.confirm.row_max_daily_loss": "单日最大亏损",
		"webui.wizard.confirm.row_stop_loss":      "止损",
		"webui.wizard.confirm.row_take_profit":    "止盈",
		"webui.wizard.confirm.row_max_holding":    "最长持仓",
		"webui.wizard.confirm.stop_loss_sr":       "跌破开仓时最近的支撑位",
		"webui.wizard.confirm.stop_loss_poc":      "收回开仓时的成交量分布重心（POC）",
		"webui.wizard.confirm.take_profit_sr":     "触及开仓时最近的阻力位",
		"webui.wizard.confirm.take_profit_poc":    "触及开仓时的成交量分布重心（POC）",
		"webui.wizard.confirm.note":               "确认后配置会以 DRAFT 状态存入系统，之后必须先过回测、再跑模拟盘，才能进入实盘。",
		"webui.wizard.confirm.submit":             "确认无误，保存",
		"webui.wizard.confirm.cancel":             "重来",

		// ---- batch_confirm.html (batch-only) ----
		"webui.batch.confirm.heading":         "我理解你的规则是这样",
		"webui.batch.confirm.symbols_heading": "将应用到以下 {count} 个标的",
		"webui.batch.confirm.symbols_note":    "每个标的会各自生成一份独立的 DRAFT 策略，参数跟上面完全一样，互不影响。",
		"webui.batch.confirm.note":            "确认后会为以上每个标的各存一份 DRAFT，之后必须先过回测、再跑模拟盘，才能进入实盘。",
		"webui.batch.confirm.submit":          "确认无误，批量保存",

		// ---- wizard_result.html ----
		"webui.wizard.result.saved":          "已保存，策略 ID：{id}（当前状态：DRAFT）。下一步：跑回测，未通过回测与模拟盘之前该策略无法进入实盘。",
		"webui.wizard.result.view_detail":    "查看策略详情",
		"webui.wizard.result.create_another": "再建一个",
		"webui.wizard.result.restart":        "重新开始",
		"webui.wizard.result.cancelled":      "已取消，配置未写入系统。",
		"webui.wizard.result.confirm_failed": "确认失败：{err}",
		"webui.wizard.result.save_failed":    "写入数据库失败：{err}",

		// ---- batch_start.html ----
		"webui.batch.start.title":                 "批量扫描建策",
		"webui.batch.start.intro1":                "系统会按 24 小时成交量选出排名靠前的 N 个标的，把你描述的这条规则原样套用到每一个——参数完全相同，只有标的不同。每个标的仍然要各自过回测、模拟盘，你手动解锁才会真正进入实盘；已经在实盘状态的策略触发信号就会自动下单，这点跟单个建策没有任何区别。",
		"webui.batch.start.intro2":                "只描述规则本身，不用提具体标的——标的由扫描结果决定，你在这里写的标的会被忽略。",
		"webui.batch.start.agent_not_ready":       "Agent 翻译层未就绪（通常是没有配置 ANTHROPIC_API_KEY），批量扫描暂不可用。",
		"webui.batch.start.count_label":           "扫描前多少个标的（按 24 小时成交量排名，最多 {max} 个）",
		"webui.batch.start.utterance_placeholder": "例如：1 小时线，成交量突破 2 倍均量且价格站上阻力位时做多，止损 2%，止盈 5%",
		"webui.batch.start.submit":                "扫描并翻译",

		// ---- batch_result.html ----
		"webui.batch.result.col_symbol":     "标的",
		"webui.batch.result.col_outcome":    "结果",
		"webui.batch.result.skipped_prefix": "跳过：{reason}",
		"webui.batch.result.view_detail":    "查看详情",
		"webui.batch.result.draft_suffix":   "（DRAFT）",
		"webui.batch.result.back_dashboard": "回看板",
		"webui.batch.result.scan_again":     "再扫描一次",
		"webui.batch.result.success":        "成功创建 {created} 份草稿，跳过 {skipped} 个。",
		"webui.batch.result.cancelled":      "已取消，没有任何草稿被创建。",

		// ---- errors shared by wizard + batch flows (wizardErrorData.Message,
		// rendered via i18n.T -- see package doc comment above) ----
		"webui.wizard.err.agent_not_ready":              "Agent 翻译层未就绪，请先在设置页面配置模型。",
		"webui.wizard.err.empty_utterance":              "交易规则不能为空。",
		"webui.wizard.err.translate_failed":             "翻译失败：{err}",
		"webui.wizard.err.session_expired":              "会话已失效，请重新开始。",
		"webui.wizard.err.empty_answer":                 "回答不能为空。",
		"webui.wizard.err.too_many_clarifications":      "澄清轮次过多，请把规则描述得更完整一些后重试。",
		"webui.wizard.err.proposal_empty":               "提案为空",
		"webui.wizard.err.proposal_needs_clarification": "该提案仍在等待用户澄清，不能直接确认",
		"webui.wizard.err.proposal_missing_config":      "提案中没有配置",
		"webui.wizard.err.validation_failed":            "确认时校验未通过：{err}",
		"webui.batch.err.invalid_count":                 "扫描数量必须是正整数",
		"webui.batch.err.scan_failed":                   "扫描市场标的失败：{err}",
		"webui.batch.err.scan_empty":                    "没有扫描到任何标的，请稍后重试。",
	})
}
