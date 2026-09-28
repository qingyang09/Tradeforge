package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"tradeforge/pkg/types"
)

// feedKey identifies one independent market data stream: symbol + timeframe.
// Strategies on the same symbol but different timeframes need their own
// independent backfill/subscription and can't share a single rolling window
// — this is the live-path counterpart to the fix for the "feeds deduped by
// symbol alone" gap in the CSV replay path (see the loadCSVFeeds comment in
// main.go).
type feedKey struct {
	Symbol    string
	Timeframe types.Timeframe
}

// historicalSource is the minimal interface runLive needs to backfill the
// initial rolling window at startup. *okx.Client satisfies it structurally;
// the okx package doesn't need to know this interface exists.
type historicalSource interface {
	FetchCandles(ctx context.Context, symbol string, tf types.Timeframe, limit int) ([]types.Candle, error)
}

// liveSource is the minimal interface runLive needs to subscribe to live
// closed candles.
type liveSource interface {
	Subscribe(ctx context.Context, symbol string, tf types.Timeframe) (<-chan types.Candle, error)
}

// processor is the minimal interface runLive needs to compute decisions.
// *engine.Engine's Process method satisfies it structurally; tests use a
// fake implementation that records the feeds received on each call, so
// runLive's own orchestration logic (grouping, backfill, window trimming,
// reconnect, per-symbol isolation, multi-timeframe assembly) can be verified
// without standing up a real composition engine (module registry, audit,
// publisher).
type processor interface {
	Process(ctx context.Context, cfg types.StrategyConfig, feeds map[types.Timeframe]types.MarketData) (types.Decision, error)
}

// reconnectDelay is how long to wait before retrying after a failed
// subscription or a dropped connection. Fixed backoff rather than
// exponential — this is a local long-running process, not a heavy caller of
// an external API, so a more elaborate strategy isn't warranted here. It's a
// variable rather than a constant so tests can shorten it instead of
// actually waiting 3 seconds.
var reconnectDelay = 3 * time.Second

// candleCache is a shared rolling-window cache guarded per (symbol,
// timeframe). Under multi-timeframe strategies, a given (symbol, timeframe)
// may simultaneously be one strategy's trigger timeframe and another
// strategy's background timeframe — each market data stream has its own
// goroutine maintaining its own window and writing it here; the trigger
// timeframe's goroutine reads the latest window for any other timeframes
// its strategy needs from here before computing a decision.
//
// The live path has no "peeking into the future" problem by construction:
// each goroutine only writes to the cache when it actually receives an
// already-closed candle pushed by the subscription, so what it reads is
// always data that has genuinely already happened — no need for the
// time-based trimming that AlignAsOf does (that's for offline replay, see
// cmd/backtest-runner and the CSV replay path below).
type candleCache struct {
	mu   sync.RWMutex
	data map[feedKey][]types.Candle
}

func newCandleCache() *candleCache {
	return &candleCache{data: make(map[feedKey][]types.Candle)}
}

func (c *candleCache) set(k feedKey, candles []types.Candle) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[k] = candles
}

func (c *candleCache) get(k feedKey) ([]types.Candle, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.data[k]
	return v, ok
}

// buildFeeds assembles a feeds map for a strategy from the shared cache, one
// entry per timeframe it needs. If any required timeframe has no data yet
// (e.g. right after startup, before backfill completes), it returns false —
// better to skip this evaluation than force a decision out of incomplete
// market data.
func buildFeeds(cache *candleCache, s types.StrategyConfig) (map[types.Timeframe]types.MarketData, bool) {
	tfs := s.RequiredTimeframes()
	feeds := make(map[types.Timeframe]types.MarketData, len(tfs))
	for _, tf := range tfs {
		candles, ok := cache.get(feedKey{s.Symbol, tf})
		if !ok || len(candles) == 0 {
			return nil, false
		}
		feeds[tf] = types.MarketData{Symbol: s.Symbol, Timeframe: tf, Candles: candles}
	}
	return feeds, true
}

// feedPlan splits a strategy list into "strategies grouped by trigger
// timeframe" and "the full set of (symbol, timeframe) combinations that need
// a subscription" — the trigger timeframe itself plus every background
// timeframe each strategy uses. runLive does this same work at startup and
// on every rescan, so it's factored out here to avoid the two drifting apart.
func feedPlan(strategies []types.StrategyConfig) (triggers map[feedKey][]types.StrategyConfig, allKeys map[feedKey]bool) {
	triggers = make(map[feedKey][]types.StrategyConfig)
	allKeys = make(map[feedKey]bool)
	for _, s := range strategies {
		triggers[feedKey{s.Symbol, s.Timeframe}] = append(triggers[feedKey{s.Symbol, s.Timeframe}], s)
		for _, tf := range s.RequiredTimeframes() {
			allKeys[feedKey{s.Symbol, tf}] = true
		}
	}
	return triggers, allKeys
}

// runLive subscribes to live market data grouped by (symbol, timeframe), one
// goroutine per group, fully independent of each other — a reconnect on one
// stream doesn't hold up signal computation for other symbols, matching the
// execution layer's standing requirement that "one symbol's problem must
// never affect another symbol".
//
// Both the trigger timeframe and each background timeframe of a
// multi-timeframe strategy get their own independent market data goroutine;
// only the trigger timeframe's goroutine evaluates strategies when it
// receives a new closed candle (assembling the other needed timeframes' data
// from the shared cache on the spot) — a background timeframe's goroutine
// only maintains its own rolling window. Blocks until ctx is canceled.
//
// When reload is non-nil, it's called again every rescanInterval to fetch the
// latest strategy list and open a new subscription for any newly-seen
// (symbol, timeframe) combination — combinations already subscribed are left
// alone, even if the rescan finds that their triggerStrategies should gain
// an entry (e.g. a new user created a strategy on the same symbol and
// timeframe). This round doesn't support dynamically appending a strategy to
// an already-running subscription; a process restart is still required for
// that to take effect (see the top comment in cmd/signal-engine/main.go).
// When reload is nil or rescanInterval <= 0, no rescanning happens, matching
// the behavior before this field was added.
func runLive(
	ctx context.Context, e processor, strategies []types.StrategyConfig,
	hist historicalSource, live liveSource, backfill, window int, logger *slog.Logger,
	reload func(ctx context.Context) ([]types.StrategyConfig, error), rescanInterval time.Duration,
) {
	cache := newCandleCache()

	var wg sync.WaitGroup
	var startedMu sync.Mutex
	started := make(map[feedKey]bool)

	startFeed := func(k feedKey, triggerStrategies []types.StrategyConfig) {
		startedMu.Lock()
		if started[k] {
			startedMu.Unlock()
			return
		}
		started[k] = true
		startedMu.Unlock()

		wg.Add(1)
		go func() {
			defer wg.Done()
			runSymbolFeed(ctx, e, cache, triggerStrategies, hist, live, k.Symbol, k.Timeframe, backfill, window, logger)
		}()
	}

	triggers, allKeys := feedPlan(strategies)
	for k := range allKeys {
		startFeed(k, triggers[k])
	}

	if reload != nil && rescanInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runRescanLoop(ctx, reload, startFeed, logger, rescanInterval)
		}()
	}

	wg.Wait()
}

// runRescanLoop reloads the strategy list on a fixed interval and calls
// startFeed for every newly-seen (symbol, timeframe) combination — startFeed
// itself dedupes, so no extra diffing logic is needed here.
func runRescanLoop(
	ctx context.Context, reload func(ctx context.Context) ([]types.StrategyConfig, error),
	startFeed func(feedKey, []types.StrategyConfig), logger *slog.Logger, interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			strategies, err := reload(ctx)
			if err != nil {
				logger.Error("failed to rescan strategy list, keeping existing subscriptions unchanged", "err", err)
				continue
			}
			triggers, allKeys := feedPlan(strategies)
			for k := range allKeys {
				startFeed(k, triggers[k])
			}
		}
	}
}

// runSymbolFeed handles the full lifecycle of a single (symbol, timeframe):
// backfill the historical window into the shared cache, then keep
// subscribing to live closed candles and updating the cache, reconnecting
// automatically on disconnect, until ctx is canceled.
//
// triggerStrategies is the list of strategies that use this (symbol,
// timeframe) as their trigger timeframe — it may be empty (this stream is
// only a background timeframe for other strategies), in which case this
// goroutine only updates the cache and doesn't evaluate any strategy.
func runSymbolFeed(
	ctx context.Context, e processor, cache *candleCache,
	triggerStrategies []types.StrategyConfig, hist historicalSource, live liveSource,
	symbol string, tf types.Timeframe, backfill, window int, logger *slog.Logger,
) {
	initial, err := hist.FetchCandles(ctx, symbol, tf, backfill)
	if err != nil {
		logger.Error("failed to backfill historical candles, this timeframe will produce no signals", "symbol", symbol, "timeframe", tf, "err", err)
		return
	}
	candles := initial
	cache.set(feedKey{symbol, tf}, candles)
	logger.Info("backfilled historical candles", "symbol", symbol, "timeframe", tf, "count", len(candles))

	for {
		if ctx.Err() != nil {
			return
		}

		ch, err := live.Subscribe(ctx, symbol, tf)
		if err != nil {
			logger.Error("failed to subscribe to live market data, retrying later", "symbol", symbol, "timeframe", tf, "err", err)
			if !sleepOrDone(ctx, reconnectDelay) {
				return
			}
			continue
		}
		logger.Info("subscribed to live market data", "symbol", symbol, "timeframe", tf)

		for c := range ch {
			candles = append(candles, c)
			if window > 0 && len(candles) > window {
				candles = candles[len(candles)-window:]
			}
			cache.set(feedKey{symbol, tf}, candles)

			for _, s := range triggerStrategies {
				feeds, ok := buildFeeds(cache, s)
				if !ok {
					logger.Warn("background timeframe has no data yet, skipping this evaluation",
						"strategy_id", s.ID, "symbol", symbol, "trigger_timeframe", tf)
					continue
				}
				d, err := e.Process(ctx, s, feeds)
				if err != nil {
					// One strategy's error doesn't affect other strategies on the same market data stream.
					logger.Error("failed to compute decision", "strategy_id", s.ID, "symbol", symbol, "err", err)
					continue
				}
				if d.Triggered {
					logger.Info("decision triggered",
						"strategy_id", s.ID, "symbol", symbol,
						"direction", d.Direction, "score", d.Score,
						"price", d.Price.String(), "reason", d.Reason)
				}
			}
		}

		if ctx.Err() != nil {
			return
		}
		logger.Warn("live market data connection dropped, reconnecting", "symbol", symbol, "timeframe", tf)
		if !sleepOrDone(ctx, reconnectDelay) {
			return
		}
	}
}

// sleepOrDone sleeps for d; if ctx is canceled during that time, it returns
// false early so the caller knows to exit.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
