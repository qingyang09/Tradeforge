// Package backtest implements signal replay: it runs a StrategyConfig
// candle-by-candle over historical data through internal/engine, producing
// a stream of decisions.
//
// Why replay lives in Go instead of Python: backtesting and live trading
// must share the exact same signal logic. Re-implementing support/resistance,
// CVD, etc. a second time in Python would inevitably drift from the Go
// version over time — and at that point the backtest results would be
// evaluating a different strategy, which is more dangerous than having no
// backtest at all. This reuses internal/engine directly; the Python side
// (python/backtest) only handles what it's actually good at: matching
// simulation, fee/slippage modeling, and performance statistics.
//
// This logic originally lived directly in cmd/backtest-runner, with the CLI
// as its only caller. Once the builder got a "run backtest" button
// (internal/webui), the webui process needed to run a replay in-process,
// rather than writing to disk first and shelling out to a subprocess to
// read it back — so this was pulled out into its own package, shared by the
// CLI and webui. There must never be a second replay loop.
package backtest

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"tradeforge/internal/engine"
	"tradeforge/internal/modules"
	"tradeforge/pkg/types"
)

// EngineVersion tags which version of the signal logic produced a decision
// stream, and gets written into the backtest result — since CLI-triggered
// and webui-triggered backtests now share the same Replay implementation,
// they should rightly share the same version number.
const EngineVersion = "signal-replay/1.0.0"

// DefaultWindow is the maximum amount of history fed to modules.
//
// Passing the entire history on every candle would make candle-by-candle
// replay O(n²); modules' lookback windows have an upper bound (currently
// 1000 max), so giving them ample headroom beyond that is enough.
const DefaultWindow = 1200

// Meta is the header metadata for one replay run.
type Meta struct {
	Type          string    `json:"type"`
	EngineVersion string    `json:"engine_version"`
	StrategyID    string    `json:"strategy_id"`
	Symbol        string    `json:"symbol"`
	Timeframe     string    `json:"timeframe"`
	Combine       string    `json:"combine"`
	BarCount      int       `json:"bar_count"`
	DataStart     time.Time `json:"data_start"`
	DataEnd       time.Time `json:"data_end"`
	GeneratedAt   time.Time `json:"generated_at"`
}

// DecisionLine is the decision produced for one candle during replay. Its
// field shape corresponds exactly to one JSONL line as understood by
// python/backtest/tradeforge_backtest/loaders.py (decision_from_iter parses
// by these field names) — do not change it.
type DecisionLine struct {
	Type      string         `json:"type"`
	Index     int            `json:"index"`
	BarTime   time.Time      `json:"bar_time"`
	Direction string         `json:"direction"`
	Score     float64        `json:"score"`
	Triggered bool           `json:"triggered"`
	Price     string         `json:"price"`
	Reason    string         `json:"reason"`
	Signals   []types.Signal `json:"signals"`
}

// Replay walks candle-by-candle over the trigger timeframe, returning the
// header metadata and each candle's decision.
//
// The critical property: the decision for trigger-timeframe candle i only
// ever uses candles[:i+1]; the other (slower) timeframes in contextFeeds
// likewise only ever expose the portion that's genuinely closed "as of this
// moment" — trimmed with types.AlignAsOf to the current trigger candle's
// close time, never touching future data on any timeframe. This is the
// fundamental precondition for a backtest to be trustworthy at all, more
// important than any single metric.
func Replay(
	ctx context.Context, cfg types.StrategyConfig, md types.MarketData,
	contextFeeds map[types.Timeframe]types.MarketData,
	window int, logger *slog.Logger,
) (Meta, []DecisionLine, error) {
	if len(md.Candles) == 0 {
		return Meta{}, nil, fmt.Errorf("no candle data")
	}
	e := engine.New(modules.NewDefaultRegistry(), engine.WithLogger(logger))

	meta := Meta{
		Type:          "meta",
		EngineVersion: EngineVersion,
		StrategyID:    cfg.ID,
		Symbol:        cfg.Symbol,
		Timeframe:     string(cfg.Timeframe),
		Combine:       string(cfg.Combine),
		BarCount:      len(md.Candles),
		DataStart:     md.Candles[0].OpenTime,
		DataEnd:       md.Candles[len(md.Candles)-1].CloseTime,
		GeneratedAt:   time.Now().UTC(),
	}

	decisions := make([]DecisionLine, 0, len(md.Candles))
	for i := range md.Candles {
		start := 0
		if window > 0 && i+1 > window {
			start = i + 1 - window
		}
		cutoff := md.Candles[i].CloseTime
		feeds := map[types.Timeframe]types.MarketData{
			cfg.Timeframe: {
				Symbol:    md.Symbol,
				Timeframe: md.Timeframe,
				Candles:   md.Candles[start : i+1],
			},
		}
		for tf, ctxMD := range contextFeeds {
			aligned := types.AlignAsOf(ctxMD.Candles, cutoff)
			if window > 0 && len(aligned) > window {
				aligned = aligned[len(aligned)-window:]
			}
			feeds[tf] = types.MarketData{Symbol: ctxMD.Symbol, Timeframe: tf, Candles: aligned}
		}

		d, err := e.Evaluate(ctx, cfg, feeds)
		if err != nil {
			return Meta{}, nil, fmt.Errorf("evaluation failed for candle %d: %w", i+1, err)
		}

		decisions = append(decisions, DecisionLine{
			Type:      "decision",
			Index:     i,
			BarTime:   md.Candles[i].CloseTime,
			Direction: string(d.Direction),
			Score:     d.Score,
			Triggered: d.Triggered,
			Price:     d.Price.String(),
			Reason:    d.Reason,
			Signals:   d.Signals,
		})
	}
	return meta, decisions, nil
}
