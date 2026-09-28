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

// ---------- pct mode: equivalent to the old adverseMove/favorableMove behavior ----------

func TestResolveStopLossPricePctLong(t *testing.T) {
	got, err := ResolveStopLossPrice(pctRisk(0.05, 0), types.DirectionLong, dec("100"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("95")) {
		t.Errorf("long 5%% stop-loss price = %s, want 95", got)
	}
}

func TestResolveStopLossPricePctShort(t *testing.T) {
	got, err := ResolveStopLossPrice(pctRisk(0.05, 0), types.DirectionShort, dec("100"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("105")) {
		t.Errorf("short 5%% stop-loss price = %s, want 105", got)
	}
}

func TestResolveTakeProfitPricePctLong(t *testing.T) {
	got, err := ResolveTakeProfitPrice(pctRisk(0, 0.10), types.DirectionLong, dec("100"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("110")) {
		t.Errorf("long 10%% take-profit price = %s, want 110", got)
	}
}

func TestResolveTakeProfitPricePctShort(t *testing.T) {
	got, err := ResolveTakeProfitPrice(pctRisk(0, 0.10), types.DirectionShort, dec("100"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("90")) {
		t.Errorf("short 10%% take-profit price = %s, want 90", got)
	}
}

func TestResolveStopLossPricePctZeroMeansUnset(t *testing.T) {
	got, err := ResolveStopLossPrice(pctRisk(0, 0), types.DirectionLong, dec("100"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Errorf("a stop-loss percentage of 0 should be treated as unset and return the zero value, got %s", got)
	}
}

// ---------- support_resistance mode: four directions x stop-loss/take-profit combinations ----------

func TestResolveStopLossPriceSupportResistanceLongUsesSupport(t *testing.T) {
	signals := []types.Signal{supportResistanceSignal("95", "110", false)}
	got, err := ResolveStopLossPrice(levelRisk(), types.DirectionLong, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("95")) {
		t.Errorf("a long's stop-loss should take the support level 95, got %s", got)
	}
}

func TestResolveTakeProfitPriceSupportResistanceLongUsesResistance(t *testing.T) {
	signals := []types.Signal{supportResistanceSignal("95", "110", false)}
	got, err := ResolveTakeProfitPrice(levelRisk(), types.DirectionLong, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("110")) {
		t.Errorf("a long's take-profit should take the resistance level 110, got %s", got)
	}
}

func TestResolveStopLossPriceSupportResistanceShortUsesResistance(t *testing.T) {
	signals := []types.Signal{supportResistanceSignal("95", "110", false)}
	got, err := ResolveStopLossPrice(levelRisk(), types.DirectionShort, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("110")) {
		t.Errorf("a short's stop-loss should take the resistance level 110, got %s", got)
	}
}

func TestResolveTakeProfitPriceSupportResistanceShortUsesSupport(t *testing.T) {
	signals := []types.Signal{supportResistanceSignal("95", "110", false)}
	got, err := ResolveTakeProfitPrice(levelRisk(), types.DirectionShort, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("95")) {
		t.Errorf("a short's take-profit should take the support level 95, got %s", got)
	}
}

// ---------- support_resistance mode: every resolution failure should error, never silently return zero ----------

func TestResolveStopLossPriceSupportResistanceMissingModuleErrors(t *testing.T) {
	_, err := ResolveStopLossPrice(levelRisk(), types.DirectionLong, dec("100"), nil)
	if err == nil {
		t.Fatal("should error when the signals contain no support_resistance module data")
	}
}

func TestResolveStopLossPriceSupportResistanceDegradedSignalErrors(t *testing.T) {
	signals := []types.Signal{supportResistanceSignal("95", "110", true)}
	_, err := ResolveStopLossPrice(levelRisk(), types.DirectionLong, dec("100"), signals)
	if err == nil {
		t.Fatal("should error when the support_resistance signal is a degraded signal, must not pretend it has data")
	}
}

func TestResolveStopLossPriceSupportResistanceNoLevelNearbyErrors(t *testing.T) {
	// No support level nearby (e.g. price is at a historical low).
	signals := []types.Signal{supportResistanceSignal("", "110", false)}
	_, err := ResolveStopLossPrice(levelRisk(), types.DirectionLong, dec("100"), signals)
	if err == nil {
		t.Fatal("should error when there's no support level nearby, rather than returning zero and pretending it succeeded")
	}
	if !strings.Contains(err.Error(), "支撑位") {
		t.Errorf("error message should mention the support level, got: %v", err)
	}
}

func TestResolveTakeProfitPriceSupportResistanceZeroModeReturnsZero(t *testing.T) {
	// TakeProfitMode left empty (defaults to pct) and TakeProfitPct is 0: treated as take-profit unset.
	got, err := ResolveTakeProfitPrice(types.RiskConfig{MaxPositionSizeQuote: dec("1000")}, types.DirectionLong, dec("100"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Errorf("should return zero when take-profit isn't configured, got %s", got)
	}
}

// ---------- poc mode: longs and shorts use the same price, unlike support/resistance which splits into two ----------

func TestResolveStopLossPricePOCLong(t *testing.T) {
	signals := []types.Signal{pocSignal("102", false)}
	got, err := ResolveStopLossPrice(pocRisk(), types.DirectionLong, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("102")) {
		t.Errorf("stop-loss price should equal the POC 102, got %s", got)
	}
}

func TestResolveStopLossPricePOCShort(t *testing.T) {
	signals := []types.Signal{pocSignal("102", false)}
	got, err := ResolveStopLossPrice(pocRisk(), types.DirectionShort, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("102")) {
		t.Errorf("a short's stop-loss price should also equal the same POC 102, got %s", got)
	}
}

func TestResolveTakeProfitPricePOC(t *testing.T) {
	signals := []types.Signal{pocSignal("98", false)}
	got, err := ResolveTakeProfitPrice(pocRisk(), types.DirectionLong, dec("100"), signals)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(dec("98")) {
		t.Errorf("take-profit price should equal the POC 98, got %s", got)
	}
}

func TestResolveStopLossPricePOCMissingModuleErrors(t *testing.T) {
	if _, err := ResolveStopLossPrice(pocRisk(), types.DirectionLong, dec("100"), nil); err == nil {
		t.Fatal("should error when the signals contain no poc module data")
	}
}

func TestResolveStopLossPricePOCDegradedSignalErrors(t *testing.T) {
	signals := []types.Signal{pocSignal("102", true)}
	if _, err := ResolveStopLossPrice(pocRisk(), types.DirectionLong, dec("100"), signals); err == nil {
		t.Fatal("should error when the poc signal is a degraded signal")
	}
}

func TestResolveStopLossPricePOCNoPriceErrors(t *testing.T) {
	signals := []types.Signal{pocSignal("", false)} // e.g. zero price variance, POC can't be computed
	if _, err := ResolveStopLossPrice(pocRisk(), types.DirectionLong, dec("100"), signals); err == nil {
		t.Fatal("should error when the poc signal has no poc_price")
	}
}

func TestResolveStopLossPriceUnknownModeErrors(t *testing.T) {
	risk := types.RiskConfig{MaxPositionSizeQuote: dec("1000"), StopLossMode: types.RiskLevelMode("trailing")}
	if _, err := ResolveStopLossPrice(risk, types.DirectionLong, dec("100"), nil); err == nil {
		t.Fatal("an unknown stop-loss mode should error")
	}
}

// Confirms decimal precision never accidentally passes through float64 (sentinel case: 0.1, a classic floating-point error value).
func TestResolveStopLossPricePctUsesDecimalPrecision(t *testing.T) {
	got, err := ResolveStopLossPrice(pctRisk(0.1, 0), types.DirectionLong, dec("30000"), nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := decimal.NewFromString("27000")
	if !got.Equal(want) {
		t.Errorf("stop-loss price = %s, want exactly 27000", got)
	}
}

// ---------- ResolvePositionSizeQuote: how the position size is computed ----------

func riskPctSizingRisk(equity string, riskPct float64, maxCap string) types.RiskConfig {
	return types.RiskConfig{
		MaxPositionSizeQuote: dec(maxCap),
		PositionSizingMode:   types.PositionSizingModeRiskPct,
		AccountEquityQuote:   dec(equity),
		RiskPerTradePct:      riskPct,
		StopLossPct:          0.02, // the validation layer requires risk_pct mode to have a usable stop-loss; this is a placeholder value
	}
}

func TestResolvePositionSizeQuoteFixedQuoteIgnoresStopLoss(t *testing.T) {
	risk := types.RiskConfig{MaxPositionSizeQuote: dec("500")}
	got, err := ResolvePositionSizeQuote(risk, dec("100"), decimal.Zero) // passing zero for the stop-loss price has no effect either
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(dec("500")) {
		t.Errorf("fixed_quote mode position size = %s, want it to equal MaxPositionSizeQuote 500 unchanged", got)
	}
}

func TestResolvePositionSizeQuoteRiskPctBasic(t *testing.T) {
	// Equity 10000, risk 1% (=100), entry 100, stop 95, distance 5% -> position = 100 / 0.05 = 2000.
	risk := riskPctSizingRisk("10000", 0.01, "1000000")
	got, err := ResolvePositionSizeQuote(risk, dec("100"), dec("95"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(dec("2000")) {
		t.Errorf("risk_pct position size = %s, want 2000", got)
	}
}

func TestResolvePositionSizeQuoteRiskPctShortDirectionSameMagnitude(t *testing.T) {
	// A short's stop-loss sits above: entry 100, stop 105, distance is also 5%,
	// so the position size should match the long case — the formula only cares about the absolute distance, not direction.
	risk := riskPctSizingRisk("10000", 0.01, "1000000")
	got, err := ResolvePositionSizeQuote(risk, dec("100"), dec("105"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(dec("2000")) {
		t.Errorf("risk_pct position size = %s, want 2000", got)
	}
}

func TestResolvePositionSizeQuoteRiskPctZeroStopDistanceErrors(t *testing.T) {
	risk := riskPctSizingRisk("10000", 0.01, "1000000")
	if _, err := ResolvePositionSizeQuote(risk, dec("100"), dec("100")); err == nil {
		t.Fatal("should error when the stop-loss price equals the entry price (zero distance)")
	}
}

func TestResolvePositionSizeQuoteRiskPctUnresolvedStopLossErrors(t *testing.T) {
	risk := riskPctSizingRisk("10000", 0.01, "1000000")
	if _, err := ResolvePositionSizeQuote(risk, dec("100"), decimal.Zero); err == nil {
		t.Fatal("should error when the stop-loss price is unresolved (zero value)")
	}
}

func TestResolvePositionSizeQuoteUnknownModeErrors(t *testing.T) {
	risk := types.RiskConfig{MaxPositionSizeQuote: dec("1000"), PositionSizingMode: types.PositionSizingMode("kelly")}
	if _, err := ResolvePositionSizeQuote(risk, dec("100"), dec("95")); err == nil {
		t.Fatal("an unknown position-sizing mode should error")
	}
}
