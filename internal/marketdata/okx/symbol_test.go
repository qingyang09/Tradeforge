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
		{"btcusdt", "BTC-USDT"}, // 大小写不敏感
		{"ETHBTC", "ETH-BTC"},
		{"ETHUSDC", "ETH-USDC"},
	}
	for _, tc := range cases {
		t.Run(tc.symbol, func(t *testing.T) {
			got, err := ToInstID(tc.symbol)
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if got != tc.want {
				t.Errorf("ToInstID(%q) = %q，期望 %q", tc.symbol, got, tc.want)
			}
		})
	}
}

// USDT 必须比 USD 优先匹配，否则 "ETHUSDT" 会被错误切成 "ETHUSDT" 的 USD 后缀，
// 剩下一个不成词的 "T" 前缀（或者更糟：切出一个凑巧存在的错误交易对）。
func TestToInstIDPrefersLongerQuoteSuffix(t *testing.T) {
	got, err := ToInstID("ETHUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ETH-USDT" {
		t.Errorf("ToInstID(ETHUSDT) = %q，期望 ETH-USDT（不能被 USD 抢先匹配）", got)
	}
}

func TestToInstIDRejectsUnknownQuote(t *testing.T) {
	if _, err := ToInstID("XYZABC"); err == nil {
		t.Fatal("无法识别计价货币时应报错，不能瞎猜")
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
				t.Fatalf("意外错误：%v", err)
			}
			if got != tc.want {
				t.Errorf("toBar(%s) = %q，期望 %q", tc.tf, got, tc.want)
			}
		})
	}
}

func TestToBarRejectsUnknownTimeframe(t *testing.T) {
	if _, err := toBar(types.Timeframe("2h")); err == nil {
		t.Fatal("不支持的周期应报错")
	}
}
