// Command backtest-runner replays a strategy candle by candle over
// historical data, emitting a decision stream (JSONL).
//
// The replay logic itself lives in internal/backtest (it used to be written
// directly in this command, but once the builder got a "run backtest"
// button, internal/webui also needed to run the same replay in-process, so
// it was factored out into a shared package — the CLI and webui must not
// each run their own replay implementation that could drift apart).
//
// Usage:
//
//	backtest-runner -strategy s.json -candles btc.csv > decisions.jsonl
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"tradeforge/internal/backtest"
	"tradeforge/internal/marketdata"
	"tradeforge/pkg/types"
)

// contextCandlesFlag collects repeated -context-candles timeframe=path
// flags, supplying a multi-timeframe strategy with historical candles for
// the other (slower) timeframes its modules use besides the trigger
// timeframe. The standard flag package doesn't support collecting a
// repeated flag into a slice, so this implements the flag.Value interface
// itself.
type contextCandlesFlag map[types.Timeframe]string

func (f contextCandlesFlag) String() string {
	parts := make([]string, 0, len(f))
	for tf, path := range f {
		parts = append(parts, string(tf)+"="+path)
	}
	return strings.Join(parts, ",")
}

func (f contextCandlesFlag) Set(value string) error {
	tf, path, ok := strings.Cut(value, "=")
	if !ok || tf == "" || path == "" {
		return fmt.Errorf("expected format timeframe=path, got %q", value)
	}
	f[types.Timeframe(tf)] = path
	return nil
}

func main() {
	strategyPath := flag.String("strategy", "", "path to the strategy config JSON file (required)")
	candlesPath := flag.String("candles", "", "path to the historical candle CSV for the trigger timeframe (required)")
	outPath := flag.String("out", "", "output file path; leave empty to write to stdout")
	window := flag.Int("window", backtest.DefaultWindow, "max number of historical candles fed to modules")
	quiet := flag.Bool("quiet", false, "suppress warning logs such as module degradation")
	contextCandles := make(contextCandlesFlag)
	flag.Var(contextCandles, "context-candles", "historical candles for timeframes other than the trigger timeframe in a multi-timeframe strategy, "+
		"format timeframe=path, may be repeated (e.g. -context-candles 1h=btc_1h.csv)")
	flag.Parse()

	if *strategyPath == "" || *candlesPath == "" {
		flag.Usage()
		os.Exit(2)
	}

	cfg, err := loadStrategy(*strategyPath)
	if err != nil {
		fatal("failed to read strategy config: %v", err)
	}

	md, err := marketdata.LoadCSV(*candlesPath, cfg.Symbol, cfg.Timeframe)
	if err != nil {
		fatal("failed to read market data: %v", err)
	}

	// Verify that every timeframe the strategy needs (other than the trigger
	// timeframe itself) has a matching -context-candles entry. Missing one
	// is a hard error and exit, rather than silently skipping it and letting
	// that module run degraded for the entire backtest.
	contextFeeds := make(map[types.Timeframe]types.MarketData, len(contextCandles))
	for _, tf := range cfg.RequiredTimeframes() {
		if tf == cfg.Timeframe {
			continue
		}
		path, ok := contextCandles[tf]
		if !ok {
			fatal("strategy needs %s timeframe market data, but no matching -context-candles %s=<path> was provided", tf, tf)
		}
		cmd, err := marketdata.LoadCSV(path, cfg.Symbol, tf)
		if err != nil {
			fatal("failed to read %s timeframe market data: %v", tf, err)
		}
		contextFeeds[tf] = cmd
	}

	logger := slog.Default()
	if *quiet {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	meta, decisions, err := backtest.Replay(context.Background(), cfg, md, contextFeeds, *window, logger)
	if err != nil {
		fatal("replay failed: %v", err)
	}

	out := io.Writer(os.Stdout)
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			fatal("failed to create output file: %v", err)
		}
		defer f.Close()
		out = f
	}
	if err := backtest.WriteJSONL(out, meta, decisions); err != nil {
		fatal("failed to write decision stream: %v", err)
	}
}

func loadStrategy(path string) (types.StrategyConfig, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return types.StrategyConfig{}, err
	}
	var cfg types.StrategyConfig
	dec := json.NewDecoder(bytes.NewReader(blob))
	// Reject unknown fields: extra fields in a strategy file are usually a
	// typo from manual editing — silently ignoring them would make someone
	// think their change took effect.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return types.StrategyConfig{}, fmt.Errorf("failed to parse strategy JSON: %w", err)
	}
	return cfg, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
