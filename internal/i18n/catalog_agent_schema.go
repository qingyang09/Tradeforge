package i18n

// Catalog entries for internal/agent/schema.go's JSON-Schema "description"
// fields -- the hints sent to the LLM alongside the schema itself, steering
// what it puts in each field. These are Class A: resolved fresh at the
// point BuildSchema is called, using the caller's requested language, so
// the LLM is steered in whichever language it's expected to answer in.
func init() {
	register(LangEN, map[string]string{
		"agent.schema.root.outcome":                 "config means the user's description was clear enough and has been translated into a config; clarification_needed means the description is ambiguous and the user must be asked first.",
		"agent.schema.root.restatement":             "Restate your understanding of the user's rule in plain language, for the user to confirm. Describe only the rule itself -- never any judgment of whether the strategy is good or bad, and never investment advice. When outcome is clarification_needed, write the part you're already sure of here.",
		"agent.schema.root.questions":               "The list of questions the user needs to clarify. Must be an empty array when outcome is config; must have at least one question when outcome is clarification_needed.",
		"agent.schema.root.config":                  "The translated strategy config. Null when outcome is clarification_needed.",
		"agent.schema.strategy.name":                "The strategy's name -- a short summary of this rule.",
		"agent.schema.strategy.symbol":              "The trading symbol, an exchange-format uppercase ticker such as BTCUSDT or ETHUSDT.",
		"agent.schema.strategy.timeframe":           "The strategy's trigger timeframe: a decision is computed once at the close of each candle on this timeframe. It must be the fastest of every timeframe any module uses (some modules may declare a slower timeframe via their own timeframe field).",
		"agent.schema.strategy.combine":             "How the modules' signals are combined. ALL: triggers only when every module agrees on direction; WEIGHTED: a weighted score, triggers once it exceeds the threshold.",
		"agent.schema.strategy.threshold":           "The trigger threshold for WEIGHTED combination, in (0, 1]. Omit for ALL combination.",
		"agent.schema.strategy.modules":             "The modules participating in this strategy. Only modules from the list below may be chosen, and each module may appear at most once.",
		"agent.schema.module.weight":                "This module's weight in WEIGHTED combination, in (0, 1]. Omit for ALL combination.",
		"agent.schema.module.timeframe":             "The timeframe this module uses; if omitted, it follows the strategy's top-level timeframe (the trigger timeframe). If set, it must not be faster than the top-level timeframe -- e.g. if the user describes \"use the 1-hour chart for context and the 15-minute chart for entry timing,\" the context module's timeframe is \"1h\" and the top-level timeframe is \"15m\".",
		"agent.schema.module.params":                "The module's parameters. Omit any parameter the user didn't explicitly specify -- the system fills in its default. Never invent a value on your own.",
		"agent.schema.describe.restatement":         "Restate the structured config given below in plain language, for the user to confirm. Describe only what's already in the config -- never add, drop, or reinterpret anything, and never include any judgment or investment advice.",
		"agent.schema.risk.root":                    "This symbol's risk-control parameters. Omit anything the user didn't mention.",
		"agent.schema.risk.max_position_size_quote": "The hard cap on a single position's notional amount, as a quote-currency (e.g. USDT) amount string such as \"1000\". A string, not a number, to avoid floating-point precision issues. Required regardless of position_sizing_mode: in fixed_quote mode, fill \"1000\" when the user didn't say; in risk_pct mode, if the user didn't separately state a specific cap amount, default it to the value of account_equity_quote itself (the loosest possible fallback, equivalent to \"a single trade may use at most the entire account\"), and state in the restatement that this is a system default, not something the user explicitly asked for.",
		"agent.schema.risk.max_daily_loss_quote":    "The maximum daily loss amount, as a string. Omit if the user didn't say.",
		"agent.schema.risk.max_holding_period":      "The maximum holding period, e.g. \"4h\" or \"90m\". Omit if the user didn't say.",
		"agent.schema.risk.stop_loss_mode":          "How the stop-loss is computed: \"pct\" (default, a fixed percentage, see stop_loss_pct), \"support_resistance\" (use the nearest support/resistance level the support_resistance module detects at the moment of opening as the stop-loss price -- that module must then be included in modules), or \"poc\" (use the volume-profile point of control the poc module computes as the stop-loss price -- the poc module must then be included in modules). Do not also fill stop_loss_pct in a non-pct mode. Omit if the user didn't say, which is equivalent to \"pct\".",
		"agent.schema.risk.stop_loss_pct":           "The stop-loss percentage, 0.02 meaning 2%. Only fill when stop_loss_mode is pct. Omit if the user didn't say.",
		"agent.schema.risk.take_profit_mode":        "How the take-profit is computed, using the same values as stop_loss_mode (support_resistance mode uses the nearest resistance level as the take-profit price; poc mode uses the volume-profile point of control). Independent of stop_loss_mode -- one may use a support/resistance level or POC while the other uses a fixed percentage. Omit if the user didn't say, which is equivalent to \"pct\".",
		"agent.schema.risk.take_profit_pct":         "The take-profit percentage, 0.05 meaning 5%. Only fill when take_profit_mode is pct. Omit if the user didn't say.",
		"agent.schema.risk.position_sizing_mode":    "How a single position's notional amount is computed: \"fixed_quote\" (default, a fixed amount, see max_position_size_quote), or \"risk_pct\" (computed dynamically as account equity × per-trade risk percentage ÷ stop-loss distance percentage -- the position size varies with each trade's stop-loss distance). risk_pct mode requires both account_equity_quote and risk_per_trade_pct to be filled, and the strategy must have an actually-effective stop-loss configured (stop_loss_pct greater than 0, or stop_loss_mode set to support_resistance / poc) -- without a stop-loss there is no stop-loss distance to compute a position size from. max_position_size_quote still applies in every mode as the hard cap on the computed position: a trade exceeding it is rejected outright, never silently shrunk. Omit if the user didn't say, which is equivalent to \"fixed_quote\".",
		"agent.schema.risk.account_equity_quote":    "The user's self-reported account equity, as a quote-currency (e.g. USDT) amount string. This is a static number the user states, not a balance read live from the exchange -- it is never auto-updated at runtime. Only needed when position_sizing_mode is risk_pct. Never invent a number when the user hasn't stated their account equity/capital -- this must go through clarification_needed instead, the same ironclad rule as never inventing stop-loss/take-profit numbers.",
		"agent.schema.risk.risk_per_trade_pct":      "The percentage of account equity the user is willing to risk per trade, 0.01 meaning at most 1% of account equity per trade (assuming the stop-loss is hit). Only needed when position_sizing_mode is risk_pct.",
		"agent.schema.catalog.header":               "The platform currently offers the following {count} signal modules; you may only choose among them:\n",
		"agent.schema.catalog.module":               "\nModule: {name}\n  Purpose: {description}\n  Parameters:\n",
		"agent.schema.catalog.param":                "    - {name} ({type}, default {default}, allowed range {range}): {description}\n",
	})
	register(LangZH, map[string]string{
		"agent.schema.root.outcome":                 "config 表示用户的描述足够明确、已翻译成配置；clarification_needed 表示描述存在歧义，必须先向用户提问。",
		"agent.schema.root.restatement":             "用大白话复述你对用户规则的理解，供用户确认。只描述规则本身，不得包含任何对策略优劣的评价或投资建议。outcome 为 clarification_needed 时，这里写你目前能确定的部分。",
		"agent.schema.root.questions":               "需要用户澄清的问题列表。outcome 为 config 时必须为空数组；为 clarification_needed 时必须至少有一个问题。",
		"agent.schema.root.config":                  "翻译出的策略配置。outcome 为 clarification_needed 时为 null。",
		"agent.schema.strategy.name":                "策略名称，用简短的中文概括这条规则。",
		"agent.schema.strategy.symbol":              "交易标的，交易所格式的大写符号，如 BTCUSDT、ETHUSDT。",
		"agent.schema.strategy.timeframe":           "策略的触发周期：决策在这个周期收盘时计算一次，必须是所有模块所用周期中最快的那个（如果某些模块通过各自的 timeframe 字段声明了更慢的周期）。",
		"agent.schema.strategy.combine":             "模块信号的组合方式。ALL：所有模块同向才触发；WEIGHTED：按权重加权，超过阈值才触发。",
		"agent.schema.strategy.threshold":           "WEIGHTED 组合的触发阈值，取值 (0, 1]。ALL 组合时省略。",
		"agent.schema.strategy.modules":             "参与该策略的模块。只能从下面列出的模块中选择，每个模块最多出现一次。",
		"agent.schema.module.weight":                "该模块在 WEIGHTED 组合中的权重，取值 (0, 1]。ALL 组合时省略。",
		"agent.schema.module.timeframe":             "该模块使用的周期；不填则跟随策略顶层 timeframe（触发周期）。填了就不能比顶层 timeframe 更快——比如用户描述'1 小时判断背景、15 分钟判断入场'，背景模块填 \"1h\"，顶层 timeframe 填 \"15m\"。",
		"agent.schema.module.params":                "模块参数。用户没有明确指定的参数请直接省略，系统会填入默认值；不要自行编造数值。",
		"agent.schema.describe.restatement":         "用大白话复述下面给出的结构化配置，供用户确认。只能描述配置里已有的内容，不得增删或重新解读，不得包含任何评价性或投资建议措辞。",
		"agent.schema.risk.root":                    "该标的的风控参数。用户没提到的项请省略。",
		"agent.schema.risk.max_position_size_quote": "单笔仓位的硬上限，以计价货币（如 USDT）计的金额字符串，例如 \"1000\"。用字符串而非数字，避免浮点精度问题。不管 position_sizing_mode 是哪种都必须填：fixed_quote 模式下用户没说时填 \"1000\"；risk_pct 模式下如果用户没有额外说一个具体上限金额，默认填 account_equity_quote 的值本身（相当于'单笔最多不超过全部本金'这个最宽松的兜底），并在 restatement 里说明这是系统默认的上限、不是用户主动要求的数字。",
		"agent.schema.risk.max_daily_loss_quote":    "单日最大亏损金额字符串。用户没说时省略。",
		"agent.schema.risk.max_holding_period":      "最大持仓时间，如 \"4h\"、\"90m\"。用户没说时省略。",
		"agent.schema.risk.stop_loss_mode":          "止损怎么算：\"pct\"（默认，固定百分比，见 stop_loss_pct）、\"support_resistance\"（用 support_resistance 模块在开仓那一刻检测到的最近支撑/阻力位当止损价，此时 modules 里必须包含该模块）、或 \"poc\"（用 poc 模块算出的成交量分布重心当止损价，modules 里必须包含 poc 模块）。非 pct 模式下都不应再填 stop_loss_pct。用户没说时省略，等同 \"pct\"。",
		"agent.schema.risk.stop_loss_pct":           "止损比例，0.02 表示 2%。仅 stop_loss_mode 为 pct 时填写。用户没说时省略。",
		"agent.schema.risk.take_profit_mode":        "止盈怎么算，取值与 stop_loss_mode 相同（support_resistance 模式下取最近阻力位当止盈价，poc 模式下取成交量分布重心）。跟 stop_loss_mode 相互独立，可以一个用支撑/阻力位或 POC、另一个用固定百分比。用户没说时省略，等同 \"pct\"。",
		"agent.schema.risk.take_profit_pct":         "止盈比例，0.05 表示 5%。仅 take_profit_mode 为 pct 时填写。用户没说时省略。",
		"agent.schema.risk.position_sizing_mode":    "单笔开仓名义金额怎么算：\"fixed_quote\"（默认，固定金额，见 max_position_size_quote）、或 \"risk_pct\"（按 账户权益 × 单笔风险比例 ÷ 止损距离百分比 动态计算，仓位会随每次的止损距离变化）。risk_pct 模式下必须同时填 account_equity_quote、risk_per_trade_pct，且策略必须配置了真的会生效的止损（stop_loss_pct 大于 0，或 stop_loss_mode 用 support_resistance / poc）——没有止损就算不出止损距离，也算不出仓位。max_position_size_quote 在任何模式下都仍然生效，是算出来的仓位的硬上限：超过它这笔交易会被直接拒绝，不会被静默缩小。用户没说时省略，等同 \"fixed_quote\"。",
		"agent.schema.risk.account_equity_quote":    "用户自报的账户权益，以计价货币（如 USDT）计的金额字符串。这是一个用户声明的静态数字，不是平台实时读取的交易所余额，运行期间不会自动更新。仅 position_sizing_mode 为 risk_pct 时需要。用户没有明确说过自己的账户权益/本金是多少时，绝不能凭空填一个数字——必须走 clarification_needed 向用户询问，跟止损止盈从不编造数字是同一条铁律。",
		"agent.schema.risk.risk_per_trade_pct":      "单笔愿意承担的账户权益风险比例，0.01 表示每笔最多亏账户权益的 1%（触发止损的假设下）。仅 position_sizing_mode 为 risk_pct 时需要。",
		"agent.schema.catalog.header":               "平台当前提供以下 {count} 个信号模块，你只能从中选择：\n",
		"agent.schema.catalog.module":               "\n模块名：{name}\n  作用：{description}\n  参数：\n",
		"agent.schema.catalog.param":                "    - {name}（{type}，默认 {default}，允许范围 {range}）：{description}\n",
	})
}
