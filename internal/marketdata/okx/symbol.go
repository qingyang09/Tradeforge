package okx

import (
	"fmt"
	"strings"

	"tradeforge/pkg/types"
)

// knownQuoteCurrencies lists common quote-currency suffixes in check order,
// used to split the project's internal no-separator notation (e.g.
// "BTCUSDT") into OKX's required hyphenated notation ("BTC-USDT").
//
// Order matters: USDT must come before USD, otherwise "BTCUSDT" would be
// matched by USD first and wrongly split into "BTCUS"+"DT" — checking longer
// suffixes first avoids being preempted by a shorter suffix.
var knownQuoteCurrencies = []string{"USDT", "USDC", "BTC", "ETH", "USD"}

// ToInstID converts the project's internal symbol notation to OKX's instId
// notation.
//
// Errors outright when no known quote currency matches, rather than guessing
// — an incorrect split would send the request to a semantically wrong
// instId, and it might not even error (e.g. it happens to collide with
// another real trading pair). That "looks like it works but is wrong"
// outcome is more dangerous than an explicit rejection.
func ToInstID(symbol string) (string, error) {
	up := strings.ToUpper(symbol)
	for _, quote := range knownQuoteCurrencies {
		if strings.HasSuffix(up, quote) && len(up) > len(quote) {
			base := up[:len(up)-len(quote)]
			return base + "-" + quote, nil
		}
	}
	return "", fmt.Errorf("cannot infer an OKX instId from symbol %q: use a notation like BTCUSDT "+
		"(known quote currencies: %s)", symbol, strings.Join(knownQuoteCurrencies, "/"))
}

// barByTimeframe maps types.Timeframe to OKX's bar parameter.
//
// The casing is required by the OKX API itself (verified against real
// requests: lowercase m for minutes, uppercase H/D for hours/days; passing
// "1h" is rejected outright) — it's not a stylistic choice made here.
var barByTimeframe = map[types.Timeframe]string{
	types.TF1m:  "1m",
	types.TF5m:  "5m",
	types.TF15m: "15m",
	types.TF1h:  "1H",
	types.TF4h:  "4H",
	types.TF1d:  "1D",
}

// toBar converts a types.Timeframe to OKX's bar parameter.
func toBar(tf types.Timeframe) (string, error) {
	bar, ok := barByTimeframe[tf]
	if !ok {
		return "", fmt.Errorf("OKX data source does not support timeframe %q", tf)
	}
	return bar, nil
}
