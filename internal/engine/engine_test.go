package engine

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/internal/modules"
	"tradeforge/pkg/types"
)

var start = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------- 可控的假模块 ----------

// fakeModule 是一个行为完全可控的模块，用于构造引擎需要处理的各种情况。
type fakeModule struct {
	name  string
	dir   types.Direction
	conf  float64
	err   error
	delay time.Duration
	panic bool
}

func (f *fakeModule) Name() string        { return f.name }
func (f *fakeModule) Description() string { return "测试用假模块" }
func (f *fakeModule) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{{Name: "k", Type: types.ParamInt, Default: 1, Min: types.F(1), Max: types.F(10)}}
}

func (f *fakeModule) Evaluate(ctx context.Context, md types.MarketData, params map[string]any) (types.Signal, error) {
	if _, err := types.ResolveParams(f.name, f.RequiredParams(), params); err != nil {
		return types.Signal{}, err
	}
	if f.panic {
		panic("假模块故意 panic")
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return types.Signal{}, ctx.Err()
		}
	}
	if f.err != nil {
		return types.Signal{}, f.err
	}
	return types.Signal{
		Module: f.name, Symbol: md.Symbol, Direction: f.dir, Confidence: f.conf,
		Timestamp: md.Time(), Price: decimal.NewFromInt(100), Reason: "假模块输出",
	}, nil
}

func registryOf(mods ...modules.SignalModule) *modules.Registry {
	r := modules.NewRegistry()
	for _, m := range mods {
		r.Register(m)
	}
	return r
}

func cfgFor(combine types.CombineMode, threshold float64, mcs ...types.ModuleConfig) types.StrategyConfig {
	return types.StrategyConfig{
		ID: "11111111-1111-4111-8111-111111111111", Name: "测试策略",
		Symbol: "BTCUSDT", Timeframe: types.TF1h,
		Modules: mcs, Combine: combine, Threshold: threshold,
		Risk:  types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State: types.StateDraft,
	}
}

func marketData() types.MarketData {
	return synth.New("BTCUSDT", types.TF1h, start).Trend(60, 100, 110, 1000, 0.6).Build()
}

// feedsFor 把单个 MarketData 包成 Evaluate/Process 现在要求的 feeds map，
// 键用它自己的 Timeframe——单周期场景下这就是唯一需要的那一份。
func feedsFor(md types.MarketData) map[types.Timeframe]types.MarketData {
	return map[types.Timeframe]types.MarketData{md.Timeframe: md}
}

// ---------- ALL 组合 ----------

func TestAggregateAllTriggersWhenAllAgree(t *testing.T) {
	reg := registryOf(
		&fakeModule{name: "a", dir: types.DirectionLong, conf: 0.8},
		&fakeModule{name: "b", dir: types.DirectionLong, conf: 0.6},
		&fakeModule{name: "c", dir: types.DirectionLong, conf: 0.7},
	)
	cfg := cfgFor(types.CombineAll, 0,
		types.ModuleConfig{Module: "a"}, types.ModuleConfig{Module: "b"}, types.ModuleConfig{Module: "c"})

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(marketData()))
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if !d.Triggered {
		t.Fatalf("期望触发，实际未触发：%s", d.Reason)
	}
	if d.Direction != types.DirectionLong {
		t.Errorf("方向 = %s，期望 LONG", d.Direction)
	}
	if want := (0.8 + 0.6 + 0.7) / 3; d.Score < want-1e-9 || d.Score > want+1e-9 {
		t.Errorf("Score = %v，期望平均置信度 %v", d.Score, want)
	}
	if len(d.Signals) != 3 {
		t.Errorf("信号数 = %d，期望 3（可解释性要求保留全部参与信号）", len(d.Signals))
	}
}

func TestAggregateAllBlockedByOpposingModule(t *testing.T) {
	reg := registryOf(
		&fakeModule{name: "a", dir: types.DirectionLong, conf: 0.9},
		&fakeModule{name: "b", dir: types.DirectionShort, conf: 0.9},
	)
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "a"}, types.ModuleConfig{Module: "b"})

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(marketData()))
	if err != nil {
		t.Fatal(err)
	}
	if d.Triggered {
		t.Fatal("方向相反时不应触发")
	}
	if !strings.Contains(d.Reason, "相反") {
		t.Errorf("Reason 应说明是方向冲突导致的，实际：%s", d.Reason)
	}
}

func TestAggregateAllBlockedByNeutralModule(t *testing.T) {
	reg := registryOf(
		&fakeModule{name: "a", dir: types.DirectionLong, conf: 0.9},
		&fakeModule{name: "b", dir: types.DirectionNeutral, conf: 0},
	)
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "a"}, types.ModuleConfig{Module: "b"})

	d, _ := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(marketData()))
	if d.Triggered {
		t.Fatalf("有模块中性时 ALL 不应触发：%s", d.Reason)
	}
}

// ---------- WEIGHTED 组合 ----------

func TestAggregateWeightedTriggersAboveThreshold(t *testing.T) {
	reg := registryOf(
		&fakeModule{name: "a", dir: types.DirectionLong, conf: 0.9},
		&fakeModule{name: "b", dir: types.DirectionLong, conf: 0.8},
	)
	cfg := cfgFor(types.CombineWeighted, 0.5,
		types.ModuleConfig{Module: "a", Weight: 0.6},
		types.ModuleConfig{Module: "b", Weight: 0.4})

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(marketData()))
	if err != nil {
		t.Fatal(err)
	}
	// (0.6*0.9 + 0.4*0.8) / 1.0 = 0.86
	if want := 0.86; d.Score < want-1e-9 || d.Score > want+1e-9 {
		t.Errorf("Score = %v，期望 %v", d.Score, want)
	}
	if !d.Triggered || d.Direction != types.DirectionLong {
		t.Errorf("期望触发 LONG，实际 triggered=%v dir=%s：%s", d.Triggered, d.Direction, d.Reason)
	}
}

func TestAggregateWeightedNetsOutOpposingSignals(t *testing.T) {
	reg := registryOf(
		&fakeModule{name: "a", dir: types.DirectionLong, conf: 0.9},
		&fakeModule{name: "b", dir: types.DirectionShort, conf: 0.9},
	)
	cfg := cfgFor(types.CombineWeighted, 0.3,
		types.ModuleConfig{Module: "a", Weight: 0.5},
		types.ModuleConfig{Module: "b", Weight: 0.5})

	d, _ := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(marketData()))
	if d.Triggered {
		t.Fatalf("等权反向信号应互相抵消，不该触发：%s", d.Reason)
	}
	if d.Score > 1e-9 || d.Score < -1e-9 {
		t.Errorf("Score = %v，期望约 0", d.Score)
	}
}

func TestAggregateWeightedShortDirection(t *testing.T) {
	reg := registryOf(&fakeModule{name: "a", dir: types.DirectionShort, conf: 0.9})
	cfg := cfgFor(types.CombineWeighted, 0.5, types.ModuleConfig{Module: "a", Weight: 1.0})

	d, _ := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(marketData()))
	if !d.Triggered || d.Direction != types.DirectionShort {
		t.Fatalf("期望触发 SHORT，实际 triggered=%v dir=%s", d.Triggered, d.Direction)
	}
	if d.Score >= 0 {
		t.Errorf("空头 Score = %v，期望为负", d.Score)
	}
}

// 沉默的模块必须计入分母，否则少数模块能独自把分数顶到阈值以上。
func TestWeightedSilentModulesDiluteScore(t *testing.T) {
	reg := registryOf(
		&fakeModule{name: "a", dir: types.DirectionLong, conf: 1.0},
		&fakeModule{name: "b", dir: types.DirectionNeutral, conf: 0},
		&fakeModule{name: "c", dir: types.DirectionNeutral, conf: 0},
	)
	cfg := cfgFor(types.CombineWeighted, 0.6,
		types.ModuleConfig{Module: "a", Weight: 0.5},
		types.ModuleConfig{Module: "b", Weight: 0.25},
		types.ModuleConfig{Module: "c", Weight: 0.25})

	d, _ := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(marketData()))
	// 0.5*1.0 / (0.5+0.25+0.25) = 0.5，低于阈值 0.6
	if d.Triggered {
		t.Fatalf("中性模块应稀释分数使其低于阈值，实际触发了：%s", d.Reason)
	}
	if want := 0.5; d.Score < want-1e-9 || d.Score > want+1e-9 {
		t.Errorf("Score = %v，期望 %v", d.Score, want)
	}
}

// ---------- 隔离与降级 ----------

func TestModuleTimeoutDegradesWithoutBlockingOthers(t *testing.T) {
	reg := registryOf(
		&fakeModule{name: "fast", dir: types.DirectionLong, conf: 0.9},
		&fakeModule{name: "slow", dir: types.DirectionLong, conf: 0.9, delay: 2 * time.Second},
	)
	cfg := cfgFor(types.CombineWeighted, 0.4,
		types.ModuleConfig{Module: "fast", Weight: 0.5},
		types.ModuleConfig{Module: "slow", Weight: 0.5})

	e := New(reg, WithTimeout(50*time.Millisecond), WithLogger(quietLogger()))

	begin := time.Now()
	d, err := e.Evaluate(context.Background(), cfg, feedsFor(marketData()))
	elapsed := time.Since(begin)

	if err != nil {
		t.Fatalf("单模块超时不应让整个引擎报错：%v", err)
	}
	if elapsed > time.Second {
		t.Errorf("耗时 %v：慢模块拖垮了整个引擎，超时未生效", elapsed)
	}

	byName := signalsByModule(d.Signals)
	if !byName["slow"].Degraded {
		t.Error("超时模块应被标记为 Degraded")
	}
	if byName["slow"].Direction != types.DirectionNeutral {
		t.Errorf("降级信号方向 = %s，期望 NEUTRAL", byName["slow"].Direction)
	}
	if byName["fast"].Degraded {
		t.Error("正常模块不应受慢模块影响")
	}
	if byName["fast"].Direction != types.DirectionLong {
		t.Errorf("正常模块方向 = %s，期望 LONG", byName["fast"].Direction)
	}
}

func TestModuleErrorDegradesGracefully(t *testing.T) {
	reg := registryOf(
		&fakeModule{name: "ok", dir: types.DirectionLong, conf: 0.9},
		&fakeModule{name: "bad", err: errors.New("数据源炸了")},
	)
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "ok"}, types.ModuleConfig{Module: "bad"})

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(marketData()))
	if err != nil {
		t.Fatalf("模块报错不应让引擎报错：%v", err)
	}
	bad := signalsByModule(d.Signals)["bad"]
	if !bad.Degraded {
		t.Error("报错的模块应被标记为 Degraded")
	}
	if !strings.Contains(bad.Err, "数据源炸了") {
		t.Errorf("降级信号应保留原始错误，实际：%q", bad.Err)
	}
	// ALL 组合下有降级模块就不该触发。
	if d.Triggered {
		t.Errorf("有模块降级时 ALL 不应触发：%s", d.Reason)
	}
}

// 模块 panic 必须被隔离，否则一个模块的 bug 会让整个引擎进程崩溃。
func TestModulePanicIsIsolated(t *testing.T) {
	reg := registryOf(
		&fakeModule{name: "ok", dir: types.DirectionLong, conf: 0.9},
		&fakeModule{name: "boom", panic: true},
	)
	cfg := cfgFor(types.CombineWeighted, 0.3,
		types.ModuleConfig{Module: "ok", Weight: 0.5},
		types.ModuleConfig{Module: "boom", Weight: 0.5})

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(marketData()))
	if err != nil {
		t.Fatalf("模块 panic 不应让引擎报错：%v", err)
	}
	boom := signalsByModule(d.Signals)["boom"]
	if !boom.Degraded {
		t.Error("panic 的模块应被标记为 Degraded")
	}
	if !strings.Contains(boom.Err, "panic") {
		t.Errorf("降级信号应说明是 panic，实际：%q", boom.Err)
	}
	if signalsByModule(d.Signals)["ok"].Direction != types.DirectionLong {
		t.Error("panic 模块不应影响其它模块的结果")
	}
}

// ---------- 配置校验 ----------

func TestEvaluateRejectsInvalidConfig(t *testing.T) {
	reg := registryOf(&fakeModule{name: "a", dir: types.DirectionLong, conf: 0.9})
	cases := []struct {
		name string
		cfg  types.StrategyConfig
	}{
		{"引用不存在的模块", cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "不存在的模块"})},
		{"模块参数越界", cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "a", Params: map[string]any{"k": 999}})},
		{"没有任何模块", cfgFor(types.CombineAll, 0)},
		{"WEIGHTED 未配权重", cfgFor(types.CombineWeighted, 0.5, types.ModuleConfig{Module: "a"})},
		{"WEIGHTED 阈值非法", cfgFor(types.CombineWeighted, 1.5, types.ModuleConfig{Module: "a", Weight: 1})},
	}
	e := New(reg, WithLogger(quietLogger()))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.Evaluate(context.Background(), tc.cfg, feedsFor(marketData())); err == nil {
				t.Fatal("期望校验失败，实际通过了")
			}
		})
	}
}

// 标的错配必须拦下：不同标的的策略是一等公民，绝不能拿 ETH 的行情跑 BTC 的策略。
func TestEvaluateRejectsSymbolMismatch(t *testing.T) {
	reg := registryOf(&fakeModule{name: "a", dir: types.DirectionLong, conf: 0.9})
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "a"})
	md := synth.New("ETHUSDT", types.TF1h, start).Trend(60, 100, 110, 1000, 0.6).Build()

	_, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(md))
	if err == nil {
		t.Fatal("期望拒绝标的不一致的行情")
	}
	if !strings.Contains(err.Error(), "隔离") {
		t.Errorf("错误信息应点明标的隔离要求，实际：%v", err)
	}
}

// ---------- Process：审计与发布 ----------

type recordingAuditor struct {
	mu        sync.Mutex
	decisions []types.Decision
	err       error
}

func (a *recordingAuditor) RecordDecision(_ context.Context, d types.Decision) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.err != nil {
		return a.err
	}
	a.decisions = append(a.decisions, d)
	return nil
}

type recordingPublisher struct {
	mu        sync.Mutex
	decisions []types.Decision
	err       error
}

func (p *recordingPublisher) PublishDecision(_ context.Context, d types.Decision) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.decisions = append(p.decisions, d)
	return nil
}

func TestProcessAuditsAndPublishes(t *testing.T) {
	reg := registryOf(&fakeModule{name: "a", dir: types.DirectionLong, conf: 0.9})
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "a"})
	aud, pub := &recordingAuditor{}, &recordingPublisher{}

	e := New(reg, WithAuditor(aud), WithPublisher(pub), WithLogger(quietLogger()))
	d, err := e.Process(context.Background(), cfg, feedsFor(marketData()))
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if d.ID == "" {
		t.Error("决策必须带 ID，订单溯源要靠它反查")
	}
	if len(aud.decisions) != 1 || len(pub.decisions) != 1 {
		t.Fatalf("审计 %d 条、发布 %d 条，各期望 1 条", len(aud.decisions), len(pub.decisions))
	}
	if aud.decisions[0].ID != d.ID || pub.decisions[0].ID != d.ID {
		t.Error("审计与发布的必须是同一条决策")
	}
}

// 审计失败必须中止发布：绝不能出现"执行层已下单、审计表查无此决策"。
func TestProcessAbortsPublishWhenAuditFails(t *testing.T) {
	reg := registryOf(&fakeModule{name: "a", dir: types.DirectionLong, conf: 0.9})
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "a"})
	aud := &recordingAuditor{err: errors.New("数据库不可用")}
	pub := &recordingPublisher{}

	e := New(reg, WithAuditor(aud), WithPublisher(pub), WithLogger(quietLogger()))
	if _, err := e.Process(context.Background(), cfg, feedsFor(marketData())); err == nil {
		t.Fatal("审计失败时 Process 应返回错误")
	}
	if len(pub.decisions) != 0 {
		t.Error("审计失败后不得继续发布决策")
	}
}

// 未触发的决策同样要留痕：用户需要知道"为什么这根 K 线没有开仓"。
func TestNonTriggeredDecisionsAreStillAudited(t *testing.T) {
	reg := registryOf(&fakeModule{name: "a", dir: types.DirectionNeutral, conf: 0})
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "a"})
	aud := &recordingAuditor{}

	e := New(reg, WithAuditor(aud), WithLogger(quietLogger()))
	d, err := e.Process(context.Background(), cfg, feedsFor(marketData()))
	if err != nil {
		t.Fatal(err)
	}
	if d.Triggered {
		t.Fatal("中性信号不应触发")
	}
	if len(aud.decisions) != 1 {
		t.Fatal("未触发的决策也必须落库审计")
	}
	if aud.decisions[0].Reason == "" {
		t.Error("未触发时更要说明原因")
	}
}

// ---------- 多周期 ----------

// tfEchoModule 把自己实际收到的 MarketData.Timeframe 塞进 Signal.Reason 里，
// 用来断言引擎有没有把正确周期的行情路由给正确的模块。
type tfEchoModule struct{ name string }

func (f *tfEchoModule) Name() string        { return f.name }
func (f *tfEchoModule) Description() string { return "回显收到的周期" }
func (f *tfEchoModule) RequiredParams() []types.ParamSpec { return nil }
func (f *tfEchoModule) Evaluate(ctx context.Context, md types.MarketData, params map[string]any) (types.Signal, error) {
	return types.Signal{
		Module: f.name, Symbol: md.Symbol, Direction: types.DirectionLong, Confidence: 0.9,
		Timestamp: md.Time(), Price: decimal.NewFromInt(100), Reason: "看到的周期：" + string(md.Timeframe),
	}, nil
}

func TestEvaluateRoutesEachModuleToItsOwnTimeframe(t *testing.T) {
	reg := registryOf(&tfEchoModule{name: "slow"}, &tfEchoModule{name: "fast"})
	cfg := cfgFor(types.CombineAll, 0,
		types.ModuleConfig{Module: "slow", Timeframe: types.TF1h},
		types.ModuleConfig{Module: "fast"}, // 留空，跟随触发周期
	)
	cfg.Timeframe = types.TF15m

	feeds := map[types.Timeframe]types.MarketData{
		types.TF15m: synth.New("BTCUSDT", types.TF15m, start).Trend(30, 100, 110, 1000, 0.6).Build(),
		types.TF1h:  synth.New("BTCUSDT", types.TF1h, start).Trend(30, 100, 110, 1000, 0.6).Build(),
	}

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feeds)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	byName := signalsByModule(d.Signals)
	if !strings.Contains(byName["slow"].Reason, "1h") {
		t.Errorf("slow 模块应看到 1h 周期的行情，实际 Reason=%q", byName["slow"].Reason)
	}
	if !strings.Contains(byName["fast"].Reason, "15m") {
		t.Errorf("fast 模块应跟随触发周期看到 15m 行情，实际 Reason=%q", byName["fast"].Reason)
	}
	if d.Timestamp != feeds[types.TF15m].Time() {
		t.Errorf("Decision.Timestamp 应取自触发周期的 feed")
	}
}

func TestEvaluateDegradesModuleWhenItsTimeframeFeedMissing(t *testing.T) {
	reg := registryOf(&tfEchoModule{name: "needs1h"})
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "needs1h", Timeframe: types.TF1h})
	cfg.Timeframe = types.TF15m

	// 只提供触发周期的 feed，不提供模块需要的 1h feed。
	feeds := map[types.Timeframe]types.MarketData{
		types.TF15m: synth.New("BTCUSDT", types.TF15m, start).Trend(30, 100, 110, 1000, 0.6).Build(),
	}

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feeds)
	if err != nil {
		t.Fatalf("缺少某个模块所需的 feed 不应让整个 Evaluate 报错：%v", err)
	}
	sig := signalsByModule(d.Signals)["needs1h"]
	if !sig.Degraded {
		t.Error("缺少所需周期行情的模块应降级为中性信号，而不是让引擎报错")
	}
}

func TestEvaluateRejectsMissingTriggerFeed(t *testing.T) {
	reg := registryOf(&tfEchoModule{name: "a"})
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "a"})
	cfg.Timeframe = types.TF15m

	// feeds 里完全没有触发周期 15m 的数据。
	feeds := map[types.Timeframe]types.MarketData{
		types.TF1h: synth.New("BTCUSDT", types.TF1h, start).Trend(30, 100, 110, 1000, 0.6).Build(),
	}

	if _, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feeds); err == nil {
		t.Fatal("缺少触发周期的 feed 应该直接报错，它决定整条决策的时间戳和价格")
	}
}

func signalsByModule(signals []types.Signal) map[string]types.Signal {
	out := make(map[string]types.Signal, len(signals))
	for _, s := range signals {
		out[s.Module] = s
	}
	return out
}
