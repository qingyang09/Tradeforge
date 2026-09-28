package modules

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

func TestDefaultRegistryContainsAllModules(t *testing.T) {
	r := NewDefaultRegistry()
	want := []string{"cvd_orderflow", "fakeout", "macd_rsi", "news_sentiment", "poc", "support_resistance", "volume_breakout"}
	got := r.Names()
	if len(got) != len(want) {
		t.Fatalf("registered modules = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q, want %q (output should be stably sorted)", i, got[i], want[i])
		}
	}
}

// Referencing a nonexistent module must error and list the available options,
// so an Agent-hallucinated module name surfaces during config validation.
func TestGetUnknownModuleListsAvailable(t *testing.T) {
	r := NewDefaultRegistry()
	_, err := r.Get("moving_average_cross")
	if err == nil {
		t.Fatal("expected an error for an unknown module")
	}
	var ume *UnknownModuleError
	if !asUnknown(err, &ume) {
		t.Fatalf("expected *UnknownModuleError, got %T", err)
	}
	if len(ume.Available) == 0 {
		t.Error("the error must include the list of available modules")
	}
}

func asUnknown(err error, target **UnknownModuleError) bool {
	u, ok := err.(*UnknownModuleError)
	if ok {
		*target = u
	}
	return ok
}

// Every module's parameter spec must be self-consistent: it must have a
// default, the default must be in range, and it must pass the module's own
// validation.
func TestEveryModuleHasUsableDefaults(t *testing.T) {
	for _, m := range NewDefaultRegistry().All() {
		t.Run(m.Name(), func(t *testing.T) {
			specs := m.RequiredParams()
			if len(specs) == 0 {
				t.Fatal("module declares no parameters")
			}
			for _, s := range specs {
				if s.Description == "" {
					t.Errorf("param %q has no description, the Agent can't understand its meaning from this", s.Name)
				}
				if !s.Required && s.Default == nil {
					t.Errorf("param %q is neither required nor has a default", s.Name)
				}
			}
			// Empty params should be filled in by defaults and pass validation.
			if _, err := ResolveParams(m, map[string]any{}); err != nil {
				t.Errorf("default params failed their own validation: %v", err)
			}
		})
	}
}

// Every module must return a neutral signal for empty market data, not panic or error.
func TestEveryModuleHandlesEmptyData(t *testing.T) {
	for _, m := range NewDefaultRegistry().All() {
		t.Run(m.Name(), func(t *testing.T) {
			sig, err := m.Evaluate(context.Background(), types.MarketData{Symbol: "BTCUSDT"}, nil)
			if err != nil {
				t.Fatalf("empty market data should not error, got: %v", err)
			}
			if sig.Direction != types.DirectionNeutral {
				t.Errorf("Direction = %s, want NEUTRAL", sig.Direction)
			}
			if sig.Module != m.Name() {
				t.Errorf("Signal.Module = %q, want %q", sig.Module, m.Name())
			}
		})
	}
}

// Even a neutral output must carry the reference price and timestamp at the
// time it was computed: audit and replay need to know "what the module saw
// on this candle", and price=0 would pollute downstream records.
func TestNeutralSignalsStillCarryPriceAndTime(t *testing.T) {
	md := flatMarketData(300, 100, 1000)
	for _, m := range NewDefaultRegistry().All() {
		t.Run(m.Name(), func(t *testing.T) {
			sig, err := m.Evaluate(context.Background(), md, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sig.Direction != types.DirectionNeutral {
				t.Skipf("module produced a %s signal on this data; this test only covers neutral output", sig.Direction)
			}
			if !sig.Price.IsPositive() {
				t.Errorf("neutral signal Price = %s, want it to carry the closing price at the time", sig.Price)
			}
			if sig.Timestamp.IsZero() {
				t.Error("neutral signal Timestamp must not be zero")
			}
		})
	}
}

// flatMarketData builds a perfectly flat market: unmoving price, even volume,
// buy/sell split evenly. No module should produce a directional signal on it.
func flatMarketData(n int, price, volume float64) types.MarketData {
	md := types.MarketData{Symbol: "BTCUSDT", Timeframe: types.TF1h}
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	p := decimal.NewFromFloat(price)
	v := decimal.NewFromFloat(volume)
	for i := 0; i < n; i++ {
		openTime := t0.Add(time.Duration(i) * time.Hour)
		md.Candles = append(md.Candles, types.Candle{
			OpenTime:       openTime,
			CloseTime:      openTime.Add(time.Hour),
			Open:           p,
			High:           p,
			Low:            p,
			Close:          p,
			Volume:         v,
			TakerBuyVolume: v.Div(decimal.NewFromInt(2)),
		})
	}
	return md
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering the same module name twice should panic")
		}
	}()
	r := NewDefaultRegistry()
	r.Register(NewDefaultRegistry().All()[0])
}
