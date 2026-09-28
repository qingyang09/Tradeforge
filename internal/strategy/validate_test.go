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
		t.Fatalf("默认 pct 模式的止损不应被拒绝：%v", err)
	}
}

func TestValidateAcceptsSupportResistanceStopLossWithModule(t *testing.T) {
	cfg := validConfig()
	cfg.Modules = append(cfg.Modules, types.ModuleConfig{Module: "support_resistance", Params: map[string]any{}})
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance
	cfg.Risk.TakeProfitMode = types.RiskLevelModeSupportResistance

	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("配了 support_resistance 模块时，support_resistance 止损止盈模式应通过校验：%v", err)
	}
}

func TestValidateRejectsSupportResistanceStopLossWithoutModule(t *testing.T) {
	cfg := validConfig() // 只有 volume_breakout，没有 support_resistance
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance

	_, err := Validate(cfg, modules.NewDefaultRegistry())
	if err == nil {
		t.Fatal("止损设为 support_resistance 模式但没有配该模块，应该被拒绝")
	}
	if !strings.Contains(err.Error(), "support_resistance") {
		t.Errorf("错误信息应提到 support_resistance，实际：%v", err)
	}
}

func TestValidateRejectsSupportResistanceModeWithPctAlsoSet(t *testing.T) {
	cfg := validConfig()
	cfg.Modules = append(cfg.Modules, types.ModuleConfig{Module: "support_resistance", Params: map[string]any{}})
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance
	cfg.Risk.StopLossPct = 0.02 // 同时设置了两种模式的值，语义不清

	_, err := Validate(cfg, modules.NewDefaultRegistry())
	if err == nil {
		t.Fatal("support_resistance 模式下又设置了 stop_loss_pct，应该被拒绝，而不是悄悄选一个生效")
	}
}

func TestValidateRejectsUnknownRiskLevelMode(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.StopLossMode = types.RiskLevelMode("trailing")

	_, err := Validate(cfg, modules.NewDefaultRegistry())
	if err == nil {
		t.Fatal("未知的止损模式应该被拒绝")
	}
}

func TestValidateAcceptsPOCStopLossWithModule(t *testing.T) {
	cfg := validConfig()
	cfg.Modules = append(cfg.Modules, types.ModuleConfig{Module: "poc", Params: map[string]any{}})
	cfg.Risk.StopLossMode = types.RiskLevelModePOC

	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("配了 poc 模块时，poc 止损模式应通过校验：%v", err)
	}
}

func TestValidateRejectsPOCStopLossWithoutModule(t *testing.T) {
	cfg := validConfig() // 只有 volume_breakout，没有 poc
	cfg.Risk.StopLossMode = types.RiskLevelModePOC

	_, err := Validate(cfg, modules.NewDefaultRegistry())
	if err == nil {
		t.Fatal("止损设为 poc 模式但没有配该模块，应该被拒绝")
	}
	if !strings.Contains(err.Error(), "poc") {
		t.Errorf("错误信息应提到 poc，实际：%v", err)
	}
}

func TestValidateTakeProfitModeIndependentOfStopLossMode(t *testing.T) {
	// 止损用支撑位、止盈用固定百分比——两个字段互相独立，允许混搭。
	cfg := validConfig()
	cfg.Modules = append(cfg.Modules, types.ModuleConfig{Module: "support_resistance", Params: map[string]any{}})
	cfg.Risk.StopLossMode = types.RiskLevelModeSupportResistance
	cfg.Risk.TakeProfitPct = 0.05 // TakeProfitMode 留空，默认 pct

	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("止损止盈模式应该可以独立混搭：%v", err)
	}
}

func TestValidateRejectsModuleTimeframeFasterThanTrigger(t *testing.T) {
	cfg := validConfig()
	cfg.Timeframe = types.TF1h
	cfg.Modules[0].Timeframe = types.TF15m // 比触发周期还快，永远来不及被看到

	_, err := Validate(cfg, modules.NewDefaultRegistry())
	if err == nil {
		t.Fatal("模块周期比触发周期更快应该被拒绝")
	}
	if !strings.Contains(err.Error(), "更快") {
		t.Errorf("错误信息应说明周期比触发周期更快，实际：%v", err)
	}
}

func TestValidateAcceptsModuleTimeframeSlowerThanTrigger(t *testing.T) {
	cfg := validConfig()
	cfg.Timeframe = types.TF15m
	cfg.Modules[0].Timeframe = types.TF1h // 比触发周期慢，正常的多周期用法

	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("模块周期比触发周期慢应该通过校验：%v", err)
	}
}

func TestValidateAcceptsModuleTimeframeEqualToTrigger(t *testing.T) {
	cfg := validConfig()
	cfg.Timeframe = types.TF1h
	cfg.Modules[0].Timeframe = types.TF1h

	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("模块周期与触发周期相同应该通过校验：%v", err)
	}
}

func TestValidateAcceptsEmptyModuleTimeframe(t *testing.T) {
	cfg := validConfig() // cfg.Modules[0].Timeframe 留空，跟随触发周期
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("留空的模块周期应该通过校验：%v", err)
	}
}

func TestValidateRejectsUnknownModuleTimeframe(t *testing.T) {
	cfg := validConfig()
	cfg.Modules[0].Timeframe = types.Timeframe("3h")

	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err == nil {
		t.Fatal("不受支持的模块周期应该被拒绝")
	}
}

// ---------- 仓位模式（PositionSizingMode） ----------

func TestValidateAcceptsFixedQuoteSizingByDefault(t *testing.T) {
	cfg := validConfig() // PositionSizingMode 留空，默认 fixed_quote
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("默认 fixed_quote 仓位模式不应该被拒绝：%v", err)
	}
}

func TestValidateRejectsUnknownPositionSizingMode(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.PositionSizingMode = types.PositionSizingMode("percent_of_moon")
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err == nil {
		t.Fatal("不受支持的仓位模式应该被拒绝")
	}
}

func TestValidateRejectsFixedQuoteModeWithEquityFieldsSet(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.AccountEquityQuote = decimal.NewFromInt(10000) // 留空仓位模式却填了权益，混填了
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err == nil {
		t.Fatal("fixed_quote 模式下不该允许同时填账户权益")
	}
}

func TestValidateRejectsRiskPctSizingWithoutEquity(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.PositionSizingMode = types.PositionSizingModeRiskPct
	cfg.Risk.RiskPerTradePct = 0.01
	cfg.Risk.StopLossPct = 0.02
	// AccountEquityQuote 留空
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err == nil {
		t.Fatal("risk_pct 模式下没有账户权益应该被拒绝")
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
			t.Errorf("risk_per_trade_pct=%v 应该被拒绝（必须落在 (0,1)）", pct)
		}
	}
}

func TestValidateRejectsRiskPctSizingWithoutStopLoss(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.PositionSizingMode = types.PositionSizingModeRiskPct
	cfg.Risk.AccountEquityQuote = decimal.NewFromInt(10000)
	cfg.Risk.RiskPerTradePct = 0.01
	// StopLossPct 留空（0），也没设 StopLossMode，算不出止损距离
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err == nil {
		t.Fatal("risk_pct 模式下没有可用止损应该被拒绝，否则算不出止损距离")
	}
}

func TestValidateAcceptsRiskPctSizingWithPctStopLoss(t *testing.T) {
	cfg := validConfig()
	cfg.Risk.PositionSizingMode = types.PositionSizingModeRiskPct
	cfg.Risk.AccountEquityQuote = decimal.NewFromInt(10000)
	cfg.Risk.RiskPerTradePct = 0.01
	cfg.Risk.StopLossPct = 0.02
	if _, err := Validate(cfg, modules.NewDefaultRegistry()); err != nil {
		t.Fatalf("risk_pct 模式配合固定百分比止损应该通过校验：%v", err)
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
		t.Fatalf("risk_pct 模式配合 support_resistance 止损应该通过校验：%v", err)
	}
}
