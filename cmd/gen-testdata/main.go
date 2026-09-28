// Command gen-testdata generates the sample data needed for an end-to-end
// backtest: a synthetic candle CSV and a strategy config JSON.
//
// Synthetic rather than real data is intentional: what the end-to-end test
// needs to verify is "does the pipeline run end to end, do the key points
// trigger as expected" — reproducible data is what lets us hard-code the
// assertions. Wiring up real exchange data is a separate concern that
// belongs to the data-source modules.
//
// Usage: go run ./cmd/gen-testdata -dir testdata
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata"
	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

func main() {
	dir := flag.String("dir", "testdata", "output directory")
	seed := flag.Int64("seed", 20250101, "random walk seed; a fixed seed keeps results reproducible")
	flag.Parse()

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fatal("failed to create directory: %v", err)
	}

	md := buildMarketData(*seed)
	csvPath := filepath.Join(*dir, "btcusdt_1h.csv")
	f, err := os.Create(csvPath)
	if err != nil {
		fatal("failed to create CSV: %v", err)
	}
	if err := marketdata.WriteCSV(f, md); err != nil {
		f.Close()
		fatal("failed to write CSV: %v", err)
	}
	f.Close()

	cfg := buildStrategy()
	jsonPath := filepath.Join(*dir, "strategy.json")
	blob, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fatal("failed to serialize strategy: %v", err)
	}
	if err := os.WriteFile(jsonPath, blob, 0o644); err != nil {
		fatal("failed to write strategy file: %v", err)
	}

	fmt.Printf("Generated:\n  %s (%d candles, %s ~ %s)\n  %s\n",
		csvPath, len(md.Candles),
		md.Candles[0].OpenTime.Format("2006-01-02"),
		md.Candles[len(md.Candles)-1].CloseTime.Format("2006-01-02"),
		jsonPath)
}

// buildMarketData constructs a market with a clearly defined structure so
// the end-to-end assertions have something solid to check against.
//
// It deliberately sets up two identical trigger patterns, one falling
// in-sample and one out-of-sample (with the default 70/30 split, the cut
// point is at bar 350):
//
//	  0-199: oscillates between 100-110, forming a key level that's
//	         touched repeatedly
//	200-218: a push up with buy-side pressure dominant, driving up the
//	         CVD imbalance
//	    219: 3x volume spike breaking above 110  <- trigger #1 (in-sample)
//	220-279: random walk
//	280-399: oscillates between 120-130, forming a new key level
//	400-418: another push up with buy-side pressure dominant
//	    419: 3x volume spike breaking above 130  <- trigger #2 (out-of-sample)
//	420-499: random walk
func buildMarketData(seed int64) types.MarketData {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	b := synth.New("BTCUSDT", types.TF1h, start)

	b.Oscillate(200, 100, 110, 1000)
	b.Trend(19, 100, 110, 1000, 0.85)
	b.AddBar(110, 113, 3000, 0.85) // trigger #1, index 219
	b.RandomWalk(60, 113, 0.008, 1000, seed)

	b.Oscillate(120, 120, 130, 1000)
	b.Trend(19, 120, 130, 1000, 0.85)
	b.AddBar(130, 134, 3000, 0.85) // trigger #2, index 419
	b.RandomWalk(80, 134, 0.008, 1000, seed+1)

	return b.Build()
}

// buildStrategy is a "three modules combined with ALL" strategy config,
// shaped the same way as output from the Agent translation layer.
func buildStrategy() types.StrategyConfig {
	return types.StrategyConfig{
		ID:        "9f8e7d6c-5b4a-4392-8180-1a2b3c4d5e6f",
		Name:      "BTC breakout + volume + orderflow triple confirmation",
		Symbol:    "BTCUSDT",
		Timeframe: types.TF1h,
		Combine:   types.CombineAll,
		Modules: []types.ModuleConfig{
			{Module: "support_resistance", Params: map[string]any{
				"pivot_strength": 1, "min_touches": 2,
			}},
			{Module: "volume_breakout", Params: map[string]any{
				"window": 20, "multiplier": 2.0,
			}},
			{Module: "cvd_orderflow", Params: map[string]any{
				"window": 50, "imbalance_threshold": 0.2,
			}},
		},
		Risk: types.RiskConfig{
			MaxPositionSizeQuote: decimal.NewFromInt(1000),
			MaxDailyLossQuote:    decimal.NewFromInt(200),
			StopLossPct:          0.03,
			TakeProfitPct:        0.06,
			MaxHoldingPeriod:     types.D(24 * time.Hour),
		},
		State:           types.StateDraft,
		SourceUtterance: "BTC 1h chart: go long when price breaks a key resistance level, volume surges to 2x its average, and active buying dominates; max 1000 USDT per trade, 3% stop-loss, 6% take-profit, hold at most one day",
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
