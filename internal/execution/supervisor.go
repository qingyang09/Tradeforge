package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"tradeforge/pkg/types"
)

// Supervisor manages a set of execution instances, one per strategy (symbol).
//
// Its entire value is in the word "isolation": decisions are routed to their
// own Worker by strategy ID, and each Worker processes its own queue
// serially in its own goroutine. If one symbol blocks, errors, or panics,
// every other symbol keeps running normally.
type Supervisor struct {
	logger *slog.Logger

	mu      sync.RWMutex
	workers map[string]*workerHandle
}

type workerHandle struct {
	worker *Worker
	// inbox is this symbol's dedicated queue. One queue per symbol, so a slow
	// symbol backs up only its own queue and never holds up anyone else.
	inbox  chan types.Decision
	cancel context.CancelFunc
	done   chan struct{}
	// inflight counts decisions that have been delivered but not yet fully
	// processed, so Drain can wait precisely.
	inflight sync.WaitGroup
}

// NewSupervisor creates the execution-layer manager.
func NewSupervisor(logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{logger: logger, workers: make(map[string]*workerHandle)}
}

// ErrUnknownStrategy indicates a decision pointed at an unregistered strategy.
var ErrUnknownStrategy = errors.New("unregistered strategy")

// ErrAlreadyRegistered indicates this strategy has already been registered
// and is running — used so periodic rescans can distinguish "this strategy
// was already scanned before, no need to re-register" (normal, silently
// skip) from a genuine registration failure.
var ErrAlreadyRegistered = errors.New("strategy is already running")

// DefaultQueueSize is the queue capacity for a single symbol.
const DefaultQueueSize = 256

// Register registers a strategy and starts its execution instance.
func (s *Supervisor) Register(ctx context.Context, cfg types.StrategyConfig, broker Broker, opts ...WorkerOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, dup := s.workers[cfg.ID]; dup {
		return fmt.Errorf("%w: %s", ErrAlreadyRegistered, cfg.ID)
	}

	// Multiple strategies are allowed on the same symbol, but this must be
	// called out explicitly in the logs — they'll share the same exchange
	// position, and their risk controls are unaware of each other.
	for _, h := range s.workers {
		if h.worker.Symbol() == cfg.Symbol {
			s.logger.Warn("another strategy is already running on this symbol; risk control for each is independent and they do not share a position view",
				"symbol", cfg.Symbol, "existing", h.worker.StrategyID(), "new", cfg.ID)
		}
	}

	opts = append(opts, WithWorkerLogger(s.logger.With("symbol", cfg.Symbol, "strategy_id", cfg.ID)))
	w, err := NewWorker(cfg, broker, opts...)
	if err != nil {
		return err
	}

	wctx, cancel := context.WithCancel(ctx)
	h := &workerHandle{
		worker: w,
		inbox:  make(chan types.Decision, DefaultQueueSize),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	s.workers[cfg.ID] = h

	go s.runWorker(wctx, h)
	return nil
}

// runWorker is a single symbol's event loop.
func (s *Supervisor) runWorker(ctx context.Context, h *workerHandle) {
	defer close(h.done)

	// A panic in the event loop itself must also be caught: Worker.Handle
	// already recovers one layer internally, and this is the last line of
	// defense to make sure one symbol crashing doesn't take down the whole process.
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("execution instance's event loop panicked; this symbol has stopped (other symbols unaffected)",
				"symbol", h.worker.Symbol(), "panic", r)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-h.inbox:
			if !ok {
				return
			}
			if err := h.worker.Handle(ctx, d); err != nil {
				// Already counted in stats and logged inside Worker; this is just a rollup notice.
				s.logger.Warn("error processing a decision, isolated to this symbol",
					"symbol", h.worker.Symbol(), "err", err)
			}
			h.inflight.Done()
		}
	}
}

// Dispatch delivers a decision to its execution instance.
//
// Delivery is non-blocking: when the queue is full it drops the decision and
// errors, rather than blocking the caller. In a trading system, waiting on an
// already-backed-up queue is pointless — by the time it's your turn the
// market has moved on — and blocking would drag down delivery for other symbols too.
func (s *Supervisor) Dispatch(d types.Decision) error {
	s.mu.RLock()
	h, ok := s.workers[d.StrategyID]
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownStrategy, d.StrategyID)
	}

	h.inflight.Add(1)
	select {
	case h.inbox <- d:
		return nil
	default:
		h.inflight.Done()
		s.logger.Error("execution queue full, dropping decision",
			"symbol", h.worker.Symbol(), "strategy_id", d.StrategyID)
		return fmt.Errorf("execution queue for symbol %s is full; this decision was dropped", h.worker.Symbol())
	}
}

// Worker looks up an execution instance by strategy ID.
func (s *Supervisor) Worker(strategyID string) (*Worker, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.workers[strategyID]
	if !ok {
		return nil, false
	}
	return h.worker, true
}

// StrategyIDs returns every running strategy ID, in lexicographic order.
func (s *Supervisor) StrategyIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.workers))
	for id := range s.workers {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// StrategyStats bundles a run's stats together with the strategy/symbol they
// belong to — using symbol alone as the key isn't safe in a multi-user
// setting: many different users could easily all be running the same symbol
// (e.g. everyone trading BTCUSDT), and aggregating by symbol would let
// unrelated users' stats overwrite each other. Strategy ID is globally
// unique, so aggregating by it avoids that collision.
type StrategyStats struct {
	StrategyID string
	Symbol     string
	Stats      Stats
}

// StatsByStrategy returns a snapshot of each strategy's run stats, aggregated
// by strategy ID (globally unique).
func (s *Supervisor) StatsByStrategy() map[string]StrategyStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]StrategyStats, len(s.workers))
	for id, h := range s.workers {
		out[id] = StrategyStats{StrategyID: id, Symbol: h.worker.Symbol(), Stats: h.worker.Stats()}
	}
	return out
}

// Unregister stops and removes a strategy's execution instance.
func (s *Supervisor) Unregister(strategyID string) error {
	s.mu.Lock()
	h, ok := s.workers[strategyID]
	if ok {
		delete(s.workers, strategyID)
	}
	s.mu.Unlock()

	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownStrategy, strategyID)
	}
	h.cancel()
	<-h.done
	return nil
}

// Shutdown stops every execution instance and waits for them to exit.
func (s *Supervisor) Shutdown() {
	s.mu.Lock()
	handles := make([]*workerHandle, 0, len(s.workers))
	for _, h := range s.workers {
		handles = append(handles, h)
	}
	s.workers = make(map[string]*workerHandle)
	s.mu.Unlock()

	for _, h := range handles {
		h.cancel()
	}
	for _, h := range handles {
		<-h.done
	}
}

// Drain waits until every delivered decision has finished processing.
//
// Test-only: production code should never depend on the "queue is empty" state.
func (s *Supervisor) Drain() {
	s.mu.RLock()
	handles := make([]*workerHandle, 0, len(s.workers))
	for _, h := range s.workers {
		handles = append(handles, h)
	}
	s.mu.RUnlock()

	for _, h := range handles {
		h.inflight.Wait()
	}
}
