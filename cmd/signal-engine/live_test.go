package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testCandle(closePrice float64) types.Candle {
	p := decimal.NewFromFloat(closePrice)
	return types.Candle{
		OpenTime: time.Now(), CloseTime: time.Now(),
		Open: p, High: p, Low: p, Close: p, Volume: decimal.NewFromInt(1),
	}
}

func testStrategy(id, symbol string, tf types.Timeframe) types.StrategyConfig {
	return types.StrategyConfig{ID: id, Symbol: symbol, Timeframe: tf}
}

// multiTFStrategy 构造一个触发周期是 tf、但有一个模块用更慢的 ctxTF 的策略，
// 用来测试 runLive/runSymbolFeed 的多周期分发逻辑。
func multiTFStrategy(id, symbol string, tf, ctxTF types.Timeframe) types.StrategyConfig {
	cfg := testStrategy(id, symbol, tf)
	cfg.Modules = []types.ModuleConfig{{Module: "ctx", Timeframe: ctxTF}}
	return cfg
}

// ---- 假数据源/处理器 ----

type fakeHistSource struct {
	mu      sync.Mutex
	calls   []feedKey
	candles map[feedKey][]types.Candle
	errs    map[feedKey]error
}

func newFakeHistSource() *fakeHistSource {
	return &fakeHistSource{candles: map[feedKey][]types.Candle{}, errs: map[feedKey]error{}}
}

func (f *fakeHistSource) FetchCandles(_ context.Context, symbol string, tf types.Timeframe, _ int) ([]types.Candle, error) {
	k := feedKey{symbol, tf}
	f.mu.Lock()
	f.calls = append(f.calls, k)
	f.mu.Unlock()
	if err, ok := f.errs[k]; ok {
		return nil, err
	}
	return f.candles[k], nil
}

func (f *fakeHistSource) callCount(k feedKey) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == k {
			n++
		}
	}
	return n
}

// fakeLiveSource 按 feedKey 维护一个 channel 队列：每次 Subscribe 发一个新的，
// 让测试能模拟"断线后重新订阅拿到一条新连接"这个场景。
type fakeLiveSource struct {
	mu             sync.Mutex
	subscribeCalls map[feedKey]int
	queues         map[feedKey][]chan types.Candle
	errs           map[feedKey]error
}

func newFakeLiveSource() *fakeLiveSource {
	return &fakeLiveSource{
		subscribeCalls: map[feedKey]int{},
		queues:         map[feedKey][]chan types.Candle{},
		errs:           map[feedKey]error{},
	}
}

func (f *fakeLiveSource) enqueue(k feedKey, ch chan types.Candle) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queues[k] = append(f.queues[k], ch)
}

func (f *fakeLiveSource) Subscribe(_ context.Context, symbol string, tf types.Timeframe) (<-chan types.Candle, error) {
	k := feedKey{symbol, tf}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribeCalls[k]++
	if err, ok := f.errs[k]; ok {
		return nil, err
	}
	q := f.queues[k]
	if len(q) == 0 {
		ch := make(chan types.Candle)
		close(ch)
		return ch, nil
	}
	ch := q[0]
	f.queues[k] = q[1:]
	return ch, nil
}

func (f *fakeLiveSource) callCount(k feedKey) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subscribeCalls[k]
}

// fakeProcessor 记录每次 Process 调用收到的 feeds，用于断言窗口内容。
type fakeProcessor struct {
	mu    sync.Mutex
	calls []map[types.Timeframe]types.MarketData
	err   error
}

func (f *fakeProcessor) Process(_ context.Context, _ types.StrategyConfig, feeds map[types.Timeframe]types.MarketData) (types.Decision, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return types.Decision{}, f.err
	}
	f.calls = append(f.calls, feeds)
	return types.Decision{}, nil
}

func (f *fakeProcessor) snapshot() []map[types.Timeframe]types.MarketData {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[types.Timeframe]types.MarketData, len(f.calls))
	copy(out, f.calls)
	return out
}

// waitFor 轮询 cond 直到为真或超时，测试里用来等异步副作用出现，避免固定 sleep。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// ---- 测试 ----

func TestRunSymbolFeedBackfillsThenProcessesLiveCandles(t *testing.T) {
	k := feedKey{"BTCUSDT", types.TF1h}
	hist := newFakeHistSource()
	hist.candles[k] = []types.Candle{testCandle(100), testCandle(101)}

	live := newFakeLiveSource()
	ch := make(chan types.Candle, 1)
	live.enqueue(k, ch)
	ch <- testCandle(102)
	close(ch) // 关闭模拟"这一段推送结束"，range 循环才会退出去走重连分支

	proc := &fakeProcessor{}
	strategies := []types.StrategyConfig{testStrategy("s1", "BTCUSDT", types.TF1h)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runSymbolFeed(ctx, proc, newCandleCache(), strategies, hist, live, "BTCUSDT", types.TF1h, 300, 1200, quietLogger())
		close(done)
	}()

	waitFor(t, 2*time.Second, func() bool { return len(proc.snapshot()) >= 1 })
	cancel()
	<-done

	calls := proc.snapshot()
	if len(calls) == 0 {
		t.Fatal("应至少调用一次 Process")
	}
	last := calls[len(calls)-1][types.TF1h]
	if len(last.Candles) != 3 {
		t.Fatalf("最后一次调用的窗口应有 3 根 K 线（2 根回填 + 1 根实时），实际 %d 根", len(last.Candles))
	}
	if !last.Candles[2].Close.Equal(decimal.NewFromFloat(102)) {
		t.Errorf("最新一根收盘价 = %s，期望 102", last.Candles[2].Close)
	}
}

func TestRunSymbolFeedTrimsWindow(t *testing.T) {
	k := feedKey{"BTCUSDT", types.TF1h}
	hist := newFakeHistSource()
	hist.candles[k] = []types.Candle{testCandle(1), testCandle(2), testCandle(3)}

	live := newFakeLiveSource()
	ch := make(chan types.Candle, 1)
	live.enqueue(k, ch)
	ch <- testCandle(4)
	close(ch)

	proc := &fakeProcessor{}
	strategies := []types.StrategyConfig{testStrategy("s1", "BTCUSDT", types.TF1h)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		// window=2：3 根回填 + 1 根实时 = 4 根，应该被裁到最后 2 根。
		runSymbolFeed(ctx, proc, newCandleCache(), strategies, hist, live, "BTCUSDT", types.TF1h, 300, 2, quietLogger())
		close(done)
	}()

	waitFor(t, 2*time.Second, func() bool { return len(proc.snapshot()) >= 1 })
	cancel()
	<-done

	last := proc.snapshot()[len(proc.snapshot())-1][types.TF1h]
	if len(last.Candles) != 2 {
		t.Fatalf("窗口应被裁剪到 2 根，实际 %d 根", len(last.Candles))
	}
	if !last.Candles[1].Close.Equal(decimal.NewFromFloat(4)) {
		t.Errorf("裁剪后最新一根收盘价 = %s，期望 4", last.Candles[1].Close)
	}
}

func TestRunSymbolFeedReturnsWhenBackfillFails(t *testing.T) {
	k := feedKey{"BTCUSDT", types.TF1h}
	hist := newFakeHistSource()
	hist.errs[k] = errors.New("boom")
	live := newFakeLiveSource()
	proc := &fakeProcessor{}

	done := make(chan struct{})
	go func() {
		runSymbolFeed(context.Background(), proc, newCandleCache(), nil, hist, live, "BTCUSDT", types.TF1h, 300, 1200, quietLogger())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("回填失败时 runSymbolFeed 应该直接返回，而不是挂住")
	}
	if live.callCount(k) != 0 {
		t.Error("回填失败不该再去订阅实时行情")
	}
}

func TestRunSymbolFeedReconnectsAfterChannelCloses(t *testing.T) {
	k := feedKey{"BTCUSDT", types.TF1h}
	hist := newFakeHistSource()

	live := newFakeLiveSource()
	first := make(chan types.Candle, 1)
	first <- testCandle(1)
	close(first) // 模拟第一次订阅收到一根后就断线
	live.enqueue(k, first)

	second := make(chan types.Candle, 1)
	second <- testCandle(2)
	close(second)
	live.enqueue(k, second)

	proc := &fakeProcessor{}
	strategies := []types.StrategyConfig{testStrategy("s1", "BTCUSDT", types.TF1h)}

	// 把重连退避临时调短，测试不必真的等 3 秒。
	origDelay := reconnectDelay
	reconnectDelay = 10 * time.Millisecond
	defer func() { reconnectDelay = origDelay }()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runSymbolFeed(ctx, proc, newCandleCache(), strategies, hist, live, "BTCUSDT", types.TF1h, 300, 1200, quietLogger())
		close(done)
	}()

	waitFor(t, 2*time.Second, func() bool { return live.callCount(k) >= 2 })
	waitFor(t, 2*time.Second, func() bool { return len(proc.snapshot()) >= 2 })
	cancel()
	<-done

	if live.callCount(k) < 2 {
		t.Fatalf("断线后应该重新订阅，Subscribe 调用次数 = %d，期望至少 2", live.callCount(k))
	}
}

func TestRunLiveIsolatesPerSymbolFailures(t *testing.T) {
	badKey := feedKey{"BADUSDT", types.TF1h}
	goodKey := feedKey{"GOODUSDT", types.TF1h}

	hist := newFakeHistSource()
	hist.errs[badKey] = errors.New("boom")
	hist.candles[goodKey] = []types.Candle{testCandle(1)}

	live := newFakeLiveSource()
	ch := make(chan types.Candle, 1)
	ch <- testCandle(2)
	close(ch)
	live.enqueue(goodKey, ch)

	proc := &fakeProcessor{}
	strategies := []types.StrategyConfig{
		testStrategy("bad", "BADUSDT", types.TF1h),
		testStrategy("good", "GOODUSDT", types.TF1h),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runLive(ctx, proc, strategies, hist, live, 300, 1200, quietLogger(), nil, 0)
		close(done)
	}()

	waitFor(t, 2*time.Second, func() bool { return len(proc.snapshot()) >= 1 })
	cancel()
	<-done

	if len(proc.snapshot()) == 0 {
		t.Fatal("好的那路行情不该被坏的那路拖累")
	}
}

func TestRunLiveGroupsStrategiesBySymbolAndTimeframe(t *testing.T) {
	k := feedKey{"BTCUSDT", types.TF1h}
	hist := newFakeHistSource()
	hist.candles[k] = []types.Candle{testCandle(1)}

	live := newFakeLiveSource()
	ch := make(chan types.Candle, 1)
	ch <- testCandle(2)
	close(ch)
	live.enqueue(k, ch)

	proc := &fakeProcessor{}
	// 两个策略共用同一个 (symbol, timeframe)：只该回填/订阅一次。
	strategies := []types.StrategyConfig{
		testStrategy("s1", "BTCUSDT", types.TF1h),
		testStrategy("s2", "BTCUSDT", types.TF1h),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runLive(ctx, proc, strategies, hist, live, 300, 1200, quietLogger(), nil, 0)
		close(done)
	}()

	waitFor(t, 2*time.Second, func() bool { return len(proc.snapshot()) >= 2 })
	cancel()
	<-done

	if hist.callCount(k) != 1 {
		t.Errorf("FetchCandles 调用次数 = %d，期望 1（同一路行情只回填一次）", hist.callCount(k))
	}
}

// strategyRecordingProcessor 跟 fakeProcessor 一样记录每次调用的 feeds，但额外记录
// 是哪个策略触发的——TestRunLiveSharesFeedAcrossDifferentUsersSameSymbolTimeframe 需要
// 证明"两个不同用户的策略都各自被评估了"，不只是"Process 被调用了 N 次"。
type strategyRecordingProcessor struct {
	mu          sync.Mutex
	strategyIDs []string
}

func (p *strategyRecordingProcessor) Process(_ context.Context, cfg types.StrategyConfig, _ map[types.Timeframe]types.MarketData) (types.Decision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.strategyIDs = append(p.strategyIDs, cfg.ID)
	return types.Decision{}, nil
}

func (p *strategyRecordingProcessor) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.strategyIDs))
	copy(out, p.strategyIDs)
	return out
}

// TestRunLiveSharesFeedAcrossDifferentUsersSameSymbolTimeframe 是多用户并发执行改造
// 的直接证据：两个不同用户在同一个 (symbol, timeframe) 上各自都有策略时，runLive 应该
// 只回填/订阅一次（省掉重复拉行情的成本，这是合并成一个进程服务多用户的核心收益），
// 且两个用户的策略在同一次新 K 线到来时都各自被评估——不是"参数传过去了"，是两条
// 真实归属不同用户的策略都真的拿到了决策计算。live.go 本身按设计不需要为这条测试
// 新增任何代码（feedKey 从来就没有用户维度），这条测试就是用来证明这件事、并在未来
// 有人不小心往 feedKey 里引入用户维度时能立刻挂红。
func TestRunLiveSharesFeedAcrossDifferentUsersSameSymbolTimeframe(t *testing.T) {
	k := feedKey{"BTCUSDT", types.TF1h}
	hist := newFakeHistSource()
	hist.candles[k] = []types.Candle{testCandle(1)}

	live := newFakeLiveSource()
	ch := make(chan types.Candle, 1)
	ch <- testCandle(2)
	close(ch)
	live.enqueue(k, ch)

	proc := &strategyRecordingProcessor{}
	userAStrategy := types.StrategyConfig{ID: "user-a-strategy", UserID: "user-a", Symbol: "BTCUSDT", Timeframe: types.TF1h}
	userBStrategy := types.StrategyConfig{ID: "user-b-strategy", UserID: "user-b", Symbol: "BTCUSDT", Timeframe: types.TF1h}
	strategies := []types.StrategyConfig{userAStrategy, userBStrategy}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runLive(ctx, proc, strategies, hist, live, 300, 1200, quietLogger(), nil, 0)
		close(done)
	}()

	waitFor(t, 2*time.Second, func() bool { return len(proc.snapshot()) >= 2 })
	cancel()
	<-done

	if hist.callCount(k) != 1 {
		t.Errorf("FetchCandles 调用次数 = %d，期望 1（两个用户共用同一路行情，只回填一次）", hist.callCount(k))
	}
	if live.callCount(k) != 1 {
		t.Errorf("Subscribe 调用次数 = %d，期望 1（两个用户共用同一路订阅）", live.callCount(k))
	}

	got := proc.snapshot()
	sawA, sawB := false, false
	for _, id := range got {
		switch id {
		case userAStrategy.ID:
			sawA = true
		case userBStrategy.ID:
			sawB = true
		}
	}
	if !sawA {
		t.Error("用户 A 的策略应该被评估到，但没有")
	}
	if !sawB {
		t.Error("用户 B 的策略应该被评估到，但没有")
	}
}

// TestRunLiveRescanStartsSubscriptionForNewlyAppearedFeedKey 验证周期性重新扫描能给
// 全新出现的 (symbol, timeframe) 组合起一路新订阅，不需要重启进程——这是多用户共享
// 一个进程后"新用户配置生效不用打断其它人"这条设计目标在 signal-engine 侧的落地。
// 第一次 reload 只返回 ETHUSDT 的策略，第二次（模拟一次重新扫描发现了新用户/新策略）
// 才带上 BTCUSDT，断言 BTCUSDT 那路行情是在重新扫描后才被回填/订阅的，而不是一开始
// 就有（证明确实是"重新扫描"生效了，不是凑巧一开始就传全了）。
func TestRunLiveRescanStartsSubscriptionForNewlyAppearedFeedKey(t *testing.T) {
	ethKey := feedKey{"ETHUSDT", types.TF1h}
	btcKey := feedKey{"BTCUSDT", types.TF1h}

	hist := newFakeHistSource()
	hist.candles[ethKey] = []types.Candle{testCandle(1)}
	hist.candles[btcKey] = []types.Candle{testCandle(2)}

	live := newFakeLiveSource()
	ethCh := make(chan types.Candle, 1)
	ethCh <- testCandle(3)
	close(ethCh)
	live.enqueue(ethKey, ethCh)
	btcCh := make(chan types.Candle, 1)
	btcCh <- testCandle(4)
	close(btcCh)
	live.enqueue(btcKey, btcCh)

	ethStrategy := types.StrategyConfig{ID: "eth-strategy", UserID: "user-a", Symbol: "ETHUSDT", Timeframe: types.TF1h}
	btcStrategy := types.StrategyConfig{ID: "btc-strategy", UserID: "user-b", Symbol: "BTCUSDT", Timeframe: types.TF1h}

	var reloadCalls int32
	reload := func(context.Context) ([]types.StrategyConfig, error) {
		n := atomic.AddInt32(&reloadCalls, 1)
		if n == 1 {
			// 重新扫描循环启动时会立刻打一次 tick——用这一次返回旧列表，模拟
			// "还没发现新策略"的状态；真正的新策略在下一次 tick 才出现。
			return []types.StrategyConfig{ethStrategy}, nil
		}
		return []types.StrategyConfig{ethStrategy, btcStrategy}, nil
	}

	proc := &strategyRecordingProcessor{}
	strategies := []types.StrategyConfig{ethStrategy}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runLive(ctx, proc, strategies, hist, live, 300, 1200, quietLogger(), reload, 20*time.Millisecond)
		close(done)
	}()

	// 一开始只该看到 ETH 那路行情，BTC 还不该被碰。
	waitFor(t, 2*time.Second, func() bool { return hist.callCount(ethKey) >= 1 })
	if hist.callCount(btcKey) != 0 {
		t.Errorf("重新扫描发现新策略之前，BTC 那路行情不该被回填，实际调用了 %d 次", hist.callCount(btcKey))
	}

	// 等重新扫描把 BTC 加进来。
	waitFor(t, 2*time.Second, func() bool { return hist.callCount(btcKey) >= 1 })
	waitFor(t, 2*time.Second, func() bool {
		for _, id := range proc.snapshot() {
			if id == btcStrategy.ID {
				return true
			}
		}
		return false
	})
	cancel()
	<-done

	if hist.callCount(btcKey) != 1 {
		t.Errorf("BTC 那路行情应该只被回填一次，实际 %d 次", hist.callCount(btcKey))
	}
}

// 多周期策略：触发周期是 15m，一个模块用更慢的 1h 背景周期。只有触发周期收到新
// K 线才应该评估策略；评估时应该能从共享缓存里拼出背景周期的数据。
func TestRunLiveDispatchesMultiTimeframeStrategyOnTriggerOnly(t *testing.T) {
	triggerKey := feedKey{"BTCUSDT", types.TF15m}
	ctxKey := feedKey{"BTCUSDT", types.TF1h}

	hist := newFakeHistSource()
	hist.candles[triggerKey] = []types.Candle{testCandle(100)}
	hist.candles[ctxKey] = []types.Candle{testCandle(200)}

	live := newFakeLiveSource()
	ctxCh := make(chan types.Candle, 1)
	ctxCh <- testCandle(201)
	close(ctxCh)
	live.enqueue(ctxKey, ctxCh)

	// 触发周期的 channel 送两根、间隔一点时间再关闭：即便第一根到达时背景周期的
	// 回填还没在另一个 goroutine 里落定（buildFeeds 会跳过这次评估，只打个警告日志），
	// 第二根送达时也一定已经落定了——避免测试因 goroutine 调度先后顺序不同而偶发失败。
	triggerCh := make(chan types.Candle, 2)
	triggerCh <- testCandle(101)
	go func() {
		time.Sleep(100 * time.Millisecond)
		triggerCh <- testCandle(102)
		close(triggerCh)
	}()
	live.enqueue(triggerKey, triggerCh)

	proc := &fakeProcessor{}
	strategies := []types.StrategyConfig{multiTFStrategy("s1", "BTCUSDT", types.TF15m, types.TF1h)}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runLive(ctx, proc, strategies, hist, live, 300, 1200, quietLogger(), nil, 0)
		close(done)
	}()

	waitFor(t, 2*time.Second, func() bool { return len(proc.snapshot()) >= 1 })
	time.Sleep(150 * time.Millisecond) // 给第二根触发K线留出被处理的时间
	cancel()
	<-done

	calls := proc.snapshot()
	if len(calls) == 0 {
		t.Fatal("触发周期收到新K线时应该评估策略")
	}
	if len(calls) > 2 {
		t.Fatalf("只推送了 2 根触发周期K线，评估次数不该超过 2，实际 %d", len(calls))
	}
	// buildFeeds 要求所有需要的周期都齐了才会调用 Process，所以只要这里被调用过，
	// 15m（触发）和 1h（背景）两个 feed 就一定都在——这正是要验证的核心行为：
	// 触发周期的评估能正确从共享缓存里拼出背景周期的数据。
	last := calls[len(calls)-1]
	if _, ok := last[types.TF15m]; !ok {
		t.Error("feeds 里应该有触发周期 15m 的数据")
	}
	if _, ok := last[types.TF1h]; !ok {
		t.Error("feeds 里应该有背景周期 1h 的数据")
	}
}

func TestRunSymbolFeedStopsOnContextCancelWithoutSubscribing(t *testing.T) {
	hist := newFakeHistSource()
	live := newFakeLiveSource()
	proc := &fakeProcessor{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 一开始就取消

	done := make(chan struct{})
	go func() {
		runSymbolFeed(ctx, proc, newCandleCache(), nil, hist, live, "BTCUSDT", types.TF1h, 300, 1200, quietLogger())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 已取消时应立即返回")
	}
}
