package types

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestFeeDragRatioComputesFeesOverGrossProfit(t *testing.T) {
	// 本金 10000，涨到 11000（净利润 1000），手续费花了 800：
	// 毛利润 = 1000 + 800 = 1800，占比 = 800/1800 ≈ 0.444。
	m := PerformanceMetrics{
		TradeCount:  10,
		TotalReturn: 0.1,
		FinalEquity: decimal.NewFromInt(11000),
		TotalFees:   decimal.NewFromInt(800),
	}
	ratio, ok := m.FeeDragRatio()
	if !ok {
		t.Fatal("有交易且毛利润为正时应该能算出比例")
	}
	if want := 800.0 / 1800.0; ratio < want-1e-6 || ratio > want+1e-6 {
		t.Errorf("比例 = %v，期望约 %v", ratio, want)
	}
}

func TestFeeDragRatioUndefinedWhenNoTrades(t *testing.T) {
	m := PerformanceMetrics{TradeCount: 0}
	if _, ok := m.FeeDragRatio(); ok {
		t.Error("没有交易时不应该算出一个比例")
	}
}

func TestFeeDragRatioUndefinedWhenGrossProfitNotPositive(t *testing.T) {
	// 净利润为负、手续费也不足以让"净利润+手续费"转正：扣手续费前策略本身就在亏钱，
	// "手续费占毛利润的比例"这个说法此时没有意义。
	m := PerformanceMetrics{
		TradeCount:  10,
		TotalReturn: -0.2,
		FinalEquity: decimal.NewFromInt(8000),
		TotalFees:   decimal.NewFromInt(100),
	}
	if _, ok := m.FeeDragRatio(); ok {
		t.Error("毛利润非正时不应该算出一个比例")
	}
}

func TestFeeDragRatioUndefinedWhenTotalReturnIsTotalLoss(t *testing.T) {
	m := PerformanceMetrics{TradeCount: 5, TotalReturn: -1}
	if _, ok := m.FeeDragRatio(); ok {
		t.Error("本金亏光（total_return = -1）时分母为零，不应该算出一个比例")
	}
}
