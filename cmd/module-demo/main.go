// Command module-demo runs each signal module against constructed fake
// market data and prints the full Signal struct.
//
// Its purpose is manual sanity-checking of module logic — unit tests assert
// fixed expected behavior, while this command lets a human see directly
// "what market data produces what signal."
//
// Usage: go run ./cmd/module-demo
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/internal/modules"
	"tradeforge/internal/modules/cvdorderflow"
	"tradeforge/internal/modules/macdrsi"
	"tradeforge/internal/modules/newssentiment"
	"tradeforge/internal/modules/supportresistance"
	"tradeforge/internal/modules/volumebreakout"
	"tradeforge/pkg/types"
)

var start = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

type scenario struct {
	name   string
	module string
	params map[string]any
	data   types.MarketData
}

func main() {
	reg := modules.NewDefaultRegistry()

	fmt.Println("Registered modules:")
	for _, m := range reg.All() {
		fmt.Printf("  - %-20s %s\n", m.Name(), m.Description())
		for _, p := range m.RequiredParams() {
			fmt.Printf("      %-22s %-7s default %-8v range %s\n",
				p.Name, p.Type, p.Default, p.AllowedDesc())
		}
	}

	for _, sc := range allScenarios() {
		fmt.Printf("\n%s\nScenario: %s\nModule: %s  Params: %v\n%s\n",
			strings.Repeat("=", 78), sc.name, sc.module, sc.params, strings.Repeat("-", 78))

		m, err := reg.Get(sc.module)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to get module: %v\n", err)
			os.Exit(1)
		}
		sig, err := m.Evaluate(context.Background(), sc.data, sc.params)
		if err != nil {
			fmt.Printf("Evaluate returned an error: %v\n", err)
			continue
		}
		b, _ := json.MarshalIndent(sig, "", "  ")
		fmt.Println(string(b))
	}
}

func allScenarios() []scenario {
	return []scenario{
		{
			name:   "Oscillates between 100-110, then breaks above resistance",
			module: supportresistance.ModuleName,
			params: map[string]any{"pivot_strength": 1, "min_touches": 2},
			data: synth.New("BTCUSDT", types.TF1h, start).
				Oscillate(64, 100, 110, 1000).
				AddBar(110, 113, 1500, 0.65).
				Build(),
		},
		{
			name:   "Monotonic uptrend, no repeatedly-touched key level",
			module: supportresistance.ModuleName,
			params: map[string]any{"min_touches": 3},
			data: synth.New("BTCUSDT", types.TF1h, start).
				Trend(80, 100, 200, 1000, 0.6).
				Build(),
		},
		{
			name:   "Flat volume range, then a bullish candle with 3x volume",
			module: volumebreakout.ModuleName,
			params: map[string]any{"window": 20, "multiplier": 2.0},
			data: flatVolume(20, 100, 1000).
				AddBar(100, 105, 3000, 0.7).
				Build(),
		},
		{
			name:   "Volume spike but only 1.5x, below threshold",
			module: volumebreakout.ModuleName,
			params: map[string]any{"window": 20, "multiplier": 2.0},
			data: flatVolume(20, 100, 1000).
				AddBar(100, 120, 1500, 0.9).
				Build(),
		},
		{
			name:   "Sustained buy-side pressure dominant (CVD bullish imbalance)",
			module: cvdorderflow.ModuleName,
			params: map[string]any{"window": 50, "detect": cvdorderflow.DetectImbalance},
			data:   repeatBar(60, 100, 100.1, 1000, 0.8),
		},
		{
			name:   "Price rising but buy-side flow net outflow (CVD bearish divergence)",
			module: cvdorderflow.ModuleName,
			params: map[string]any{"window": 50, "detect": cvdorderflow.DetectBoth},
			data: synth.New("BTCUSDT", types.TF1h, start).
				Trend(60, 100, 120, 1000, 0.35).
				Build(),
		},
		{
			name:   "Sharp drop followed by a bounce strong enough to push RSI into oversold and back — RSI reversal mode captures a bullish signal",
			module: macdrsi.ModuleName,
			params: map[string]any{"mode": macdrsi.ModeRSIReversal},
			data:   truncateAtFirstSignal(driftWalkReversal(30, -2.0, 2.0, 0.8), macdrsi.ModuleName, map[string]any{"mode": macdrsi.ModeRSIReversal}),
		},
		{
			name:   "MACD death cross during a downtrend (macd_cross mode fires regardless of how oversold RSI already is)",
			module: macdrsi.ModuleName,
			params: map[string]any{"mode": macdrsi.ModeMACDCross},
			data:   truncateAtFirstSignal(driftWalkReversal(80, -1.2, 1.2, 1.0), macdrsi.ModuleName, map[string]any{"mode": macdrsi.ModeMACDCross}),
		},
		{
			name:   "Same death-cross candle switched to confluence mode: RSI has already dropped through oversold, judged as exhausted momentum, this bearish signal gets filtered out",
			module: macdrsi.ModuleName,
			params: map[string]any{"mode": macdrsi.ModeConfluence},
			data:   truncateAtFirstSignal(driftWalkReversal(80, -1.2, 1.2, 1.0), macdrsi.ModuleName, map[string]any{"mode": macdrsi.ModeMACDCross}),
		},
		{
			name:   "Two positive news items within 24 hours (ETF approval + fund inflow), weighted sentiment score crosses threshold",
			module: newssentiment.ModuleName,
			params: map[string]any{},
			data: newsMarketData("BTCUSDT",
				newsItem(1, "Bitcoin ETF approval sparks massive rally", "BTC"),
				newsItem(6, "Analysts note strong adoption and inflow into BTC funds", "BTC"),
			),
		},
		{
			name:   "Exchange hacked plus regulatory lawsuit, weighted sentiment score triggers bearish signal",
			module: newssentiment.ModuleName,
			params: map[string]any{},
			data: newsMarketData("BTCUSDT",
				newsItem(2, "Exchange hacked, funds stolen amid exploit", "BTC"),
				newsItem(4, "Regulators file lawsuit alleging fraud and ban trading", "BTC"),
			),
		},
		{
			name:   "Headline sentiment is extreme but the symbol is ETH, unrelated to the BTCUSDT strategy, filtered out and not counted",
			module: newssentiment.ModuleName,
			params: map[string]any{},
			data: newsMarketData("BTCUSDT",
				newsItem(1, "Ethereum ETF approval sparks massive rally", "ETH"),
			),
		},
	}
}

// truncateAtFirstSignal replays the market data with a growing window, one
// candle at a time, and truncates it at the first candle where the module
// produces a non-neutral signal.
//
// Calling Evaluate once on the full sequence only shows the state of the
// last candle — if the crossover happened partway through, the last candle
// has often already settled back to neutral. The point of the demo is to
// let a human see the "trigger moment" at a glance, so it needs to be
// truncated right there.
func truncateAtFirstSignal(full types.MarketData, moduleName string, params map[string]any) types.MarketData {
	reg := modules.NewDefaultRegistry()
	m, err := reg.Get(moduleName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "truncateAtFirstSignal: %v\n", err)
		os.Exit(1)
	}
	for k := 1; k <= len(full.Candles); k++ {
		md := types.MarketData{Symbol: full.Symbol, Timeframe: full.Timeframe, Candles: full.Candles[:k]}
		sig, err := m.Evaluate(context.Background(), md, params)
		if err != nil {
			continue // param-validation errors during the insufficient-data phase don't affect later, longer-window evaluations
		}
		if sig.Direction != types.DirectionNeutral {
			return md
		}
	}
	return full // fall back to the full sequence if nothing was found, so at least the final state is visible
}

// driftWalkReversal constructs a reversal market using a pseudo-random walk
// with drift: n candles following drift1, then n candles following drift2
// (drift1 negative and drift2 positive means down first, then up).
//
// A pure linear trend (synth.Builder.Trend) makes the MACD line converge to
// a near-constant value that almost overlaps the signal line within a few
// dozen candles — after that, the difference is just floating-point noise
// and doesn't represent a real momentum shift. Adding noise keeps indicator
// behavior close to real market data, which is needed to demonstrate a
// meaningful golden cross / death cross / RSI reversal. The seed is fixed
// so results are reproducible.
func driftWalkReversal(n int, drift1, drift2, noise float64) types.MarketData {
	b := synth.New("BTCUSDT", types.TF1h, start)
	takerA, takerB := 0.35, 0.65
	price := 150.0
	rng := rand.New(rand.NewSource(42))
	walk := func(steps int, drift, takerBuyRatio float64) {
		for i := 0; i < steps; i++ {
			next := price + drift + (rng.Float64()-0.5)*2*noise
			b.AddBar(price, next, 1000, takerBuyRatio)
			price = next
		}
	}
	walk(n, drift1, takerA)
	walk(n, drift2, takerB)
	return b.Build()
}

func flatVolume(n int, price, volume float64) *synth.Builder {
	b := synth.New("BTCUSDT", types.TF1h, start)
	for i := 0; i < n; i++ {
		b.AddBar(price, price, volume, 0.5)
	}
	return b
}

func repeatBar(n int, open, close, volume, takerRatio float64) types.MarketData {
	b := synth.New("BTCUSDT", types.TF1h, start)
	for i := 0; i < n; i++ {
		b.AddBar(open, close, volume, takerRatio)
	}
	return b.Build()
}

// newsMarketData constructs a single flat candle (news_sentiment only cares
// about the News field, not price action) plus the given news items; the
// close time of that last candle is the reference point for news timestamps.
func newsMarketData(symbol string, news ...types.NewsItem) types.MarketData {
	md := synth.New(symbol, types.TF1h, start).AddFlat(100, 1000).Build()
	md.News = news
	return md
}

// newsItem constructs a news item published "hoursAgo hours ago" (relative
// to the close time of the last candle).
func newsItem(hoursAgo float64, headline string, symbols ...string) types.NewsItem {
	closeTime := start.Add(types.TF1h.Duration())
	return types.NewsItem{
		Source: "demo", Headline: headline,
		PublishedAt: closeTime.Add(-time.Duration(hoursAgo * float64(time.Hour))),
		Symbols:     symbols,
	}
}
