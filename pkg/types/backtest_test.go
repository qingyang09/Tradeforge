package types

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestFeeDragRatioComputesFeesOverGrossProfit(t *testing.T) {
	// Capital 10000, grows to 11000 (net profit 1000), fees cost 800:
	// gross profit = 1000 + 800 = 1800, ratio = 800/1800 ~= 0.444.
	m := PerformanceMetrics{
		TradeCount:  10,
		TotalReturn: 0.1,
		FinalEquity: decimal.NewFromInt(11000),
		TotalFees:   decimal.NewFromInt(800),
	}
	ratio, ok := m.FeeDragRatio()
	if !ok {
		t.Fatal("should be able to compute a ratio when there are trades and gross profit is positive")
	}
	if want := 800.0 / 1800.0; ratio < want-1e-6 || ratio > want+1e-6 {
		t.Errorf("ratio = %v, expected approximately %v", ratio, want)
	}
}

func TestFeeDragRatioUndefinedWhenNoTrades(t *testing.T) {
	m := PerformanceMetrics{TradeCount: 0}
	if _, ok := m.FeeDragRatio(); ok {
		t.Error("should not compute a ratio when there are no trades")
	}
}

func TestFeeDragRatioUndefinedWhenGrossProfitNotPositive(t *testing.T) {
	// Net profit is negative, and fees aren't enough to push "net profit + fees"
	// positive: the strategy was already losing money before fees, so "fees as a
	// fraction of gross profit" is meaningless here.
	m := PerformanceMetrics{
		TradeCount:  10,
		TotalReturn: -0.2,
		FinalEquity: decimal.NewFromInt(8000),
		TotalFees:   decimal.NewFromInt(100),
	}
	if _, ok := m.FeeDragRatio(); ok {
		t.Error("should not compute a ratio when gross profit is not positive")
	}
}

func TestFeeDragRatioUndefinedWhenTotalReturnIsTotalLoss(t *testing.T) {
	m := PerformanceMetrics{TradeCount: 5, TotalReturn: -1}
	if _, ok := m.FeeDragRatio(); ok {
		t.Error("should not compute a ratio when capital is wiped out (total_return = -1), which zeroes the denominator")
	}
}
