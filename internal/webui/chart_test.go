package webui

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

func trade(hoursAgo float64, pnl int64, segment string) types.BacktestTrade {
	return types.BacktestTrade{
		EntryTime: time.Now().Add(-time.Duration(hoursAgo * float64(time.Hour))),
		PnL:       decimal.NewFromInt(pnl),
		Segment:   segment,
	}
}

func TestBuildEquityChartNoTrades(t *testing.T) {
	c := buildApproxEquityChart(nil)
	if c.HasTrades {
		t.Error("没有交易时 HasTrades 应为 false")
	}
	if c.InSamplePoints != "" || c.OutOfSamplePoints != "" {
		t.Errorf("没有交易时不应有任何坐标点：%+v", c)
	}
}

func TestBuildEquityChartSeparatesSegments(t *testing.T) {
	trades := []types.BacktestTrade{
		trade(3, 10, "in_sample"),
		trade(2, 5, "in_sample"),
		trade(1, -8, "out_of_sample"),
	}
	c := buildApproxEquityChart(trades)
	if !c.HasTrades {
		t.Fatal("应有交易")
	}
	if c.InSamplePoints == "" {
		t.Error("应有样本内坐标点")
	}
	if c.OutOfSamplePoints == "" {
		t.Error("应有样本外坐标点")
	}
	// 累计：0 -> 10 -> 15 -> 7，最小值 0，最大值 15。
	if c.MinLabel != "0.00" {
		t.Errorf("MinPnL = %s，期望 0.00", c.MinLabel)
	}
	if c.MaxLabel != "15.00" {
		t.Errorf("MaxPnL = %s，期望 15.00", c.MaxLabel)
	}
}

func TestBuildEquityChartAllInSample(t *testing.T) {
	trades := []types.BacktestTrade{trade(2, 10, "in_sample"), trade(1, 5, "in_sample")}
	c := buildApproxEquityChart(trades)
	if c.InSamplePoints == "" {
		t.Error("应有样本内坐标点")
	}
	if c.OutOfSamplePoints != "" {
		t.Errorf("没有样本外交易时不应有样本外坐标点，实际：%q", c.OutOfSamplePoints)
	}
}

func TestBuildEquityChartFlatLineDoesNotDivideByZero(t *testing.T) {
	trades := []types.BacktestTrade{trade(2, 0, "in_sample"), trade(1, 0, "in_sample")}
	c := buildApproxEquityChart(trades)
	if !c.HasTrades {
		t.Fatal("应有交易")
	}
	if strings.Contains(c.InSamplePoints, "NaN") || strings.Contains(c.InSamplePoints, "Inf") {
		t.Errorf("全程零盈亏不应产生 NaN/Inf：%q", c.InSamplePoints)
	}
}

// 样本外的第一个点应该续接样本内的最后一个点，让两段折线在视觉上连续，不留缺口。
func TestBuildEquityChartConnectsSegmentsAtBoundary(t *testing.T) {
	trades := []types.BacktestTrade{
		trade(2, 10, "in_sample"),
		trade(1, -3, "out_of_sample"),
	}
	c := buildApproxEquityChart(trades)
	inPts := strings.Fields(c.InSamplePoints)
	outPts := strings.Fields(c.OutOfSamplePoints)
	if len(inPts) == 0 || len(outPts) < 2 {
		t.Fatalf("坐标点数量不符预期：in=%v out=%v", inPts, outPts)
	}
	lastIn := inPts[len(inPts)-1]
	firstOut := outPts[0]
	if lastIn != firstOut {
		t.Errorf("样本外首点应等于样本内末点以保持连续，样本内末点=%s 样本外首点=%s", lastIn, firstOut)
	}
}

// ---------- buildEquityChart：真实权益曲线优先，旧记录退回近似 ----------

func eqPoint(hoursAgo float64, equity int64) types.EquityPoint {
	return types.EquityPoint{
		Time:   time.Now().Add(-time.Duration(hoursAgo * float64(time.Hour))),
		Equity: decimal.NewFromInt(equity),
	}
}

func TestBuildEquityChartUsesRealCurveWhenAvailable(t *testing.T) {
	result := types.BacktestResult{
		EquityCurve: []types.EquityPoint{
			eqPoint(3, 1000), eqPoint(2, 1100), eqPoint(1, 1050), eqPoint(0, 1080),
		},
		Trades: []types.BacktestTrade{trade(2, 10, "in_sample")}, // 不应被用到
	}
	c := buildEquityChart(result)
	if !c.IsRealEquityCurve {
		t.Fatal("有 EquityCurve 数据时应该使用真实权益曲线，不是近似")
	}
	if !c.HasTrades {
		t.Error("HasTrades 应为 true")
	}
	if c.PeakPoints == "" {
		t.Error("应该有历史新高包络线坐标")
	}
	if c.DrawdownAreaPoints == "" {
		t.Error("应该有回撤阴影区域坐标")
	}
	if c.MinLabel != "1000.00" || c.MaxLabel != "1100.00" {
		t.Errorf("MinLabel/MaxLabel = %s/%s，期望 1000.00/1100.00", c.MinLabel, c.MaxLabel)
	}
}

func TestBuildEquityChartFallsBackToApproxWhenNoEquityCurve(t *testing.T) {
	result := types.BacktestResult{
		Trades: []types.BacktestTrade{trade(2, 10, "in_sample"), trade(1, -3, "out_of_sample")},
	}
	c := buildEquityChart(result)
	if c.IsRealEquityCurve {
		t.Fatal("没有 EquityCurve 数据时应该退回近似曲线")
	}
	if !c.HasTrades {
		t.Error("HasTrades 应为 true（有交易明细）")
	}
}

func TestBuildEquityChartSeparatesSegmentsUsingSplitPoint(t *testing.T) {
	now := time.Now()
	splitAt := now.Add(-90 * time.Minute)
	result := types.BacktestResult{
		Segments: []types.BacktestSegment{
			{Label: "in_sample", Start: now.Add(-3 * time.Hour), End: splitAt},
			{Label: "out_of_sample", Start: splitAt, End: now},
		},
		EquityCurve: []types.EquityPoint{
			{Time: now.Add(-3 * time.Hour), Equity: decimal.NewFromInt(1000)},
			{Time: now.Add(-2 * time.Hour), Equity: decimal.NewFromInt(1050)},
			{Time: now.Add(-1 * time.Hour), Equity: decimal.NewFromInt(1020)},
			{Time: now, Equity: decimal.NewFromInt(1080)},
		},
	}
	c := buildEquityChart(result)
	if c.InSamplePoints == "" {
		t.Error("分界点之前的两个点应该落进样本内")
	}
	if c.OutOfSamplePoints == "" {
		t.Error("分界点及之后的点应该落进样本外")
	}
}

func TestBuildMonthlyReturnsBucketsBySplitMonth(t *testing.T) {
	jan := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2025, 2, 20, 0, 0, 0, 0, time.UTC)
	curve := []types.EquityPoint{
		{Time: jan, Equity: decimal.NewFromInt(1000)},
		{Time: jan.Add(10 * 24 * time.Hour), Equity: decimal.NewFromInt(1100)}, // 仍在 1 月
		{Time: feb, Equity: decimal.NewFromInt(1210)},
	}
	got := buildMonthlyReturns(curve)
	if len(got) != 2 {
		t.Fatalf("月份数量 = %d，期望 2，实际 %+v", len(got), got)
	}
	if got[0].Month != "2025-01" {
		t.Errorf("第一个月 = %s，期望 2025-01", got[0].Month)
	}
	// 1 月：1000 -> 1100，收益 10%。
	if got[0].Return < 0.099 || got[0].Return > 0.101 {
		t.Errorf("1 月收益 = %v，期望约 0.10", got[0].Return)
	}
	// 2 月：1100 -> 1210，收益 10%。
	if got[1].Month != "2025-02" {
		t.Errorf("第二个月 = %s，期望 2025-02", got[1].Month)
	}
	if got[1].Return < 0.099 || got[1].Return > 0.101 {
		t.Errorf("2 月收益 = %v，期望约 0.10", got[1].Return)
	}
}

func TestBuildMonthlyReturnsEmptyCurveReturnsNil(t *testing.T) {
	if got := buildMonthlyReturns(nil); got != nil {
		t.Errorf("空曲线应返回 nil，实际 %+v", got)
	}
	if got := buildMonthlyReturns([]types.EquityPoint{eqPoint(0, 1000)}); got != nil {
		t.Errorf("单点曲线应返回 nil（算不出收益），实际 %+v", got)
	}
}

func TestHoldingDurationFormatsElapsedTime(t *testing.T) {
	entry := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	exit := entry.Add(2*time.Hour + 15*time.Minute)
	if got := holdingDuration(entry, exit); got != "2h15m0s" {
		t.Errorf("holdingDuration = %q，期望 2h15m0s", got)
	}
}

func TestHoldingDurationNegativeReturnsDash(t *testing.T) {
	entry := time.Now()
	exit := entry.Add(-time.Hour)
	if got := holdingDuration(entry, exit); got != "-" {
		t.Errorf("平仓时间早于开仓时间应返回占位符，实际 %q", got)
	}
}
