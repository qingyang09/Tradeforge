package execution

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

func supportResistanceSignal(nearestSupport, nearestResistance string, degraded bool) types.Signal {
	raw := map[string]any{}
	if nearestSupport != "" {
		raw["nearest_support"] = map[string]any{"price": nearestSupport, "touches": 3}
	} else {
		raw["nearest_support"] = nil
	}
	if nearestResistance != "" {
		raw["nearest_resistance"] = map[string]any{"price": nearestResistance, "touches": 3}
	} else {
		raw["nearest_resistance"] = nil
	}
	return types.Signal{Module: "support_resistance", Symbol: "BTCUSDT", Raw: raw, Degraded: degraded}
}

func pocSignal(pocPrice string, degraded bool) types.Signal {
	raw := map[string]any{"is_approximate": true}
	if pocPrice != "" {
		raw["poc_price"] = pocPrice
	}
	return types.Signal{Module: "poc", Symbol: "BTCUSDT", Raw: raw, Degraded: degraded}
}

func pocRisk() types.RiskConfig {
	return types.RiskConfig{
		MaxPositionSizeQuote: dec("1000"),
		StopLossMode:         types.RiskLevelModePOC,
		TakeProfitMode:       types.RiskLevelModePOC,
	}
}

func pctRisk(stopLossPct, takeProfitPct float64) types.RiskConfig {
	return types.RiskConfig{MaxPositionSizeQuote: dec("1000"), StopLossPct: stopLossPct, TakeProfitPct: takeProfitPct}
}

func levelRisk() types.RiskConfig {
	return types.RiskConfig{
		MaxPositionSizeQuote: dec("1000"),
		StopLossMode:         types.RiskLevelModeSupportResistance,
		TakeProfitMode:       types.RiskLevelModeSupportResistance,
	}
}

// ---------- pct 模式：跟原有 adverseMove/favorableMove 的行为等价 ----------

func TestResolveStopLossPricePctLong(t *testing.T) {
	got, err := ResolveStopLossPrice(pctRisk(0.05, 0), types.DirectionLong, dec("100"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("95")) {
		t.Errorf("多头 5%% 止损价 = %s，期望 95", got)
	}
}

func TestResolveStopLossPricePctShort(t *testing.T) {
	got, err := ResolveStopLossPrice(pctRisk(0.05, 0), types.DirectionShort, dec("100"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("105")) {
		t.Errorf("空头 5%% 止损价 = %s，期望 105", got)
	}
}

func TestResolveTakeProfitPricePctLong(t *testing.T) {
	got, err := ResolveTakeProfitPrice(pctRisk(0, 0.10), types.DirectionLong, dec("100"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("110")) {
		t.Errorf("多头 10%% 止盈价 = %s，期望 110", got)
	}
}

func TestResolveTakeProfitPricePctShort(t *testing.T) {
	got, err := ResolveTakeProfitPrice(pctRisk(0, 0.10), types.DirectionShort, dec("100"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("90")) {
		t.Errorf("空头 10%% 止盈价 = %s，期望 90", got)
	}
}

func TestResolveStopLossPricePctZeroMeansUnset(t *testing.T) {
	got, err := ResolveStopLossPrice(pctRisk(0, 0), types.DirectionLong, dec("100"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Errorf("止损比例为 0 应视为未设置，返回零值，实际 %s", got)
	}
}

// ---------- support_resistance 模式：四种方向 × 止损/止盈组合 ----------

func TestResolveStopLossPriceSupportResistanceLongUsesSupport(t *testing.T) {
	signals := []types.Signal{supportResistanceSignal("95", "110", false)}
	got, err := ResolveStopLossPrice(levelRisk(), types.DirectionLong, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("95")) {
		t.Errorf("多头止损应取支撑位 95，实际 %s", got)
	}
}

func TestResolveTakeProfitPriceSupportResistanceLongUsesResistance(t *testing.T) {
	signals := []types.Signal{supportResistanceSignal("95", "110", false)}
	got, err := ResolveTakeProfitPrice(levelRisk(), types.DirectionLong, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("110")) {
		t.Errorf("多头止盈应取阻力位 110，实际 %s", got)
	}
}

func TestResolveStopLossPriceSupportResistanceShortUsesResistance(t *testing.T) {
	signals := []types.Signal{supportResistanceSignal("95", "110", false)}
	got, err := ResolveStopLossPrice(levelRisk(), types.DirectionShort, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("110")) {
		t.Errorf("空头止损应取阻力位 110，实际 %s", got)
	}
}

func TestResolveTakeProfitPriceSupportResistanceShortUsesSupport(t *testing.T) {
	signals := []types.Signal{supportResistanceSignal("95", "110", false)}
	got, err := ResolveTakeProfitPrice(levelRisk(), types.DirectionShort, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("95")) {
		t.Errorf("空头止盈应取支撑位 95，实际 %s", got)
	}
}

// ---------- support_resistance 模式：解析失败的情况都应该报错，不能悄悄给零值 ----------

func TestResolveStopLossPriceSupportResistanceMissingModuleErrors(t *testing.T) {
	_, err := ResolveStopLossPrice(levelRisk(), types.DirectionLong, dec("100"), nil)
	if err == nil {
		t.Fatal("信号里没有 support_resistance 模块数据时应该报错")
	}
}

func TestResolveStopLossPriceSupportResistanceDegradedSignalErrors(t *testing.T) {
	signals := []types.Signal{supportResistanceSignal("95", "110", true)}
	_, err := ResolveStopLossPrice(levelRisk(), types.DirectionLong, dec("100"), signals)
	if err == nil {
		t.Fatal("support_resistance 信号是降级信号时应该报错，不能假装有数据")
	}
}

func TestResolveStopLossPriceSupportResistanceNoLevelNearbyErrors(t *testing.T) {
	// 附近没有支撑位（比如价格处于历史低点）。
	signals := []types.Signal{supportResistanceSignal("", "110", false)}
	_, err := ResolveStopLossPrice(levelRisk(), types.DirectionLong, dec("100"), signals)
	if err == nil {
		t.Fatal("附近没有支撑位时应该报错，而不是返回零值假装设置成功")
	}
	if !strings.Contains(err.Error(), "支撑位") {
		t.Errorf("错误信息应提到支撑位，实际：%v", err)
	}
}

func TestResolveTakeProfitPriceSupportResistanceZeroModeReturnsZero(t *testing.T) {
	// TakeProfitMode 留空（默认 pct）且 TakeProfitPct 为 0：视为未设置止盈。
	got, err := ResolveTakeProfitPrice(types.RiskConfig{MaxPositionSizeQuote: dec("1000")}, types.DirectionLong, dec("100"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Errorf("未配置止盈时应返回零值，实际 %s", got)
	}
}

// ---------- poc 模式：多空双方用的是同一个价格，不像支撑/阻力位分两个 ----------

func TestResolveStopLossPricePOCLong(t *testing.T) {
	signals := []types.Signal{pocSignal("102", false)}
	got, err := ResolveStopLossPrice(pocRisk(), types.DirectionLong, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("102")) {
		t.Errorf("止损价应等于 POC 102，实际 %s", got)
	}
}

func TestResolveStopLossPricePOCShort(t *testing.T) {
	signals := []types.Signal{pocSignal("102", false)}
	got, err := ResolveStopLossPrice(pocRisk(), types.DirectionShort, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("102")) {
		t.Errorf("空头止损价也应等于同一个 POC 102，实际 %s", got)
	}
}

func TestResolveTakeProfitPricePOC(t *testing.T) {
	signals := []types.Signal{pocSignal("98", false)}
	got, err := ResolveTakeProfitPrice(pocRisk(), types.DirectionLong, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("98")) {
		t.Errorf("止盈价应等于 POC 98，实际 %s", got)
	}
}

func TestResolveStopLossPricePOCMissingModuleErrors(t *testing.T) {
	if _, err := ResolveStopLossPrice(pocRisk(), types.DirectionLong, dec("100"), nil); err == nil {
		t.Fatal("信号里没有 poc 模块数据时应该报错")
	}
}

func TestResolveStopLossPricePOCDegradedSignalErrors(t *testing.T) {
	signals := []types.Signal{pocSignal("102", true)}
	if _, err := ResolveStopLossPrice(pocRisk(), types.DirectionLong, dec("100"), signals); err == nil {
		t.Fatal("poc 信号是降级信号时应该报错")
	}
}

func TestResolveStopLossPricePOCNoPriceErrors(t *testing.T) {
	signals := []types.Signal{pocSignal("", false)} // 价格波动为零之类的情况，算不出 POC
	if _, err := ResolveStopLossPrice(pocRisk(), types.DirectionLong, dec("100"), signals); err == nil {
		t.Fatal("poc 信号里没有 poc_price 时应该报错")
	}
}

func TestResolveStopLossPriceUnknownModeErrors(t *testing.T) {
	risk := types.RiskConfig{MaxPositionSizeQuote: dec("1000"), StopLossMode: types.RiskLevelMode("trailing")}
	if _, err := ResolveStopLossPrice(risk, types.DirectionLong, dec("100"), nil); err == nil {
		t.Fatal("未知的止损模式应该报错")
	}
}

// 确认 decimal 精度没有意外经过 float64（哨兵用例：0.1 这种典型的浮点误差值）。
func TestResolveStopLossPricePctUsesDecimalPrecision(t *testing.T) {
	got, err := ResolveStopLossPrice(pctRisk(0.1, 0), types.DirectionLong, dec("30000"), nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := decimal.NewFromString("27000")
	if !got.Equal(want) {
		t.Errorf("止损价 = %s，期望精确等于 27000", got)
	}
}

// ---------- ResolvePositionSizeQuote：仓位怎么算 ----------

func riskPctSizingRisk(equity string, riskPct float64, maxCap string) types.RiskConfig {
	return types.RiskConfig{
		MaxPositionSizeQuote: dec(maxCap),
		PositionSizingMode:   types.PositionSizingModeRiskPct,
		AccountEquityQuote:   dec(equity),
		RiskPerTradePct:      riskPct,
		StopLossPct:          0.02, // 校验层要求 risk_pct 模式必须有可用止损，这里给个占位值
	}
}

func TestResolvePositionSizeQuoteFixedQuoteIgnoresStopLoss(t *testing.T) {
	risk := types.RiskConfig{MaxPositionSizeQuote: dec("500")}
	got, err := ResolvePositionSizeQuote(risk, dec("100"), decimal.Zero) // 止损价传零值也不影响
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !got.Equal(dec("500")) {
		t.Errorf("fixed_quote 模式仓位 = %s，期望原样等于 MaxPositionSizeQuote 500", got)
	}
}

func TestResolvePositionSizeQuoteRiskPctBasic(t *testing.T) {
	// 权益 10000，风险 1%（=100），入场 100、止损 95，距离 5% → 仓位 = 100 / 0.05 = 2000。
	risk := riskPctSizingRisk("10000", 0.01, "1000000")
	got, err := ResolvePositionSizeQuote(risk, dec("100"), dec("95"))
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !got.Equal(dec("2000")) {
		t.Errorf("risk_pct 仓位 = %s，期望 2000", got)
	}
}

func TestResolvePositionSizeQuoteRiskPctShortDirectionSameMagnitude(t *testing.T) {
	// 空头止损在上方：入场 100、止损 105，距离同样是 5%，仓位应该跟多头场景一致——
	// 公式只关心距离的绝对值，不关心方向。
	risk := riskPctSizingRisk("10000", 0.01, "1000000")
	got, err := ResolvePositionSizeQuote(risk, dec("100"), dec("105"))
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !got.Equal(dec("2000")) {
		t.Errorf("risk_pct 仓位 = %s，期望 2000", got)
	}
}

func TestResolvePositionSizeQuoteRiskPctZeroStopDistanceErrors(t *testing.T) {
	risk := riskPctSizingRisk("10000", 0.01, "1000000")
	if _, err := ResolvePositionSizeQuote(risk, dec("100"), dec("100")); err == nil {
		t.Fatal("止损价等于入场价（距离为 0）应该报错")
	}
}

func TestResolvePositionSizeQuoteRiskPctUnresolvedStopLossErrors(t *testing.T) {
	risk := riskPctSizingRisk("10000", 0.01, "1000000")
	if _, err := ResolvePositionSizeQuote(risk, dec("100"), decimal.Zero); err == nil {
		t.Fatal("止损价未解析（零值）时应该报错")
	}
}

func TestResolvePositionSizeQuoteUnknownModeErrors(t *testing.T) {
	risk := types.RiskConfig{MaxPositionSizeQuote: dec("1000"), PositionSizingMode: types.PositionSizingMode("kelly")}
	if _, err := ResolvePositionSizeQuote(risk, dec("100"), dec("95")); err == nil {
		t.Fatal("未知的仓位模式应该报错")
	}
}
