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

// multiTFStrategy builds a strategy whose trigger timeframe is tf but with
// one module using a slower ctxTF, for testing runLive/runSymbolFeed's
// multi-timeframe dispatch logic.
func multiTFStrategy(id, symbol string, tf, ctxTF types.Timeframe) types.StrategyConfig {
	cfg := testStrategy(id, symbol, tf)
	cfg.Modules = []types.ModuleConfig{{Module: "ctx", Timeframe: ctxTF}}
	return cfg
}

// ---- fake data sources/processor ----

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

// fakeLiveSource maintains a channel queue per feedKey: each Subscribe call
// hands out the next one, letting tests simulate "reconnecting after a
// disconnect gets a new connection".
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

// fakeProcessor records the feeds received on each Process call, for
// asserting window contents.
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

// waitFor polls cond until it's true or the timeout expires; used in tests
// to wait for an async side effect instead of a fixed sleep.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

// ---- tests ----

func TestRunSymbolFeedBackfillsThenProcessesLiveCandles(t *testing.T) {
	k := feedKey{"BTCUSDT", types.TF1h}
	hist := newFakeHistSource()
	hist.candles[k] = []types.Candle{testCandle(100), testCandle(101)}

	live := newFakeLiveSource()
	ch := make(chan types.Candle, 1)
	live.enqueue(k, ch)
	ch <- testCandle(102)
	close(ch) // closing simulates "this batch of pushes ended", so the range loop exits into the reconnect branch

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
		t.Fatal("expected at least one Process call")
	}
	last := calls[len(calls)-1][types.TF1h]
	if len(last.Candles) != 3 {
		t.Fatalf("last call's window should have 3 candles (2 backfilled + 1 live), got %d", len(last.Candles))
	}
	if !last.Candles[2].Close.Equal(decimal.NewFromFloat(102)) {
		t.Errorf("latest close = %s, want 102", last.Candles[2].Close)
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
		// window=2: 3 backfilled + 1 live = 4 candles, should be trimmed to the last 2.
		runSymbolFeed(ctx, proc, newCandleCache(), strategies, hist, live, "BTCUSDT", types.TF1h, 300, 2, quietLogger())
		close(done)
	}()

	waitFor(t, 2*time.Second, func() bool { return len(proc.snapshot()) >= 1 })
	cancel()
	<-done

	last := proc.snapshot()[len(proc.snapshot())-1][types.TF1h]
	if len(last.Candles) != 2 {
		t.Fatalf("window should be trimmed to 2 candles, got %d", len(last.Candles))
	}
	if !last.Candles[1].Close.Equal(decimal.NewFromFloat(4)) {
		t.Errorf("latest close after trimming = %s, want 4", last.Candles[1].Close)
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
		t.Fatal("runSymbolFeed should return immediately on backfill failure instead of hanging")
	}
	if live.callCount(k) != 0 {
		t.Error("a backfill failure should not go on to subscribe to live market data")
	}
}

func TestRunSymbolFeedReconnectsAfterChannelCloses(t *testing.T) {
	k := feedKey{"BTCUSDT", types.TF1h}
	hist := newFakeHistSource()

	live := newFakeLiveSource()
	first := make(chan types.Candle, 1)
	first <- testCandle(1)
	close(first) // simulates the first subscription disconnecting right after one candle
	live.enqueue(k, first)

	second := make(chan types.Candle, 1)
	second <- testCandle(2)
	close(second)
	live.enqueue(k, second)

	proc := &fakeProcessor{}
	strategies := []types.StrategyConfig{testStrategy("s1", "BTCUSDT", types.TF1h)}

	// Temporarily shorten the reconnect backoff so the test doesn't actually wait 3 seconds.
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
		t.Fatalf("should resubscribe after a disconnect, Subscribe call count = %d, want at least 2", live.callCount(k))
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
		t.Fatal("the good feed should not be held back by the bad one")
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
	// Two strategies share the same (symbol, timeframe): should only be backfilled/subscribed once.
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
		t.Errorf("FetchCandles call count = %d, want 1 (one feed backfilled only once)", hist.callCount(k))
	}
}

// strategyRecordingProcessor records the feeds from each call just like
// fakeProcessor, but additionally records which strategy triggered it —
// TestRunLiveSharesFeedAcrossDifferentUsersSameSymbolTimeframe needs to prove
// "both different users' strategies were each evaluated", not just "Process
// was called N times".
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

// TestRunLiveSharesFeedAcrossDifferentUsersSameSymbolTimeframe is direct
// evidence for the multi-tenant concurrent execution rework: when two
// different users each have a strategy on the same (symbol, timeframe),
// runLive should backfill/subscribe only once (avoiding duplicate market
// data fetches, which is the core benefit of merging into one process
// serving multiple users), and both users' strategies should be evaluated
// when the same new candle arrives — not just "the parameter was passed
// through", but that two strategies genuinely belonging to different users
// both actually got a decision computed. live.go itself needs no new code
// for this by design (feedKey never had a user dimension), so this test
// exists to prove that fact and to fail loudly if someone later
// accidentally introduces a user dimension into feedKey.
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
		t.Errorf("FetchCandles call count = %d, want 1 (two users share the same feed, backfilled once)", hist.callCount(k))
	}
	if live.callCount(k) != 1 {
		t.Errorf("Subscribe call count = %d, want 1 (two users share the same subscription)", live.callCount(k))
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
		t.Error("user A's strategy should have been evaluated, but wasn't")
	}
	if !sawB {
		t.Error("user B's strategy should have been evaluated, but wasn't")
	}
}

// TestRunLiveRescanStartsSubscriptionForNewlyAppearedFeedKey verifies that
// the periodic rescan opens a new subscription for a newly-appeared
// (symbol, timeframe) combination without a process restart — this is the
// signal-engine-side implementation of the design goal that, after sharing
// one process across multiple users, "a new user's config taking effect
// shouldn't interrupt anyone else". The first reload returns only the
// ETHUSDT strategy; the second (simulating a rescan that discovers a
// new user/strategy) adds BTCUSDT. The test asserts the BTCUSDT feed is
// only backfilled/subscribed after the rescan, not from the start (proving
// it's really the rescan taking effect, not a coincidence of everything
// being passed in upfront).
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
			// The rescan loop fires a tick immediately on startup — this
			// call returns the old list, simulating "no new strategy found
			// yet"; the real new strategy only appears on the next tick.
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

	// Initially only the ETH feed should be touched; BTC should not be touched yet.
	waitFor(t, 2*time.Second, func() bool { return hist.callCount(ethKey) >= 1 })
	if hist.callCount(btcKey) != 0 {
		t.Errorf("BTC feed should not be backfilled before the rescan discovers the new strategy, but was called %d times", hist.callCount(btcKey))
	}

	// Wait for the rescan to add BTC.
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
		t.Errorf("BTC feed should be backfilled exactly once, got %d", hist.callCount(btcKey))
	}
}

// Multi-timeframe strategy: trigger timeframe is 15m, one module uses a
// slower 1h background timeframe. Only the trigger timeframe receiving a new
// candle should evaluate the strategy; evaluation should be able to assemble
// the background timeframe's data from the shared cache.
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

	// The trigger timeframe's channel sends two candles with a delay before
	// closing: even if the background timeframe's backfill hasn't settled in
	// its other goroutine by the time the first candle arrives (buildFeeds
	// just skips that evaluation with a warning log), it's guaranteed to have
	// settled by the time the second one arrives — this avoids flaky failures
	// from goroutine scheduling order.
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
	time.Sleep(150 * time.Millisecond) // leave time for the second trigger candle to be processed
	cancel()
	<-done

	calls := proc.snapshot()
	if len(calls) == 0 {
		t.Fatal("the strategy should be evaluated when the trigger timeframe receives a new candle")
	}
	if len(calls) > 2 {
		t.Fatalf("only 2 trigger-timeframe candles were pushed, evaluation count should not exceed 2, got %d", len(calls))
	}
	// buildFeeds only calls Process once every required timeframe is
	// present, so if it was called at all, both the 15m (trigger) and 1h
	// (background) feeds must be present — this is exactly the core behavior
	// being verified: the trigger timeframe's evaluation can correctly
	// assemble the background timeframe's data from the shared cache.
	last := calls[len(calls)-1]
	if _, ok := last[types.TF15m]; !ok {
		t.Error("feeds should contain the trigger timeframe's (15m) data")
	}
	if _, ok := last[types.TF1h]; !ok {
		t.Error("feeds should contain the background timeframe's (1h) data")
	}
}

func TestRunSymbolFeedStopsOnContextCancelWithoutSubscribing(t *testing.T) {
	hist := newFakeHistSource()
	live := newFakeLiveSource()
	proc := &fakeProcessor{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled from the start

	done := make(chan struct{})
	go func() {
		runSymbolFeed(ctx, proc, newCandleCache(), nil, hist, live, "BTCUSDT", types.TF1h, 300, 1200, quietLogger())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("should return immediately when ctx is already canceled")
	}
}
