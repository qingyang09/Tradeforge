package strategy

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"tradeforge/internal/modules"
	"tradeforge/pkg/types"
)

func validConfig() types.StrategyConfig {
	return types.StrategyConfig{
		Name:      "测试策略",
		Symbol:    "BTCUSDT",
		Timeframe: types.TF1h,
		Combine:   types.CombineAll,
		Modules: []types.ModuleConfig{
			{Module: "volume_breakout", Params: map[string]any{"multiplier": 2.0}},
		},
		Risk: types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
	}
}

func TestValidateAcceptsPctStopLossByDefault(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.StopLossPct = 0.02
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("default pct-mode stop loss should not be rejected: %v", err)
	}
}

func TestValidateAcceptsSupportResistanceStopLossWithModule(t *testing.T) {
	cfg := validConfig()
	cfg.Modules = append(cfg.Modules, types.ModuleConfig{Module: "support_resistance", Params: map[string]any{}})
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance
	cfg.Risk.TakeProfitMode = types.RiskLevelModeSupportResistance

	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("support_resistance stop-loss/take-profit mode should pass validation when the module is configured: %v", err)
	}
}

func TestValidateRejectsSupportResistanceStopLossWithoutModule(t *testing.T) {
	cfg := validConfig() // only volume_breakout, no support_resistance
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance

	_, err := Validate(cfg, modules.NewDefaultRegistry())
	if err == nil {
		t.Fatal("stop loss set to support_resistance mode without configuring that module should be rejected")
	}
	if !strings.Contains(err.Error(), "support_resistance") {
		t.Errorf("error message should mention support_resistance, got: %v", err)
	}
}

func TestValidateRejectsSupportResistanceModeWithPctAlsoSet(t *testing.T) {
	cfg := validConfig()
	cfg.Modules = append(cfg.Modules, types.ModuleConfig{Module: "support_resistance", Params: map[string]any{}})
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance
	cfg.Risk.StopLossPct = 0.02 // both modes' values set at once, ambiguous

	_, err := Validate(cfg, modules.NewDefaultRegistry())
	if err == nil {
		t.Fatal("setting stop_loss_pct on top of support_resistance mode should be rejected, not silently pick one")
	}
}

func TestValidateRejectsUnknownRiskLevelMode(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.StopLossMode = types.RiskLevelMode("trailing")

	_, err := Validate(cfg, modules.NewDefaultRegistry())
	if err == nil {
		t.Fatal("unknown stop-loss mode should be rejected")
	}
}

func TestValidateAcceptsPOCStopLossWithModule(t *testing.T) {
	cfg := validConfig()
	cfg.Modules = append(cfg.Modules, types.ModuleConfig{Module: "poc", Params: map[string]any{}})
	cfg.Risk.StopLossMode = types.RiskLevelModePOC

	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("poc stop-loss mode should pass validation when the poc module is configured: %v", err)
	}
}

func TestValidateRejectsPOCStopLossWithoutModule(t *testing.T) {
	cfg := validConfig() // only volume_breakout, no poc
	cfg.Risk.StopLossMode = types.RiskLevelModePOC

	_, err := Validate(cfg, modules.NewDefaultRegistry())
	if err == nil {
		t.Fatal("stop loss set to poc mode without configuring that module should be rejected")
	}
	if !strings.Contains(err.Error(), "poc") {
		t.Errorf("error message should mention poc, got: %v", err)
	}
}

func TestValidateTakeProfitModeIndependentOfStopLossMode(t *testing.T) {
	// Stop loss uses a support level, take profit uses a fixed percentage —
	// the two fields are independent and mixing them is allowed.
	cfg := validConfig()
	cfg.Modules = append(cfg.Modules, types.ModuleConfig{Module: "support_resistance", Params: map[string]any{}})
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance
	cfg.Risk.TakeProfitPct = 0.05 // TakeProfitMode left empty, defaults to pct

	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("stop-loss and take-profit modes should be mixable independently: %v", err)
	}
}

func TestValidateRejectsModuleTimeframeFasterThanTrigger(t *testing.T) {
	cfg := validConfig()
	cfg.Timeframe = types.TF1h
	cfg.Modules[0].Timeframe = types.TF15m // faster than the trigger timeframe, would never be seen in time

	_, err := Validate(cfg, modules.NewDefaultRegistry())
	if err == nil {
		t.Fatal("a module timeframe faster than the trigger timeframe should be rejected")
	}
	if !strings.Contains(err.Error(), "更快") {
		t.Errorf("error message should explain the timeframe is faster than the trigger timeframe, got: %v", err)
	}
}

func TestValidateAcceptsModuleTimeframeSlowerThanTrigger(t *testing.T) {
	cfg := validConfig()
	cfg.Timeframe = types.TF15m
	cfg.Modules[0].Timeframe = types.TF1h // slower than the trigger timeframe, normal multi-timeframe usage

	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("a module timeframe slower than the trigger timeframe should pass validation: %v", err)
	}
}

func TestValidateAcceptsModuleTimeframeEqualToTrigger(t *testing.T) {
	cfg := validConfig()
	cfg.Timeframe = types.TF1h
	cfg.Modules[0].Timeframe = types.TF1h

	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("a module timeframe equal to the trigger timeframe should pass validation: %v", err)
	}
}

func TestValidateAcceptsEmptyModuleTimeframe(t *testing.T) {
	cfg := validConfig() // cfg.Modules[0].Timeframe left empty, follows the trigger timeframe
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("an empty module timeframe should pass validation: %v", err)
	}
}

func TestValidateRejectsUnknownModuleTimeframe(t *testing.T) {
	cfg := validConfig()
	cfg.Modules[0].Timeframe = types.Timeframe("3h")

	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err == nil {
		t.Fatal("an unsupported module timeframe should be rejected")
	}
}

// ---------- Position sizing mode (PositionSizingMode) ----------

func TestValidateAcceptsFixedQuoteSizingByDefault(t *testing.T) {
	cfg := validConfig() // PositionSizingMode left empty, defaults to fixed_quote
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("default fixed_quote position sizing mode should not be rejected: %v", err)
	}
}

func TestValidateRejectsUnknownPositionSizingMode(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.PositionSizingMode = types.PositionSizingMode("percent_of_moon")
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err == nil {
		t.Fatal("an unsupported position sizing mode should be rejected")
	}
}

func TestValidateRejectsFixedQuoteModeWithEquityFieldsSet(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.AccountEquityQuote = decimal.NewFromInt(10000) // sizing mode left empty but equity set, a mix
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err == nil {
		t.Fatal("fixed_quote mode should not allow account equity to be set at the same time")
	}
}

func TestValidateRejectsRiskPctSizingWithoutEquity(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.PositionSizingMode = types.PositionSizingModeRiskPct
	cfg.Risk.RiskPerTradePct = 0.01
	cfg.Risk.StopLossPct = 0.02
	// AccountEquityQuote left empty
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err == nil {
		t.Fatal("risk_pct mode without account equity should be rejected")
	}
}

func TestValidateRejectsRiskPctSizingWithBadPct(t *testing.T) {
	for _, pct := range []float64{0, -0.1, 1, 1.5} {
		cfg := validConfig()
		cfg.Risk.PositionSizingMode = types.PositionSizingModeRiskPct
		cfg.Risk.AccountEquityQuote = decimal.NewFromInt(10000)
		cfg.Risk.RiskPerTradePct = pct
		cfg.Risk.StopLossPct = 0.02
		if _, err := Validate(cfg, modules.NewDefaultRegistry()); err == nil {
			t.Errorf("risk_per_trade_pct=%v should be rejected (must fall within (0,1))", pct)
		}
	}
}

func TestValidateRejectsRiskPctSizingWithoutStopLoss(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.PositionSizingMode = types.PositionSizingModeRiskPct
	cfg.Risk.AccountEquityQuote = decimal.NewFromInt(10000)
	cfg.Risk.RiskPerTradePct = 0.01
	// StopLossPct left empty (0), and StopLossMode not set either, so the stop distance can't be computed
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err == nil {
		t.Fatal("risk_pct mode without any usable stop loss should be rejected, since the stop distance can't be computed")
	}
}

func TestValidateAcceptsRiskPctSizingWithPctStopLoss(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.PositionSizingMode = types.PositionSizingModeRiskPct
	cfg.Risk.AccountEquityQuote = decimal.NewFromInt(10000)
	cfg.Risk.RiskPerTradePct = 0.01
	cfg.Risk.StopLossPct = 0.02
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("risk_pct mode with a fixed-percentage stop loss should pass validation: %v", err)
	}
}

func TestValidateAcceptsRiskPctSizingWithSupportResistanceStopLoss(t *testing.T) {
	cfg := validConfig()
	cfg.Modules = append(cfg.Modules, types.ModuleConfig{Module: "support_resistance", Params: map[string]any{}})
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance
	cfg.Risk.PositionSizingMode = types.PositionSizingModeRiskPct
	cfg.Risk.AccountEquityQuote = decimal.NewFromInt(10000)
	cfg.Risk.RiskPerTradePct = 0.01
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("risk_pct mode with a support_resistance stop loss should pass validation: %v", err)
	}
}
