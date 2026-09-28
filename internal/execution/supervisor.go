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

// Supervisor 管理一组执行实例，每个策略（标的）一个。
//
// 它的全部价值就在"隔离"二字：决策按策略 ID 路由到各自的 Worker，
// 每个 Worker 在自己的 goroutine 里串行处理自己的队列。
// 一个标的阻塞、报错或 panic，其它标的照常运转。
type Supervisor struct {
	logger *slog.Logger

	mu      sync.RWMutex
	workers map[string]*workerHandle
}

type workerHandle struct {
	worker *Worker
	// inbox 是该标的的专属队列。每个标的一条队列，
	// 慢的标的堆积在自己的队列里，不会拖住别人。
	inbox  chan types.Decision
	cancel context.CancelFunc
	done   chan struct{}
	// inflight 统计"已投递但尚未处理完"的决策数，供 Drain 精确等待。
	inflight sync.WaitGroup
}

// NewSupervisor 创建执行层管理器。
func NewSupervisor(logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Supervisor{logger: logger, workers: make(map[string]*workerHandle)}
}

// ErrUnknownStrategy 表示决策指向了未注册的策略。
var ErrUnknownStrategy = errors.New("未注册的策略")

// ErrAlreadyRegistered 表示这个策略已经注册过、正在运行——供周期性重新扫描时区分
// "这个策略之前扫描过、这次不用重新注册"（正常，静默跳过即可）和真正的注册失败。
var ErrAlreadyRegistered = errors.New("策略已经在运行中")

// DefaultQueueSize 是单个标的的队列容量。
const DefaultQueueSize = 256

// Register 注册一个策略并启动它的执行实例。
func (s *Supervisor) Register(ctx context.Context, cfg types.StrategyConfig, broker Broker, opts ...WorkerOption) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, dup := s.workers[cfg.ID]; dup {
		return fmt.Errorf("%w：%s", ErrAlreadyRegistered, cfg.ID)
	}

	// 同一标的允许有多个策略，但要在日志里显式提示——
	// 它们会共享同一个交易所仓位，风控互不感知。
	for _, h := range s.workers {
		if h.worker.Symbol() == cfg.Symbol {
			s.logger.Warn("同一标的上已有其它策略在运行，两者的风控互相独立、不共享仓位视图",
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

// runWorker 是单个标的的事件循环。
func (s *Supervisor) runWorker(ctx context.Context, h *workerHandle) {
	defer close(h.done)

	// 事件循环自身的 panic 也要兜住：Worker.Handle 内部已经 recover 过一层，
	// 这里是最后的保险，确保一个标的的崩溃不会终止整个进程。
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("执行实例的事件循环 panic，该标的已停止（其它标的不受影响）",
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
				// 已经在 Worker 内计入统计并记日志，这里只做汇总提示。
				s.logger.Warn("处理决策时出错，已隔离在本标的内",
					"symbol", h.worker.Symbol(), "err", err)
			}
			h.inflight.Done()
		}
	}
}

// Dispatch 把一条决策投递给对应的执行实例。
//
// 投递是非阻塞的：队列满时丢弃并报错，而不是阻塞调用方。
// 交易系统里，等待一个已经堆积的队列毫无意义——等轮到它时行情早变了，
// 而阻塞会连累其它标的的投递。
func (s *Supervisor) Dispatch(d types.Decision) error {
	s.mu.RLock()
	h, ok := s.workers[d.StrategyID]
	s.mu.RUnlock()

	if !ok {
		return fmt.Errorf("%w：%s", ErrUnknownStrategy, d.StrategyID)
	}

	h.inflight.Add(1)
	select {
	case h.inbox <- d:
		return nil
	default:
		h.inflight.Done()
		s.logger.Error("执行队列已满，丢弃决策",
			"symbol", h.worker.Symbol(), "strategy_id", d.StrategyID)
		return fmt.Errorf("标的 %s 的执行队列已满，本条决策被丢弃", h.worker.Symbol())
	}
}

// Worker 按策略 ID 取执行实例。
func (s *Supervisor) Worker(strategyID string) (*Worker, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.workers[strategyID]
	if !ok {
		return nil, false
	}
	return h.worker, true
}

// StrategyIDs 返回全部在运行的策略 ID，按字典序排列。
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

// StrategyStats 把运行统计跟它所属的策略/标的捆在一起返回——单用 symbol 当 key 在
// 多用户场景下并不安全：很多不同用户完全可能都在跑同一个 symbol（比如都在跑
// BTCUSDT），按 symbol 聚合会让不相关用户的统计互相覆盖。策略 ID 全局唯一，按它聚合
// 才不会有这个collision。
type StrategyStats struct {
	StrategyID string
	Symbol     string
	Stats      Stats
}

// StatsByStrategy 返回各策略的运行统计快照，按策略 ID（全局唯一）聚合。
func (s *Supervisor) StatsByStrategy() map[string]StrategyStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]StrategyStats, len(s.workers))
	for id, h := range s.workers {
		out[id] = StrategyStats{StrategyID: id, Symbol: h.worker.Symbol(), Stats: h.worker.Stats()}
	}
	return out
}

// Unregister 停止并移除一个策略的执行实例。
func (s *Supervisor) Unregister(strategyID string) error {
	s.mu.Lock()
	h, ok := s.workers[strategyID]
	if ok {
		delete(s.workers, strategyID)
	}
	s.mu.Unlock()

	if !ok {
		return fmt.Errorf("%w：%s", ErrUnknownStrategy, strategyID)
	}
	h.cancel()
	<-h.done
	return nil
}

// Shutdown 停止全部执行实例并等待它们退出。
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

// Drain 等待所有已投递的决策处理完毕。
//
// 仅供测试使用：生产代码不应依赖"队列已空"这个状态。
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
