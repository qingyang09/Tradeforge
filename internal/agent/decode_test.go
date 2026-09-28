package agent

import (
	"testing"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

func TestDecodeStrategyConfigJSONParsesDecimalSafeAmounts(t *testing.T) {
	blob := MustJSON(map[string]any{
		"name": "画板策略", "symbol": "btcusdt", "timeframe": "1h", "combine": "ALL",
		"modules": []any{map[string]any{
			"module": "volume_breakout",
			"params": map[string]any{"multiplier": 2.5},
		}},
		"risk": map[string]any{
			"max_position_size_quote": "1000.123456789012345678",
			"max_daily_loss_quote":    "200",
			"stop_loss_pct":           0.02,
		},
	})

	cfg, err := DecodeStrategyConfigJSON([]byte(blob))
	if err != nil {
		t.Fatalf("DecodeStrategyConfigJSON() 失败：%v", err)
	}
	if cfg.Symbol != "BTCUSDT" {
		t.Errorf("Symbol 应被规范化为大写，实际 %q", cfg.Symbol)
	}
	want := decimal.RequireFromString("1000.123456789012345678")
	if !cfg.Risk.MaxPositionSizeQuote.Equal(want) {
		t.Errorf("MaxPositionSizeQuote = %s，期望 %s（不能经过 float64 丢精度）",
			cfg.Risk.MaxPositionSizeQuote, want)
	}
	if len(cfg.Modules) != 1 || cfg.Modules[0].Module != "volume_breakout" {
		t.Fatalf("模块 = %+v", cfg.Modules)
	}
}

func TestDecodeStrategyConfigJSONParsesSupportResistanceRiskModes(t *testing.T) {
	blob := MustJSON(map[string]any{
		"name": "支撑位止损", "symbol": "ETHUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": []any{
			map[string]any{"module": "volume_breakout", "params": map[string]any{"multiplier": 1.5}},
			map[string]any{"module": "support_resistance", "params": map[string]any{}},
		},
		"risk": map[string]any{
			"max_position_size_quote": "1000",
			"stop_loss_mode":          "support_resistance",
			"take_profit_mode":        "support_resistance",
		},
	})

	cfg, err := DecodeStrategyConfigJSON([]byte(blob))
	if err != nil {
		t.Fatalf("DecodeStrategyConfigJSON() 失败：%v", err)
	}
	if cfg.Risk.StopLossMode != types.RiskLevelModeSupportResistance {
		t.Errorf("StopLossMode = %q，期望 support_resistance", cfg.Risk.StopLossMode)
	}
	if cfg.Risk.TakeProfitMode != types.RiskLevelModeSupportResistance {
		t.Errorf("TakeProfitMode = %q，期望 support_resistance", cfg.Risk.TakeProfitMode)
	}
}

func TestDecodeStrategyConfigJSONRejectsUnknownFields(t *testing.T) {
	blob := `{"name":"x","symbol":"BTCUSDT","timeframe":"1h","combine":"ALL","modules":[],"risk":{},"bogus_field":1}`
	if _, err := DecodeStrategyConfigJSON([]byte(blob)); err == nil {
		t.Fatal("未知字段应被拒绝，不能静默忽略")
	}
}

func TestDecodeStrategyConfigJSONRejectsMalformedJSON(t *testing.T) {
	if _, err := DecodeStrategyConfigJSON([]byte("不是 JSON")); err == nil {
		t.Fatal("非法 JSON 应被拒绝")
	}
}
