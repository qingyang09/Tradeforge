package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"tradeforge/pkg/types"
)

// feedKey 标识一路独立的行情：标的 + 周期。同一个 symbol 在不同 timeframe 下的策略
// 需要各自独立回填/订阅，不能共用一份滚动窗口——这是对 CSV 重放路径里原有的
// "feeds 只按 symbol 去重"这个疏漏的同步修正（见 main.go 里 loadCSVFeeds 的注释）。
type feedKey struct {
	Symbol    string
	Timeframe types.Timeframe
}

// historicalSource 是 runLive 启动时回填初始滚动窗口所需要的最小接口。
// *okx.Client 结构性满足它，不需要 okx 包知道这个接口的存在。
type historicalSource interface {
	FetchCandles(ctx context.Context, symbol string, tf types.Timeframe, limit int) ([]types.Candle, error)
}

// liveSource 是 runLive 订阅实时收盘 K 线所需要的最小接口。
type liveSource interface {
	Subscribe(ctx context.Context, symbol string, tf types.Timeframe) (<-chan types.Candle, error)
}

// processor 是 runLive 计算决策所需要的最小接口。*engine.Engine 的 Process 方法
// 结构性满足它；测试用假实现记录每次调用收到的 feeds，不需要真的搭一个
// 组合引擎（含模块注册表、审计、发布）就能验证 runLive 自己的编排逻辑
// （分组、回填、窗口裁剪、断线重连、标的间隔离、多周期拼装）。
type processor interface {
	Process(ctx context.Context, cfg types.StrategyConfig, feeds map[types.Timeframe]types.MarketData) (types.Decision, error)
}

// reconnectDelay 是订阅失败或连接断开后重试前的等待时长。固定退避而不是指数退避——
// 这是一个本地长驻进程，不是外部 API 的重度调用方，没有必要为这一层加更复杂的策略。
// 是变量而不是常量，方便测试临时调短，不必真的等 3 秒。
var reconnectDelay = 3 * time.Second

// candleCache 是按 (symbol, timeframe) 保护的共享滚动窗口缓存。多周期策略下，一个
// (symbol, timeframe) 可能同时是某个策略的触发周期、又是另一个策略的背景周期——
// 每路行情各自一个 goroutine 维护自己的窗口并写入这里；触发周期对应的 goroutine
// 在算决策前，从这里读出该策略需要的其它周期的最新窗口。
//
// 实时路径天然不存在"偷看未来"的问题：每个 goroutine 只在真正收到订阅推来的、已经
// 收盘的 K 线时才写入缓存，读到的永远是当下已经发生的数据，不需要 AlignAsOf 那种
// 按时间裁剪的动作（那是给离线重放用的，见 cmd/backtest-runner 和下面的 CSV 重放路径）。
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

// buildFeeds 把某个策略需要的每个周期，从共享缓存里拼成一份 feeds map。
// 只要有一个所需周期还没有任何数据（比如刚启动、回填还没完成），就返回 false——
// 宁可跳过这一次评估，也不要拿不完整的行情硬凑一次决策。
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

// feedPlan 把一份策略列表拆成"按触发周期分组的策略"和"需要订阅的全部 (symbol,
// timeframe) 组合"——触发周期本身 + 每个策略用到的所有背景周期。runLive 启动时和
// 每次重新扫描都要做这同一件事，抽出来避免逻辑漂移。
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

// runLive 按 (symbol, timeframe) 分组订阅实时行情，一组一个 goroutine，组之间
// 互不影响——一路行情断线重连不会拖累其它标的的信号计算，呼应执行层"一个标的的
// 异常不能影响其它标的"的一贯要求。
//
// 多周期策略的触发周期与背景周期都各自有一路独立的行情 goroutine；只有触发周期
// 那一路在收到新收盘 K 线时才会去评估策略（并现场从共享缓存拼出其它所需周期的数据），
// 背景周期那一路只负责维护自己的滚动窗口。阻塞直到 ctx 被取消。
//
// reload 非 nil 时，每 rescanInterval 一个周期重新调用它拿最新的策略列表，给全新出现
// 的 (symbol, timeframe) 组合起一路新订阅——已经在订阅中的组合不受影响，即便重新扫描
// 发现它的 triggerStrategies 应该多一条（比如新用户在同一个标的同一个周期上也建了
// 策略），这一轮不支持给一路已经在跑的订阅动态追加策略，仍然需要重启进程才能生效
// （见 cmd/signal-engine/main.go 顶部注释）。reload 为 nil 或 rescanInterval <= 0 时
// 不重新扫描，行为等同于这个字段被加进来之前。
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

// runRescanLoop 按固定间隔重新加载策略列表，给全新出现的 (symbol, timeframe) 组合调
// startFeed——startFeed 自己会判重，这里不需要额外的差集逻辑。
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
				logger.Error("重新扫描策略列表失败，保留现有订阅不变", "err", err)
				continue
			}
			triggers, allKeys := feedPlan(strategies)
			for k := range allKeys {
				startFeed(k, triggers[k])
			}
		}
	}
}

// runSymbolFeed 是单个 (symbol, timeframe) 的完整生命周期：回填历史窗口写入共享缓存，
// 然后持续订阅实时收盘 K 线、更新缓存，断线自动重连，直到 ctx 被取消。
//
// triggerStrategies 是把这个 (symbol, timeframe) 当作触发周期的策略列表——可能为空
// （这一路只是别的策略的背景周期），此时这个 goroutine 只更新缓存，不主动评估任何策略。
func runSymbolFeed(
	ctx context.Context, e processor, cache *candleCache,
	triggerStrategies []types.StrategyConfig, hist historicalSource, live liveSource,
	symbol string, tf types.Timeframe, backfill, window int, logger *slog.Logger,
) {
	initial, err := hist.FetchCandles(ctx, symbol, tf, backfill)
	if err != nil {
		logger.Error("回填历史 K 线失败，该周期不会产生任何信号", "symbol", symbol, "timeframe", tf, "err", err)
		return
	}
	candles := initial
	cache.set(feedKey{symbol, tf}, candles)
	logger.Info("已回填历史 K 线", "symbol", symbol, "timeframe", tf, "count", len(candles))

	for {
		if ctx.Err() != nil {
			return
		}

		ch, err := live.Subscribe(ctx, symbol, tf)
		if err != nil {
			logger.Error("订阅实时行情失败，稍后重试", "symbol", symbol, "timeframe", tf, "err", err)
			if !sleepOrDone(ctx, reconnectDelay) {
				return
			}
			continue
		}
		logger.Info("已订阅实时行情", "symbol", symbol, "timeframe", tf)

		for c := range ch {
			candles = append(candles, c)
			if window > 0 && len(candles) > window {
				candles = candles[len(candles)-window:]
			}
			cache.set(feedKey{symbol, tf}, candles)

			for _, s := range triggerStrategies {
				feeds, ok := buildFeeds(cache, s)
				if !ok {
					logger.Warn("背景周期尚无可用数据，跳过本次评估",
						"strategy_id", s.ID, "symbol", symbol, "trigger_timeframe", tf)
					continue
				}
				d, err := e.Process(ctx, s, feeds)
				if err != nil {
					// 单个策略出错不影响同一路行情下的其它策略。
					logger.Error("计算决策失败", "strategy_id", s.ID, "symbol", symbol, "err", err)
					continue
				}
				if d.Triggered {
					logger.Info("决策触发",
						"strategy_id", s.ID, "symbol", symbol,
						"direction", d.Direction, "score", d.Score,
						"price", d.Price.String(), "reason", d.Reason)
				}
			}
		}

		if ctx.Err() != nil {
			return
		}
		logger.Warn("实时行情连接断开，准备重连", "symbol", symbol, "timeframe", tf)
		if !sleepOrDone(ctx, reconnectDelay) {
			return
		}
	}
}

// sleepOrDone 睡眠 d；若期间 ctx 被取消则提前返回 false，调用方据此判断是否该退出。
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
