// Package engine 实现模块组合引擎：并发调用信号模块、聚合成决策、
// 发布到消息队列并落库审计。
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

// DefaultModuleTimeout 是单个模块 Evaluate 的默认超时。
const DefaultModuleTimeout = 3 * time.Second

// Publisher 把决策发布到消息队列，供执行层与回测引擎消费。
type Publisher interface {
	PublishDecision(ctx context.Context, d types.Decision) error
}

// Auditor 把决策落库，形成审计留痕。
type Auditor interface {
	RecordDecision(ctx context.Context, d types.Decision) error
}

// Option 配置引擎。
type Option func(*Engine)

// WithTimeout 设置单模块超时。
func WithTimeout(d time.Duration) Option {
	return func(e *Engine) {
		if d > 0 {
			e.timeout = d
		}
	}
}

// WithPublisher 设置决策发布通道。不设置则不发布。
func WithPublisher(p Publisher) Option { return func(e *Engine) { e.publisher = p } }

// WithAuditor 设置审计落库通道。不设置则不落库。
func WithAuditor(a Auditor) Option { return func(e *Engine) { e.auditor = a } }

// WithLogger 设置日志器。
func WithLogger(l *slog.Logger) Option {
	return func(e *Engine) {
		if l != nil {
			e.logger = l
		}
	}
}

// WithIDFunc 覆盖决策 ID 的生成方式，便于测试产出确定性的 ID。
func WithIDFunc(f func() string) Option {
	return func(e *Engine) {
		if f != nil {
			e.newID = f
		}
	}
}

// Engine 是组合引擎。它本身无状态，可被多个标的并发共用。
type Engine struct {
	registry  *modules.Registry
	timeout   time.Duration
	publisher Publisher
	auditor   Auditor
	logger    *slog.Logger
	newID     func() string
}

// New 构造引擎。
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

// Evaluate 并发调用策略中的各模块，聚合出决策。
//
// 这一步是纯计算，不做任何 I/O：便于回测与单元测试直接复用同一套聚合逻辑，
// 保证"回测里怎么算的，实盘就怎么算"。
//
// feeds 按周期提供行情：cfg.Timeframe（触发周期）对应的那份是必需的，它决定这次决策
// 的 Timestamp/Price；其余周期供各模块通过 ModuleConfig.Timeframe 按需取用（见
// evaluateOne）。单周期策略只需要 map 里有一个元素。
func (e *Engine) Evaluate(ctx context.Context, cfg types.StrategyConfig, feeds map[types.Timeframe]types.MarketData) (types.Decision, error) {
	resolved, err := strategy.Validate(cfg, e.registry)
	if err != nil {
		return types.Decision{}, err
	}

	trigger, ok := feeds[cfg.Timeframe]
	if !ok {
		return types.Decision{}, fmt.Errorf("缺少触发周期 %s 的行情，无法计算决策", cfg.Timeframe)
	}
	for tf, md := range feeds {
		if md.Symbol != "" && cfg.Symbol != md.Symbol {
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

// evaluateModules 并发跑完所有模块，返回与 cfg.Modules 顺序一致的信号切片。
//
// 隔离是这里的核心职责：单个模块超时、报错甚至 panic，都只让该模块降级为中性信号，
// 绝不能拖垮整个引擎或影响其它模块的结果；缺少某个模块所需周期的行情同样只降级
// 那一个模块，不影响其它模块正常出信号。
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
				fmt.Errorf("缺少 %s 周期的行情", tf), triggerTime)
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

	// 模块 panic 不能掀翻整个引擎：捕获后降级，并把堆栈信息留在日志里。
	defer func() {
		if r := recover(); r != nil {
			e.logger.Error("模块 panic，已降级为中性信号",
				"module", mc.Module, "symbol", md.Symbol, "panic", r)
			sig = types.DegradedSignal(mc.Module, md.Symbol,
				fmt.Errorf("模块内部 panic：%v", r), md.Time())
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
		e.logger.Log(ctx, level, "模块未产出信号，已降级为中性",
			"module", mc.Module, "symbol", md.Symbol, "elapsed", elapsed, "err", err)
		return types.DegradedSignal(mc.Module, md.Symbol, err, md.Time())
	}

	// 模块返回的信号必须自报家门，否则审计记录会张冠李戴。
	if sig.Module == "" {
		sig.Module = mc.Module
	}
	if sig.Symbol == "" {
		sig.Symbol = md.Symbol
	}
	return sig
}

// Process 执行一次完整流程：计算决策 → 落库审计 → 发布到消息队列。
//
// 落库先于发布是刻意的：审计留痕是合规要求，宁可发布失败也不能出现
// "执行层已经收到并下单，审计表里却查无此决策"。
func (e *Engine) Process(ctx context.Context, cfg types.StrategyConfig, feeds map[types.Timeframe]types.MarketData) (types.Decision, error) {
	d, err := e.Evaluate(ctx, cfg, feeds)
	if err != nil {
		return types.Decision{}, err
	}
	d.ID = e.newID()

	if e.auditor != nil {
		if err := e.auditor.RecordDecision(ctx, d); err != nil {
			return d, fmt.Errorf("决策审计落库失败，已中止发布：%w", err)
		}
	}
	if e.publisher != nil {
		if err := e.publisher.PublishDecision(ctx, d); err != nil {
			return d, fmt.Errorf("决策发布失败（审计已落库，决策 ID %s）：%w", d.ID, err)
		}
	}
	return d, nil
}

// ModuleNames 返回策略中引用的模块名，按字典序排列。
func ModuleNames(cfg types.StrategyConfig) []string {
	names := make([]string, 0, len(cfg.Modules))
	for _, mc := range cfg.Modules {
		names = append(names, mc.Module)
	}
	sort.Strings(names)
	return names
}
