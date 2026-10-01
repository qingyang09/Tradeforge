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

	"tradeforge/internal/i18n"
	"tradeforge/internal/marketdata/synth"
	"tradeforge/internal/modules"
	"tradeforge/pkg/types"
)

var start = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------- controllable fake module ----------

// fakeModule is a module with fully controllable behavior, used to
// construct the various situations the engine needs to handle.
type fakeModule struct {
	name  string
	dir   types.Direction
	conf  float64
	err   error
	delay time.Duration
	panic bool
}

func (f *fakeModule) Name() string { return f.name }
func (f *fakeModule) Description() types.Message {
	return types.Message{Literal: "fake module for testing"}
}
func (f *fakeModule) RequiredParams() []types.ParamSpec {
	return []types.ParamSpec{{Name: "k", Type: types.ParamInt, Default: 1, Min: types.F(1), Max: types.F(10)}}
}

func (f *fakeModule) Evaluate(ctx context.Context, md types.MarketData, params map[string]any) (types.Signal, error) {
	if _, err := types.ResolveParams(f.name, f.RequiredParams(), params); err != nil {
		return types.Signal{}, err
	}
	if f.panic {
		panic("fake module deliberately panicking")
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
		Timestamp: md.Time(), Price: decimal.NewFromInt(100), Reason: types.Message{Literal: "fake module output"},
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
		ID: "11111111-1111-4111-8111-111111111111", Name: "test strategy",
		Symbol: "BTCUSDT", Timeframe: types.TF1h,
		Modules: mcs, Combine: combine, Threshold: threshold,
		Risk:  types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State: types.StateDraft,
	}
}

func marketData() types.MarketData {
	return synth.New("BTCUSDT", types.TF1h, start).Trend(60, 100, 110, 1000, 0.6).Build()
}

// feedsFor wraps a single MarketData into the feeds map that
// Evaluate/Process now require, keyed by its own Timeframe — in a
// single-timeframe scenario, that's the only entry needed.
func feedsFor(md types.MarketData) map[types.Timeframe]types.MarketData {
	return map[types.Timeframe]types.MarketData{md.Timeframe: md}
}

// ---------- ALL combine ----------

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
		t.Fatalf("unexpected error: %v", err)
	}
	if !d.Triggered {
		t.Fatalf("expected trigger, got none: %s", d.Reason)
	}
	if d.Direction != types.DirectionLong {
		t.Errorf("Direction = %s, want LONG", d.Direction)
	}
	if want := (0.8 + 0.6 + 0.7) / 3; d.Score < want-1e-9 || d.Score > want+1e-9 {
		t.Errorf("Score = %v, want average confidence %v", d.Score, want)
	}
	if len(d.Signals) != 3 {
		t.Errorf("signal count = %d, want 3 (explainability requires keeping every participating signal)", len(d.Signals))
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
		t.Fatal("should not trigger when directions oppose")
	}
	// NOTE: "相反" kept untranslated — it's the literal Chinese substring
	// produced by aggregate.go's aggregateAll (see the NOTE there); the
	// assertion has to match what production code actually emits. The
	// per-module blocker text is rendered at construction time in
	// i18n.DefaultLang (Chinese) regardless of the outer key's own language
	// (see aggregate.go's blockerLang comment), so LangZH is what's actually
	// embedded here.
	rendered := i18n.Render(i18n.LangZH, d.Reason)
	if !strings.Contains(rendered, "相反") {
		t.Errorf("Reason should explain the direction conflict, got: %s", rendered)
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
		t.Fatalf("ALL should not trigger when a module is neutral: %s", d.Reason)
	}
}

// ---------- WEIGHTED combine ----------

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
		t.Errorf("Score = %v, want %v", d.Score, want)
	}
	if !d.Triggered || d.Direction != types.DirectionLong {
		t.Errorf("expected trigger LONG, got triggered=%v dir=%s: %s", d.Triggered, d.Direction, d.Reason)
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
		t.Fatalf("equally-weighted opposing signals should cancel out and not trigger: %s", d.Reason)
	}
	if d.Score > 1e-9 || d.Score < -1e-9 {
		t.Errorf("Score = %v, want approximately 0", d.Score)
	}
}

func TestAggregateWeightedShortDirection(t *testing.T) {
	reg := registryOf(&fakeModule{name: "a", dir: types.DirectionShort, conf: 0.9})
	cfg := cfgFor(types.CombineWeighted, 0.5, types.ModuleConfig{Module: "a", Weight: 1.0})

	d, _ := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(marketData()))
	if !d.Triggered || d.Direction != types.DirectionShort {
		t.Fatalf("expected trigger SHORT, got triggered=%v dir=%s", d.Triggered, d.Direction)
	}
	if d.Score >= 0 {
		t.Errorf("short Score = %v, want negative", d.Score)
	}
}

// Silent modules must still count toward the denominator, or a minority of
// modules could push the score past the threshold on their own.
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
	// 0.5*1.0 / (0.5+0.25+0.25) = 0.5, below the 0.6 threshold
	if d.Triggered {
		t.Fatalf("neutral modules should dilute the score below threshold, but it triggered: %s", d.Reason)
	}
	if want := 0.5; d.Score < want-1e-9 || d.Score > want+1e-9 {
		t.Errorf("Score = %v, want %v", d.Score, want)
	}
}

// ---------- isolation and degradation ----------

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
		t.Fatalf("a single module timing out should not error out the whole engine: %v", err)
	}
	if elapsed > time.Second {
		t.Errorf("elapsed %v: the slow module dragged down the whole engine, timeout had no effect", elapsed)
	}

	byName := signalsByModule(d.Signals)
	if !byName["slow"].Degraded {
		t.Error("timed-out module should be marked Degraded")
	}
	if byName["slow"].Direction != types.DirectionNeutral {
		t.Errorf("degraded signal Direction = %s, want NEUTRAL", byName["slow"].Direction)
	}
	if byName["fast"].Degraded {
		t.Error("a normal module should not be affected by a slow one")
	}
	if byName["fast"].Direction != types.DirectionLong {
		t.Errorf("normal module Direction = %s, want LONG", byName["fast"].Direction)
	}
}

func TestModuleErrorDegradesGracefully(t *testing.T) {
	reg := registryOf(
		&fakeModule{name: "ok", dir: types.DirectionLong, conf: 0.9},
		// NOTE: kept in Chinese — asserted verbatim below via strings.Contains.
		&fakeModule{name: "bad", err: errors.New("数据源炸了")},
	)
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "ok"}, types.ModuleConfig{Module: "bad"})

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(marketData()))
	if err != nil {
		t.Fatalf("a module erroring should not error out the engine: %v", err)
	}
	bad := signalsByModule(d.Signals)["bad"]
	if !bad.Degraded {
		t.Error("an erroring module should be marked Degraded")
	}
	if !strings.Contains(bad.Err.Literal, "数据源炸了") {
		t.Errorf("degraded signal should retain the original error, got: %+v", bad.Err)
	}
	// ALL combine should not trigger when a module is degraded.
	if d.Triggered {
		t.Errorf("ALL should not trigger when a module is degraded: %s", d.Reason)
	}
}

// A module panicking must be isolated, or a single module's bug could crash
// the entire engine process.
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
		t.Fatalf("a module panicking should not error out the engine: %v", err)
	}
	boom := signalsByModule(d.Signals)["boom"]
	if !boom.Degraded {
		t.Error("a panicking module should be marked Degraded")
	}
	if !strings.Contains(boom.Err.Literal, "panic") {
		t.Errorf("degraded signal should mention it was a panic, got: %+v", boom.Err)
	}
	if signalsByModule(d.Signals)["ok"].Direction != types.DirectionLong {
		t.Error("a panicking module should not affect other modules' results")
	}
}

// ---------- config validation ----------

func TestEvaluateRejectsInvalidConfig(t *testing.T) {
	reg := registryOf(&fakeModule{name: "a", dir: types.DirectionLong, conf: 0.9})
	cases := []struct {
		name string
		cfg  types.StrategyConfig
	}{
		{"references a nonexistent module", cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "nonexistent_module"})},
		{"module param out of range", cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "a", Params: map[string]any{"k": 999}})},
		{"no modules at all", cfgFor(types.CombineAll, 0)},
		{"WEIGHTED with no weight configured", cfgFor(types.CombineWeighted, 0.5, types.ModuleConfig{Module: "a"})},
		{"WEIGHTED with an illegal threshold", cfgFor(types.CombineWeighted, 1.5, types.ModuleConfig{Module: "a", Weight: 1})},
	}
	e := New(reg, WithLogger(quietLogger()))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := e.Evaluate(context.Background(), tc.cfg, feedsFor(marketData())); err == nil {
				t.Fatal("expected validation to fail, but it passed")
			}
		})
	}
}

// A symbol mismatch must be rejected: per-symbol strategies are a
// first-class feature, so BTC's strategy must never be run on ETH's market data.
func TestEvaluateRejectsSymbolMismatch(t *testing.T) {
	reg := registryOf(&fakeModule{name: "a", dir: types.DirectionLong, conf: 0.9})
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "a"})
	md := synth.New("ETHUSDT", types.TF1h, start).Trend(60, 100, 110, 1000, 0.6).Build()

	_, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(md))
	if err == nil {
		t.Fatal("expected mismatched-symbol market data to be rejected")
	}
	// NOTE: "隔离" kept untranslated — it's the literal Chinese substring
	// produced by engine.go's Evaluate (see the NOTE there); the assertion
	// has to match what production code actually emits.
	if !strings.Contains(err.Error(), "隔离") {
		t.Errorf("error message should call out the per-symbol isolation requirement, got: %v", err)
	}
}

// ---------- Process: audit and publish ----------

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
		t.Fatalf("unexpected error: %v", err)
	}
	if d.ID == "" {
		t.Error("a decision must carry an ID, used to trace back to the order that resulted from it")
	}
	if len(aud.decisions) != 1 || len(pub.decisions) != 1 {
		t.Fatalf("recorded %d, published %d, want 1 each", len(aud.decisions), len(pub.decisions))
	}
	if aud.decisions[0].ID != d.ID || pub.decisions[0].ID != d.ID {
		t.Error("the recorded and published decisions must be the same one")
	}
}

// A recording failure must abort the publish: there must never be a case
// where "the execution layer already placed an order but the audit table
// has no record of the decision."
func TestProcessAbortsPublishWhenAuditFails(t *testing.T) {
	reg := registryOf(&fakeModule{name: "a", dir: types.DirectionLong, conf: 0.9})
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "a"})
	aud := &recordingAuditor{err: errors.New("database unavailable")}
	pub := &recordingPublisher{}

	e := New(reg, WithAuditor(aud), WithPublisher(pub), WithLogger(quietLogger()))
	if _, err := e.Process(context.Background(), cfg, feedsFor(marketData())); err == nil {
		t.Fatal("Process should return an error when recording fails")
	}
	if len(pub.decisions) != 0 {
		t.Error("must not publish a decision after recording it failed")
	}
}

// A non-triggered decision must still be recorded: the user needs to know
// "why didn't this candle open a position."
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
		t.Fatal("a neutral signal should not trigger")
	}
	if len(aud.decisions) != 1 {
		t.Fatal("a non-triggered decision must still be recorded for audit")
	}
	if aud.decisions[0].Reason.IsZero() {
		t.Error("a non-triggered decision needs its reason even more so")
	}
}

// ---------- multi-timeframe ----------

// tfEchoModule stuffs the MarketData.Timeframe it actually received into
// Signal.Reason, used to assert whether the engine routed the right
// timeframe's market data to the right module.
type tfEchoModule struct{ name string }

func (f *tfEchoModule) Name() string { return f.name }
func (f *tfEchoModule) Description() types.Message {
	return types.Message{Literal: "echoes back the timeframe it received"}
}
func (f *tfEchoModule) RequiredParams() []types.ParamSpec { return nil }
func (f *tfEchoModule) Evaluate(ctx context.Context, md types.MarketData, params map[string]any) (types.Signal, error) {
	return types.Signal{
		Module: f.name, Symbol: md.Symbol, Direction: types.DirectionLong, Confidence: 0.9,
		Timestamp: md.Time(), Price: decimal.NewFromInt(100),
		Reason: types.Message{Literal: "timeframe seen: " + string(md.Timeframe)},
	}, nil
}

func TestEvaluateRoutesEachModuleToItsOwnTimeframe(t *testing.T) {
	reg := registryOf(&tfEchoModule{name: "slow"}, &tfEchoModule{name: "fast"})
	cfg := cfgFor(types.CombineAll, 0,
		types.ModuleConfig{Module: "slow", Timeframe: types.TF1h},
		types.ModuleConfig{Module: "fast"}, // left blank, follows the trigger timeframe
	)
	cfg.Timeframe = types.TF15m

	feeds := map[types.Timeframe]types.MarketData{
		types.TF15m: synth.New("BTCUSDT", types.TF15m, start).Trend(30, 100, 110, 1000, 0.6).Build(),
		types.TF1h:  synth.New("BTCUSDT", types.TF1h, start).Trend(30, 100, 110, 1000, 0.6).Build(),
	}

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feeds)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byName := signalsByModule(d.Signals)
	if !strings.Contains(byName["slow"].Reason.Literal, "1h") {
		t.Errorf("the slow module should see 1h market data, got Reason=%q", byName["slow"].Reason.Literal)
	}
	if !strings.Contains(byName["fast"].Reason.Literal, "15m") {
		t.Errorf("the fast module should follow the trigger timeframe and see 15m market data, got Reason=%q", byName["fast"].Reason.Literal)
	}
	if d.Timestamp != feeds[types.TF15m].Time() {
		t.Errorf("Decision.Timestamp should come from the trigger timeframe's feed")
	}
}

func TestEvaluateDegradesModuleWhenItsTimeframeFeedMissing(t *testing.T) {
	reg := registryOf(&tfEchoModule{name: "needs1h"})
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "needs1h", Timeframe: types.TF1h})
	cfg.Timeframe = types.TF15m

	// Only supply the trigger timeframe's feed, not the 1h feed the module needs.
	feeds := map[types.Timeframe]types.MarketData{
		types.TF15m: synth.New("BTCUSDT", types.TF15m, start).Trend(30, 100, 110, 1000, 0.6).Build(),
	}

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feeds)
	if err != nil {
		t.Fatalf("a missing feed for one module's timeframe should not error out the whole Evaluate: %v", err)
	}
	sig := signalsByModule(d.Signals)["needs1h"]
	if !sig.Degraded {
		t.Error("a module missing its required timeframe's market data should degrade to neutral, not error out the engine")
	}
}

func TestEvaluateRejectsMissingTriggerFeed(t *testing.T) {
	reg := registryOf(&tfEchoModule{name: "a"})
	cfg := cfgFor(types.CombineAll, 0, types.ModuleConfig{Module: "a"})
	cfg.Timeframe = types.TF15m

	// feeds has no data at all for the 15m trigger timeframe.
	feeds := map[types.Timeframe]types.MarketData{
		types.TF1h: synth.New("BTCUSDT", types.TF1h, start).Trend(30, 100, 110, 1000, 0.6).Build(),
	}

	if _, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feeds); err == nil {
		t.Fatal("a missing trigger-timeframe feed should error out immediately, since it determines the whole decision's timestamp and price")
	}
}

func signalsByModule(signals []types.Signal) map[string]types.Signal {
	out := make(map[string]types.Signal, len(signals))
	for _, s := range signals {
		out[s.Module] = s
	}
	return out
}
