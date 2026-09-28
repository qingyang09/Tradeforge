package okx

import (
	"testing"

	"tradeforge/pkg/types"
)

func TestToInstID(t *testing.T) {
	cases := []struct {
		symbol string
		want   string
	}{
		{"BTCUSDT", "BTC-USDT"},
		{"ETHUSDT", "ETH-USDT"},
		{"btcusdt", "BTC-USDT"}, // case-insensitive
		{"ETHBTC", "ETH-BTC"},
		{"ETHUSDC", "ETH-USDC"},
	}
	for _, tc := range cases {
		t.Run(tc.symbol, func(t *testing.T) {
			got, err := ToInstID(tc.symbol)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("ToInstID(%q) = %q, want %q", tc.symbol, got, tc.want)
			}
		})
	}
}

// USDT must be matched before USD, otherwise "ETHUSDT" would be wrongly
// split on the USD suffix, leaving a nonsense "T" prefix (or worse:
// splitting out a wrong trading pair that happens to exist).
func TestToInstIDPrefersLongerQuoteSuffix(t *testing.T) {
	got, err := ToInstID("ETHUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ETH-USDT" {
		t.Errorf("ToInstID(ETHUSDT) = %q, want ETH-USDT (must not be preempted by USD)", got)
	}
}

func TestToInstIDRejectsUnknownQuote(t *testing.T) {
	if _, err := ToInstID("XYZABC"); err == nil {
		t.Fatal("should error when the quote currency isn't recognized, not guess")
	}
}

func TestToBar(t *testing.T) {
	cases := []struct {
		tf   types.Timeframe
		want string
	}{
		{types.TF1m, "1m"},
		{types.TF5m, "5m"},
		{types.TF15m, "15m"},
		{types.TF1h, "1H"},
		{types.TF4h, "4H"},
		{types.TF1d, "1D"},
	}
	for _, tc := range cases {
		t.Run(string(tc.tf), func(t *testing.T) {
			got, err := toBar(tc.tf)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("toBar(%s) = %q, want %q", tc.tf, got, tc.want)
			}
		})
	}
}

func TestToBarRejectsUnknownTimeframe(t *testing.T) {
	if _, err := toBar(types.Timeframe("2h")); err == nil {
		t.Fatal("an unsupported timeframe should error")
	}
}
