// Package engine implements the module combination engine: it calls signal
// modules concurrently, aggregates their output into a decision, publishes
// it to the message queue, and records it for audit.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"tradeforge/internal/modules"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// DefaultModuleTimeout is the default timeout for a single module's Evaluate.
const DefaultModuleTimeout = 3 * time.Second

// Publisher publishes a decision to the message queue for consumption by
// the execution layer and the backtest engine.
type Publisher interface {
	PublishDecision(ctx context.Context, d types.Decision) error
}

// Auditor persists a decision, forming an audit trail.
type Auditor interface {
	RecordDecision(ctx context.Context, d types.Decision) error
}

// Option configures the engine.
type Option func(*Engine)

// WithTimeout sets the per-module timeout.
func WithTimeout(d time.Duration) Option {
	return func(e *Engine) {
		if d > 0 {
			e.timeout = d
		}
	}
}

// WithPublisher sets the decision publishing channel. If unset, decisions aren't published.
func WithPublisher(p Publisher) Option { return func(e *Engine) { e.publisher = p } }

// WithAuditor sets the audit persistence channel. If unset, decisions aren't recorded.
func WithAuditor(a Auditor) Option { return func(e *Engine) { e.auditor = a } }

// WithLogger sets the logger.
func WithLogger(l *slog.Logger) Option {
	return func(e *Engine) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithIDFunc overrides how decision IDs are generated, so tests can produce
// deterministic IDs.
func WithIDFunc(f func() string) Option {
	return func(e *Engine) {
		if f != nil {
			e.newID = f
		}
	}
}

// Engine is the combination engine. It is stateless and can be shared
// concurrently across multiple symbols.
type Engine struct {
	registry  *modules.Registry
	timeout   time.Duration
	publisher Publisher
	auditor   Auditor
	logger    *slog.Logger
	newID     func() string
}

// New constructs an engine.
func New(reg *modules.Registry, opts ...Option) *Engine {
	e := &Engine{
		registry: reg,
		timeout:  DefaultModuleTimeout,
		logger:   slog.Default(),
		newID:    newUUID,
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Evaluate concurrently calls each module in the strategy and aggregates
// their output into a decision.
//
// This step is pure computation with no I/O, so backtesting and unit tests
// can reuse the exact same aggregation logic — guaranteeing that "however
// the backtest computed it is exactly how live trading computes it too."
//
// feeds supplies market data per timeframe: the entry for cfg.Timeframe (the
// trigger timeframe) is required and determines this decision's
// Timestamp/Price; the other timeframes are available for modules to pull
// via ModuleConfig.Timeframe as needed (see evaluateOne). A single-timeframe
// strategy only needs one entry in the map.
func (e *Engine) Evaluate(ctx context.Context, cfg types.StrategyConfig, feeds map[types.Timeframe]types.MarketData) (types.Decision, error) {
	resolved, err := strategy.Validate(cfg, e.registry)
	if err != nil {
		return types.Decision{}, err
	}

	trigger, ok := feeds[cfg.Timeframe]
	if !ok {
		return types.Decision{}, fmt.Errorf("missing market data for trigger timeframe %s, cannot compute a decision", cfg.Timeframe)
	}
	for tf, md := range feeds {
		if md.Symbol != "" && cfg.Symbol != md.Symbol {
			// NOTE: kept in Chinese — engine_test.go asserts on the "隔离"
			// substring in this message (TestEvaluateRejectsSymbolMismatch).
			return types.Decision{}, fmt.Errorf(
				"周期 %s 的行情标的 %q 与策略标的 %q 不一致；不同标的的策略必须严格隔离", tf, md.Symbol, cfg.Symbol)
		}
	}

	signals := e.evaluateModules(ctx, cfg, feeds, resolved)

	dir, score, triggered, reason := aggregate(cfg, signals)

	price := decimalZero()
	if last, ok := trigger.Last(); ok {
		price = last.Close
	}

	return types.Decision{
		StrategyID:  cfg.ID,
		Symbol:      cfg.Symbol,
		Direction:   dir,
		Score:       score,
		Triggered:   triggered,
		Signals:     signals,
		Reason:      reason,
		Price:       price,
		Timestamp:   trigger.Time(),
		EvaluatedAt: time.Now().UTC(),
	}, nil
}

// evaluateModules runs all modules concurrently, returning a signal slice
// in the same order as cfg.Modules.
//
// Isolation is the core responsibility here: a single module timing out,
// erroring, or even panicking must only degrade that module to a neutral
// signal — it can never take down the whole engine or affect other
// modules' results. Likewise, missing the market data for a module's
// timeframe only degrades that one module, not the others.
func (e *Engine) evaluateModules(
	ctx context.Context, cfg types.StrategyConfig,
	feeds map[types.Timeframe]types.MarketData, resolved map[string]map[string]any,
) []types.Signal {
	signals := make([]types.Signal, len(cfg.Modules))
	var wg sync.WaitGroup
	triggerTime := feeds[cfg.Timeframe].Time()

	for i, mc := range cfg.Modules {
		tf := mc.Timeframe
		if tf == "" {
			tf = cfg.Timeframe
		}
		md, ok := feeds[tf]
		if !ok {
			signals[i] = types.DegradedSignal(mc.Module, cfg.Symbol,
				fmt.Errorf("missing market data for timeframe %s", tf), triggerTime)
			continue
		}
		wg.Add(1)
		go func(i int, mc types.ModuleConfig, md types.MarketData) {
			defer wg.Done()
			signals[i] = e.evaluateOne(ctx, mc, md, resolved[mc.Module])
		}(i, mc, md)
	}
	wg.Wait()
	return signals
}

func (e *Engine) evaluateOne(
	ctx context.Context, mc types.ModuleConfig,
	md types.MarketData, params map[string]any,
) (sig types.Signal) {
	m, err := e.registry.Get(mc.Module)
	if err != nil {
		return types.DegradedSignal(mc.Module, md.Symbol, err, md.Time())
	}

	// A module panicking must not take down the whole engine: recover it,
	// degrade the signal, and leave the panic value in the logs.
	defer func() {
		if r := recover(); r != nil {
			e.logger.Error("module panicked, degraded to a neutral signal",
				"module", mc.Module, "symbol", md.Symbol, "panic", r)
			sig = types.DegradedSignal(mc.Module, md.Symbol,
				fmt.Errorf("module panicked: %v", r), md.Time())
		}
	}()

	mctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	start := time.Now()
	sig, err = m.Evaluate(mctx, md, params)
	elapsed := time.Since(start)

	if err != nil {
		level := slog.LevelWarn
		if errors.Is(err, context.DeadlineExceeded) {
			level = slog.LevelError
		}
		e.logger.Log(ctx, level, "module produced no signal, degraded to neutral",
			"module", mc.Module, "symbol", md.Symbol, "elapsed", elapsed, "err", err)
		return types.DegradedSignal(mc.Module, md.Symbol, err, md.Time())
	}

	// A signal returned by a module must self-identify, or the audit
	// record ends up misattributed.
	if sig.Module == "" {
		sig.Module = mc.Module
	}
	if sig.Symbol == "" {
		sig.Symbol = md.Symbol
	}
	return sig
}

// Process runs the full pipeline: compute a decision → record it for audit
// → publish it to the message queue.
//
// Recording before publishing is deliberate: the audit trail is a
// compliance requirement, so a publish failure is preferable to ever
// ending up with "the execution layer received and placed an order, but
// the audit table has no record of the decision."
func (e *Engine) Process(ctx context.Context, cfg types.StrategyConfig, feeds map[types.Timeframe]types.MarketData) (types.Decision, error) {
	d, err := e.Evaluate(ctx, cfg, feeds)
	if err != nil {
		return types.Decision{}, err
	}
	d.ID = e.newID()

	if e.auditor != nil {
		if err := e.auditor.RecordDecision(ctx, d); err != nil {
			return d, fmt.Errorf("failed to record decision for audit, aborting publish: %w", err)
		}
	}
	if e.publisher != nil {
		if err := e.publisher.PublishDecision(ctx, d); err != nil {
			return d, fmt.Errorf("failed to publish decision (already recorded for audit, decision ID %s): %w", d.ID, err)
		}
	}
	return d, nil
}

// ModuleNames returns the module names referenced by the strategy, sorted lexically.
func ModuleNames(cfg types.StrategyConfig) []string {
	names := make([]string, 0, len(cfg.Modules))
	for _, mc := range cfg.Modules {
		names = append(names, mc.Module)
	}
	sort.Strings(names)
	return names
}
