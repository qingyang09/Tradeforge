package types

import (
	"time"

	"github.com/shopspring/decimal"
)

// FeeModel describes the fee and slippage modeling used in a backtest.
// Backtesting against raw prices with no fees is explicitly forbidden.
type FeeModel struct {
	// MakerFeeRate is the maker fee rate, e.g. 0.0002 for 0.02%.
	MakerFeeRate float64 `json:"maker_fee_rate"`
	// TakerFeeRate is the taker fee rate, e.g. 0.0004 for 0.04%.
	TakerFeeRate float64 `json:"taker_fee_rate"`
	// SlippageBps is slippage, in basis points (1 bps = 0.01%).
	SlippageBps float64 `json:"slippage_bps"`
}

// DefaultFeeModel is a conservative default modeling, based on mainstream
// exchanges' spot taker fee rates. Erring conservative is intentional: a
// backtest result should underestimate performance rather than overestimate it.
func DefaultFeeModel() FeeModel {
	return FeeModel{MakerFeeRate: 0.0002, TakerFeeRate: 0.0004, SlippageBps: 5}
}

// BacktestSegment annotates a time window covered by a backtest and its nature.
type BacktestSegment struct {
	// Label is "in_sample" or "out_of_sample".
	Label string    `json:"label"`
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// PerformanceMetrics are the performance metrics over one backtest segment.
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

// BacktestResult is the complete output of one backtest run.
//
// Storing InSample and OutOfSample separately is a hard requirement: only the
// out-of-sample metrics represent "parameters that weren't tuned on this data",
// and the distinction must be preserved when displaying results, to prevent an
// overfit result from being misread as real performance.
type BacktestResult struct {
	ID         string `json:"id,omitempty"`
	StrategyID string `json:"strategy_id"`
	Symbol     string `json:"symbol"`

	// Overall is the metrics over the entire dataset.
	Overall PerformanceMetrics `json:"overall"`
	// InSample is the training-segment metrics; if parameters were tuned, they were
	// tuned on this segment.
	InSample PerformanceMetrics `json:"in_sample"`
	// OutOfSample is the test-segment metrics, the sole basis for deciding whether a
	// strategy may enter paper trading.
	OutOfSample PerformanceMetrics `json:"out_of_sample"`

	Segments []BacktestSegment `json:"segments"`
	FeeModel FeeModel          `json:"fee_model"`

	// Trades is the trade-by-trade detail, for spot-checking specific trade points
	// rather than only looking at summary metrics.
	Trades []BacktestTrade `json:"trades,omitempty"`

	// EquityCurve is per-candle equity (including unrealized P&L), used to draw the
	// real equity/drawdown curve — unlike Trades, it captures the floating swings
	// while a position is held, which per-trade realized P&L alone can't show.
	// Historical records produced before 2026-09 don't have this data (omitempty;
	// the UI falls back to the old approximate curve when it's empty) and it is not
	// backfilled.
	EquityCurve []EquityPoint `json:"equity_curve,omitempty"`

	InitialCapital decimal.Decimal `json:"initial_capital"`
	DataStart      time.Time       `json:"data_start"`
	DataEnd        time.Time       `json:"data_end"`
	RanAt          time.Time       `json:"ran_at"`
	// EngineVersion tags the backtest engine version, so a historical result can be
	// traced back to the code that produced it.
	EngineVersion string `json:"engine_version"`
}

// BacktestTrade is one complete round-trip trade within a backtest.
type BacktestTrade struct {
	EntryTime  time.Time       `json:"entry_time"`
	ExitTime   time.Time       `json:"exit_time"`
	Direction  Direction       `json:"direction"`
	EntryPrice decimal.Decimal `json:"entry_price"`
	ExitPrice  decimal.Decimal `json:"exit_price"`
	Quantity   decimal.Decimal `json:"quantity"`
	PnL        decimal.Decimal `json:"pnl"`
	Fees       decimal.Decimal `json:"fees"`
	// ExitReason explains why the position was closed: "signal", "stop_loss",
	// "take_profit", "max_holding", "end_of_data".
	ExitReason string `json:"exit_reason"`
	// TriggerSignals records each module's signal at entry time, satisfying the
	// explainability requirement.
	TriggerSignals []Signal `json:"trigger_signals,omitempty"`
	// Segment annotates whether this trade falls in in_sample or out_of_sample.
	Segment string `json:"segment"`
}

// EquityPoint is one sample point on the equity curve: account equity (including
// unrealized P&L) at the close of a given candle.
type EquityPoint struct {
	Time   time.Time       `json:"time"`
	Equity decimal.Decimal `json:"equity"`
}

// FeeDragRatio returns the fraction of gross profit consumed by fees:
// TotalFees / gross profit, where gross profit = net profit + TotalFees (net
// profit has already had fees deducted, via FinalEquity).
//
// This segment's own starting capital has no dedicated field; it's backed out
// from FinalEquity and TotalReturn (the out_of_sample segment's starting capital
// is the equity at the split point, not the same as the backtest's overall
// InitialCapital — see metrics_for_segment in
// python/backtest/tradeforge_backtest/metrics.py).
//
// When gross profit <= 0 (unprofitable even before fees, or no trades at all)
// this ratio has no well-defined meaning; ok returns false and callers should
// skip it rather than display/validate a misleading number.
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
