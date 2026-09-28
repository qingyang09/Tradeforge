package types

import (
	"reflect"
	"testing"
)

func TestRequiredTimeframesDedupsAndIncludesTrigger(t *testing.T) {
	cfg := StrategyConfig{
		Timeframe: TF15m,
		Modules: []ModuleConfig{
			{Module: "fakeout", Timeframe: TF1h},
			{Module: "poc", Timeframe: TF1h},
			{Module: "volume_breakout"}, // left empty, follows the trigger timeframe
		},
	}
	got := cfg.RequiredTimeframes()
	want := []Timeframe{TF15m, TF1h}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RequiredTimeframes() = %v, expected %v", got, want)
	}
}

func TestRequiredTimeframesSingleTimeframeStrategy(t *testing.T) {
	cfg := StrategyConfig{
		Timeframe: TF1h,
		Modules: []ModuleConfig{
			{Module: "support_resistance"},
			{Module: "volume_breakout"},
		},
	}
	got := cfg.RequiredTimeframes()
	want := []Timeframe{TF1h}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RequiredTimeframes() = %v, expected %v", got, want)
	}
}

func TestRequiredTimeframesNoModules(t *testing.T) {
	cfg := StrategyConfig{Timeframe: TF4h}
	got := cfg.RequiredTimeframes()
	want := []Timeframe{TF4h}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RequiredTimeframes() = %v, expected %v", got, want)
	}
}
