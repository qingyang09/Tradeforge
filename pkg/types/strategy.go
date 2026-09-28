package types

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// ModuleConfig 是策略中对某一个信号模块的一次引用：用哪个模块、传什么参数、占多少权重。
type ModuleConfig struct {
	// Module 必须是已在模块注册表中登记的名字。Agent 不得发明不存在的模块。
	Module string `json:"module"`
	// Params 是模块参数，键必须落在该模块 RequiredParams() 的范围内。
	Params map[string]any `json:"params"`
	// Weight 仅在 CombineWeighted 下有意义，取值 (0, 1]，同一策略内不要求归一化。
	Weight float64 `json:"weight,omitempty"`
	// Timeframe 是该模块使用的 K 线周期，留空表示跟随 StrategyConfig.Timeframe
	// （触发周期）。填了就不能比触发周期更快——决策只在触发周期收盘时算一次，
	// 更快的模块周期永远来不及被看到。多个模块可以各自使用不同的（比触发周期慢的）
	// 周期，比如用 1 小时的关键位判断背景、15 分钟的放量判断入场触发。
	Timeframe Timeframe `json:"timeframe,omitempty"`
}

// CombineMode 是模块信号的聚合方式。
type CombineMode string

const (
	// CombineAll 要求所有模块给出同方向信号才触发。
	CombineAll CombineMode = "ALL"
	// CombineWeighted 按权重加权置信度，超过阈值才触发。
	CombineWeighted CombineMode = "WEIGHTED"
)

// Valid 报告聚合方式是否受支持。
func (c CombineMode) Valid() bool {
	return c == CombineAll || c == CombineWeighted
}

// RiskLevelMode 决定止损/止盈的价格怎么算。
type RiskLevelMode string

const (
	// RiskLevelModePct 是默认模式：按固定百分比计算（见 StopLossPct/TakeProfitPct）。
	RiskLevelModePct RiskLevelMode = "pct"
	// RiskLevelModeSupportResistance 用 support_resistance 模块在开仓那一刻检测到的
	// 最近支撑/阻力位作为止损/止盈的价格阈值，而不是固定百分比。多头止损取支撑位、
	// 止盈取阻力位；空头相反。这个价格只在开仓时算一次并锁定，此后不随行情重新计算——
	// 跟固定百分比止损的语义一致（都是开仓瞬间定死一个阈值），不是逐根跟随的移动止损。
	RiskLevelModeSupportResistance RiskLevelMode = "support_resistance"
	// RiskLevelModePOC 用 poc 模块在开仓那一刻算出的成交量分布重心（Point of Control）
	// 作为止损/止盈的价格阈值。POC 只有一个价格，不像支撑/阻力位分上下两个——不区分
	// 多空、不区分止损止盈方向，统一取同一个值。
	RiskLevelModePOC RiskLevelMode = "poc"
)

// Valid 报告是否为已支持的止损/止盈模式。空字符串视为 RiskLevelModePct（默认）。
func (m RiskLevelMode) Valid() bool {
	return m == "" || m == RiskLevelModePct || m == RiskLevelModeSupportResistance || m == RiskLevelModePOC
}

// RequiredModule 返回该模式依赖的信号模块名；pct 模式不依赖任何模块，返回空字符串。
func (m RiskLevelMode) RequiredModule() string {
	switch m.EffectiveOrPct() {
	case RiskLevelModeSupportResistance:
		return "support_resistance"
	case RiskLevelModePOC:
		return "poc"
	default:
		return ""
	}
}

// EffectiveOrPct 把空值规范化成 RiskLevelModePct，调用方不必到处判断空字符串。
func (m RiskLevelMode) EffectiveOrPct() RiskLevelMode {
	if m == "" {
		return RiskLevelModePct
	}
	return m
}

// PositionSizingMode 决定单笔开仓的名义金额怎么算。
type PositionSizingMode string

const (
	// PositionSizingModeFixedQuote 是默认模式：名义金额固定等于 MaxPositionSizeQuote，
	// 跟止损距离无关。
	PositionSizingModeFixedQuote PositionSizingMode = "fixed_quote"
	// PositionSizingModeRiskPct 按"账户权益 × 单笔风险比例 ÷ 止损距离百分比"动态计算，
	// 依赖已经解析出的止损绝对价格——调用方必须先算好止损价，再算仓位（这跟固定金额
	// 模式不同，固定金额模式的仓位大小跟止损完全无关，谁先算都行）。
	PositionSizingModeRiskPct PositionSizingMode = "risk_pct"
)

// Valid 报告是否为已支持的仓位模式。空字符串视为 PositionSizingModeFixedQuote（默认）。
func (m PositionSizingMode) Valid() bool {
	return m == "" || m == PositionSizingModeFixedQuote || m == PositionSizingModeRiskPct
}

// EffectiveOrFixed 把空值规范化成 PositionSizingModeFixedQuote，调用方不必到处判断
// 空字符串。
func (m PositionSizingMode) EffectiveOrFixed() PositionSizingMode {
	if m == "" {
		return PositionSizingModeFixedQuote
	}
	return m
}

// RiskConfig 是标的级别的风控参数。每个标的独立配置，互不影响。
type RiskConfig struct {
	// MaxPositionSizeQuote 是单笔仓位的硬上限，以计价货币计（如 USDT）。不管
	// PositionSizingMode 是哪种，这个值永远生效：fixed_quote 模式下它就是仓位金额
	// 本身；risk_pct 模式下按公式算出来的仓位一旦超过它就直接拒绝这笔交易，不做
	// 静默裁剪——静默缩小仓位会破坏"这笔交易只承担 N% 权益风险"这个用户明确要的
	// 语义，等于假装忠实执行了规则、实际上没有。
	MaxPositionSizeQuote decimal.Decimal `json:"max_position_size_quote"`
	// MaxDailyLossQuote 是单日最大亏损，达到后暂停该标的的策略。
	MaxDailyLossQuote decimal.Decimal `json:"max_daily_loss_quote"`
	// MaxHoldingPeriod 是最大持仓时间，超时强制平仓。为 0 表示不限制。
	MaxHoldingPeriod Duration `json:"max_holding_period"`
	// StopLossMode/TakeProfitMode 决定止损/止盈怎么算，默认（空值）是 RiskLevelModePct。
	// 两者相互独立，允许"止损用支撑位、止盈用固定百分比"这种混搭。
	StopLossMode RiskLevelMode `json:"stop_loss_mode,omitempty"`
	// StopLossPct 是止损百分比，如 0.02 表示 2%。仅 StopLossMode 为 pct 时有意义。
	// 为 0 表示不设置。
	StopLossPct    float64       `json:"stop_loss_pct,omitempty"`
	TakeProfitMode RiskLevelMode `json:"take_profit_mode,omitempty"`
	// TakeProfitPct 是止盈百分比。仅 TakeProfitMode 为 pct 时有意义。为 0 表示不设置。
	TakeProfitPct float64 `json:"take_profit_pct,omitempty"`
	// PositionSizingMode 决定单笔仓位怎么算，默认（空值）是 fixed_quote。
	PositionSizingMode PositionSizingMode `json:"position_sizing_mode,omitempty"`
	// AccountEquityQuote 是用户自报的账户权益（计价货币）。这是一个用户声明的静态
	// 数字，不是从交易所实时拉取的余额——运行期间不会自动更新，用户权益变化后需要
	// 自己回来改。仅 PositionSizingMode 为 risk_pct 时使用。
	AccountEquityQuote decimal.Decimal `json:"account_equity_quote,omitempty"`
	// RiskPerTradePct 是单笔愿意承担的账户权益风险比例，如 0.01 表示 1%。
	// 仅 PositionSizingMode 为 risk_pct 时使用。
	RiskPerTradePct float64 `json:"risk_per_trade_pct,omitempty"`
}

// StrategyConfig 是一个策略的完整定义，也是 AI Agent 翻译层唯一允许输出的结构。
//
// 它是"用户规则的忠实记录"：系统只负责执行它，不对它的优劣做任何判断。
type StrategyConfig struct {
	ID string `json:"id,omitempty"`
	// UserID 是这条策略的归属用户（多用户 SaaS 改造引入）。存取时以数据库的 user_id
	// 列为准，这个字段在 config JSONB 里的值可能是零值或过期的快照，不可信任
	// （见 internal/storage/postgres.go 的 GetStrategy/ListStrategies 注释）。
	UserID string `json:"user_id,omitempty"`
	Name   string `json:"name"`
	// Symbol 是标的，如 "BTCUSDT"。不同标的的策略完全独立。
	Symbol string `json:"symbol"`
	// Timeframe 是策略的触发周期：决策只在这个周期的每根 K 线收盘时计算一次。
	// 模块可以通过各自的 ModuleConfig.Timeframe 声明使用更慢的周期，此时它们在每次
	// 决策时被喂入"以触发时刻为准、已经真实收盘"的最新数据，不会看到未来的数据。
	Timeframe Timeframe `json:"timeframe"`
	// Modules 是参与该策略的模块组合，至少一个。
	Modules []ModuleConfig `json:"modules"`
	// Combine 是聚合方式。
	Combine CombineMode `json:"combine"`
	// Threshold 仅在 CombineWeighted 下使用：加权置信度超过它才触发，取值 (0, 1]。
	Threshold float64 `json:"threshold,omitempty"`
	// Risk 是该策略（该标的）的风控参数。
	Risk RiskConfig `json:"risk"`

	// State 是状态机当前状态，见 strategy_state.go。新建策略一律从 StateDraft 开始。
	State StrategyState `json:"state"`

	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	// SourceUtterance 保存用户最初的自然语言输入，用于审计"这条规则是怎么来的"。
	SourceUtterance string `json:"source_utterance,omitempty"`
}

// RequiredTimeframes 返回该策略实际用到的全部周期（触发周期本身 + 每个模块显式声明
// 的周期），去重后按字典序排列。引擎、信号引擎、回测重放都用它决定要准备哪些行情源，
// 不必各自重新遍历一遍 Modules。
func (cfg StrategyConfig) RequiredTimeframes() []Timeframe {
	seen := map[Timeframe]bool{cfg.Timeframe: true}
	for _, mc := range cfg.Modules {
		tf := mc.Timeframe
		if tf == "" {
			tf = cfg.Timeframe
		}
		seen[tf] = true
	}
	out := make([]Timeframe, 0, len(seen))
	for tf := range seen {
		out = append(out, tf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Decision 是组合引擎聚合后的最终决策，写入 Kafka 并落库审计。
type Decision struct {
	// ID 由引擎在落库前生成，订单的 Provenance 通过它反查触发依据。
	ID         string    `json:"id"`
	StrategyID string    `json:"strategy_id"`
	Symbol     string    `json:"symbol"`
	Direction  Direction `json:"direction"`
	// Score 是聚合后的强度：ALL 模式下为参与模块置信度的均值，
	// WEIGHTED 模式下为加权置信度。
	Score float64 `json:"score"`
	// Triggered 为 true 表示该决策满足触发条件，执行层应当据此下单。
	Triggered bool `json:"triggered"`
	// Signals 是参与本次决策的全部模块信号（含降级的），用于可解释性。
	Signals []Signal `json:"signals"`
	// Reason 说明为什么触发或为什么没触发，纯事实描述。
	Reason    string          `json:"reason"`
	Price     decimal.Decimal `json:"price"`
	Timestamp time.Time       `json:"timestamp"`
	// EvaluatedAt 是引擎实际完成计算的墙上时间，与 Timestamp（行情时间）区分开。
	EvaluatedAt time.Time `json:"evaluated_at"`
}
