package types

import (
	"time"

	"github.com/shopspring/decimal"
)

// FeeModel 描述回测中的手续费与滑点建模。裸价格回测是明确禁止的。
type FeeModel struct {
	// MakerFeeRate 挂单费率，如 0.0002 表示 0.02%。
	MakerFeeRate float64 `json:"maker_fee_rate"`
	// TakerFeeRate 吃单费率，如 0.0004 表示 0.04%。
	TakerFeeRate float64 `json:"taker_fee_rate"`
	// SlippageBps 滑点，以基点计（1 bps = 0.01%）。
	SlippageBps float64 `json:"slippage_bps"`
}

// DefaultFeeModel 是保守的默认建模，参考主流交易所现货吃单费率。
// 数值偏保守是有意的：回测结果宁可低估也不要高估。
func DefaultFeeModel() FeeModel {
	return FeeModel{MakerFeeRate: 0.0002, TakerFeeRate: 0.0004, SlippageBps: 5}
}

// BacktestSegment 标注一段回测覆盖的时间窗口及其性质。
type BacktestSegment struct {
	// Label 为 "in_sample" 或 "out_of_sample"。
	Label string    `json:"label"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// PerformanceMetrics 是一段回测区间上的绩效指标。
type PerformanceMetrics struct {
	TotalReturn      float64         `json:"total_return"`
	AnnualizedReturn float64         `json:"annualized_return"`
	SharpeRatio      float64         `json:"sharpe_ratio"`
	SortinoRatio     float64         `json:"sortino_ratio"`
	MaxDrawdown      float64         `json:"max_drawdown"`
	WinRate          float64         `json:"win_rate"`
	ProfitFactor     float64         `json:"profit_factor"`
	TradeCount       int             `json:"trade_count"`
	TotalFees        decimal.Decimal `json:"total_fees"`
	FinalEquity      decimal.Decimal `json:"final_equity"`
}

// BacktestResult 是一次回测的完整产物。
//
// InSample 与 OutOfSample 分开保存是硬性要求：只有样本外指标才代表
// "参数没有在这段数据上被调过"，展示时必须区分，防止过拟合结果被误读。
type BacktestResult struct {
	ID         string `json:"id,omitempty"`
	StrategyID string `json:"strategy_id"`
	Symbol     string `json:"symbol"`

	// Overall 是整段数据上的指标。
	Overall PerformanceMetrics `json:"overall"`
	// InSample 是训练段指标；参数若经过调优，是在这一段上调的。
	InSample PerformanceMetrics `json:"in_sample"`
	// OutOfSample 是测试段指标，是判断策略能否进入模拟盘的唯一依据。
	OutOfSample PerformanceMetrics `json:"out_of_sample"`

	Segments []BacktestSegment `json:"segments"`
	FeeModel FeeModel          `json:"fee_model"`

	// Trades 是逐笔交易明细，用于抽查具体交易点位而不是只看汇总指标。
	Trades []BacktestTrade `json:"trades,omitempty"`

	// EquityCurve 是逐根 K 线的权益（含浮动盈亏），用于画真正的权益/回撤曲线——
	// 跟 Trades 的区别是它包含持仓期间的浮动波动，逐笔已实现盈亏看不出这段。
	// 2026-09 之前产生的历史记录没有这份数据（omitempty，为空时界面退回旧的
	// 近似曲线），不做回填。
	EquityCurve []EquityPoint `json:"equity_curve,omitempty"`

	InitialCapital decimal.Decimal `json:"initial_capital"`
	DataStart      time.Time       `json:"data_start"`
	DataEnd        time.Time       `json:"data_end"`
	RanAt          time.Time       `json:"ran_at"`
	// EngineVersion 标注回测引擎版本，历史结果需要能追溯到产出它的代码。
	EngineVersion string `json:"engine_version"`
}

// BacktestTrade 是回测中的一笔完整往返交易。
type BacktestTrade struct {
	EntryTime  time.Time       `json:"entry_time"`
	ExitTime   time.Time       `json:"exit_time"`
	Direction  Direction       `json:"direction"`
	EntryPrice decimal.Decimal `json:"entry_price"`
	ExitPrice  decimal.Decimal `json:"exit_price"`
	Quantity   decimal.Decimal `json:"quantity"`
	PnL        decimal.Decimal `json:"pnl"`
	Fees       decimal.Decimal `json:"fees"`
	// ExitReason 说明平仓原因："signal"、"stop_loss"、"take_profit"、"max_holding"、"end_of_data"。
	ExitReason string `json:"exit_reason"`
	// TriggerSignals 记录开仓时各模块的信号，满足可解释性要求。
	TriggerSignals []Signal `json:"trigger_signals,omitempty"`
	// Segment 标注该笔交易落在 in_sample 还是 out_of_sample。
	Segment string `json:"segment"`
}

// EquityPoint 是权益曲线上的一个采样点：某根K线收盘时的账户权益（含浮动盈亏）。
type EquityPoint struct {
	Time   time.Time       `json:"time"`
	Equity decimal.Decimal `json:"equity"`
}

// FeeDragRatio 返回手续费占毛利润的比例：TotalFees / 毛利润，
// 毛利润 = 净利润 + TotalFees（净利润已经在 FinalEquity 里扣过手续费）。
//
// 这段区间自己的起始本金没有单独存字段，是从 FinalEquity 和 TotalReturn 反推出来的
// （out_of_sample 起始本金是分割点当时的权益，不等于整个回测的 InitialCapital，
// 见 python/backtest/tradeforge_backtest/metrics.py 的 metrics_for_segment）。
//
// 毛利润 <= 0（扣手续费前就不赚钱，或没有任何交易）时这个比例没有明确含义，
// ok 返回 false，调用方应跳过而不是展示/校验一个误导性的数字。
func (m PerformanceMetrics) FeeDragRatio() (ratio float64, ok bool) {
	if m.TradeCount == 0 {
		return 0, false
	}
	denom := 1 + m.TotalReturn
	if denom <= 0 {
		return 0, false
	}
	finalEquity, _ := m.FinalEquity.Float64()
	fees, _ := m.TotalFees.Float64()
	capital := finalEquity / denom
	netProfit := finalEquity - capital
	gross := netProfit + fees
	if gross <= 0 {
		return 0, false
	}
	return fees / gross, true
}
