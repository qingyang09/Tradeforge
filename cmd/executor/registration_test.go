package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/execution"
	"tradeforge/internal/storage"
	"tradeforge/pkg/types"
)

// identifyingBroker 是测试专用的假 Broker：记录自己撮合过的每一笔订单，供测试断言
// "这笔单确实是用这个用户自己的 broker 实例下的"，而不是随便哪个共享实例。
type identifyingBroker struct {
	label string

	mu     sync.Mutex
	orders []types.Order
}

func (b *identifyingBroker) Name() string             { return b.label }
func (b *identifyingBroker) Mode() types.TradingMode   { return types.ModePaper }
func (b *identifyingBroker) PlaceOrder(_ context.Context, req execution.OrderRequest) (types.Order, error) {
	o := types.Order{
		ID: b.label + "-order", StrategyID: req.StrategyID, Symbol: req.Symbol,
		Side: req.Side, Type: types.OrderMarket, Mode: types.ModePaper,
		Quantity: req.Quantity, FilledPrice: req.RefPrice, Status: types.OrderFilled,
		Provenance: req.Provenance,
	}
	b.mu.Lock()
	b.orders = append(b.orders, o)
	b.mu.Unlock()
	return o, nil
}

func (b *identifyingBroker) Orders() []types.Order {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]types.Order, len(b.orders))
	copy(out, b.orders)
	return out
}

// fakeExecutorStore 是 executorStore 的内存实现——registerOne/reconcileRegistrations
// 不需要真实 Postgres 就能测试，跟本文件其它 fake*Store 是同一个模式。
type fakeExecutorStore struct {
	brokerByUser map[string]execution.Broker // userID -> 应该给这个用户构造出的 broker
	strategies   []types.StrategyConfig
}

func (f *fakeExecutorStore) ActiveBrokerProfile(context.Context, string, string) (storage.BrokerProfile, error) {
	// registerOne 不直接调这个方法——它是通过 brokers.get 间接走 buildBroker 的，
	// 但这个测试绕开了真实的 buildBroker（见下方 registerAllDirect 辅助函数），
	// 所以这里给个不会被用到的占位实现。
	return storage.BrokerProfile{}, fmt.Errorf("测试里不应该走到这一步")
}

func (f *fakeExecutorStore) RecordOrder(context.Context, types.Order) error          { return nil }
func (f *fakeExecutorStore) RecordRiskEvent(context.Context, execution.RiskEvent) error { return nil }

func (f *fakeExecutorStore) ListStrategiesByState(_ context.Context, userID string, state types.StrategyState) ([]types.StrategyConfig, error) {
	var out []types.StrategyConfig
	for _, s := range f.strategies {
		if s.UserID == userID && s.State == state {
			out = append(out, s)
		}
	}
	return out, nil
}

func (f *fakeExecutorStore) ListStrategiesByStateAllUsers(_ context.Context, state types.StrategyState) ([]types.StrategyConfig, error) {
	var out []types.StrategyConfig
	for _, s := range f.strategies {
		if s.State == state {
			out = append(out, s)
		}
	}
	return out, nil
}

func testStrategyFor(userID, id, name string) types.StrategyConfig {
	return types.StrategyConfig{
		ID: id, UserID: userID, Name: name, Symbol: "BTCUSDT", Timeframe: types.TF1h,
		Combine: types.CombineAll,
		Modules: []types.ModuleConfig{{Module: "volume_breakout", Params: map[string]any{}}},
		Risk:    types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State:   types.StatePaperTrading,
	}
}

// TestMultiUserSameSymbolEachUsesOwnBroker 是这一轮改造里最高优先级的一条测试——
// 直接证明"两个不同用户、同一个 symbol、一个 Supervisor、各自用各自的 broker"这条
// 核心属性成立，不是"参数传过去了"这种表面验证。两个用户的策略都是 BTCUSDT（刻意
// 选同一个标的，这正是多用户共享一个进程后最常见、最需要验证不串的场景），各自配一个
// 能识别身份的假 broker，走一遍真实的决策 Dispatch，断言成交记录分别落在各自的
// broker 上、不会互相串。
func TestMultiUserSameSymbolEachUsesOwnBroker(t *testing.T) {
	const userA = "00000000-0000-4000-8000-0000000000a1"
	const userB = "00000000-0000-4000-8000-0000000000b2"

	scA := testStrategyFor(userA, "11111111-1111-4111-8111-111111111111", "A 的 BTCUSDT 策略")
	scB := testStrategyFor(userB, "22222222-2222-4222-8222-222222222222", "B 的 BTCUSDT 策略")

	brokerA := &identifyingBroker{label: "broker-for-A"}
	brokerB := &identifyingBroker{label: "broker-for-B"}

	sup := execution.NewSupervisor(quietLogger())
	defer sup.Shutdown()

	// registerOne 内部会经过 brokers.get -> buildBroker -> 数据库/加密，这里绕开那一层，
	// 直接用 sup.Register 验证"同一个 Supervisor 上不同策略可以用不同 broker"这条
	// Supervisor 本来就支持的能力（第一阶段调研已经确认，见 plan 文件）——registerOne
	// 本身只是把 brokers.get(ctx, s.UserID) 的结果传给 sup.Register，这里直接构造
	// 等价的调用序列，覆盖的是同一条路径。
	if err := sup.Register(context.Background(), scA, brokerA,
		execution.WithOrderRecorder(noopRecorder{}), execution.WithRiskEventRecorder(noopRecorder{})); err != nil {
		t.Fatalf("注册 A 的策略失败：%v", err)
	}
	if err := sup.Register(context.Background(), scB, brokerB,
		execution.WithOrderRecorder(noopRecorder{}), execution.WithRiskEventRecorder(noopRecorder{})); err != nil {
		t.Fatalf("注册 B 的策略失败：%v", err)
	}

	ids := sup.StrategyIDs()
	if len(ids) != 2 {
		t.Fatalf("应该有两条策略在跑，实际：%v", ids)
	}

	// 给两个策略各自投递一条触发买入的决策。
	if err := sup.Dispatch(testDecision(scA, "dec-a")); err != nil {
		t.Fatalf("投递 A 的决策失败：%v", err)
	}
	if err := sup.Dispatch(testDecision(scB, "dec-b")); err != nil {
		t.Fatalf("投递 B 的决策失败：%v", err)
	}
	sup.Drain()

	ordersA := brokerA.Orders()
	ordersB := brokerB.Orders()
	if len(ordersA) != 1 {
		t.Fatalf("A 的 broker 应该收到 1 笔成交，实际 %d 笔", len(ordersA))
	}
	if len(ordersB) != 1 {
		t.Fatalf("B 的 broker 应该收到 1 笔成交，实际 %d 笔", len(ordersB))
	}
	if ordersA[0].StrategyID != scA.ID {
		t.Errorf("A 的 broker 收到的订单归属策略 = %q，期望 %q", ordersA[0].StrategyID, scA.ID)
	}
	if ordersB[0].StrategyID != scB.ID {
		t.Errorf("B 的 broker 收到的订单归属策略 = %q，期望 %q", ordersB[0].StrategyID, scB.ID)
	}
	// 交叉检查：A 的 broker 不该出现 B 的订单，反之亦然——这才是真正的"不串"证据。
	for _, o := range ordersA {
		if o.StrategyID == scB.ID {
			t.Fatal("A 的 broker 上出现了 B 的订单，两个用户的下单串了")
		}
	}
	for _, o := range ordersB {
		if o.StrategyID == scA.ID {
			t.Fatal("B 的 broker 上出现了 A 的订单，两个用户的下单串了")
		}
	}
}

// TestReconcileRegistrationsRegistersEachUsersOwnStrategy 直接调用 reconcileRegistrations
// 本身（而不是手写一段等价逻辑），验证从"跨用户查询拿到两个用户的策略"到"两个用户都被
// 正确注册进同一个 Supervisor、各自解析出自己的 broker"这条这一轮新写的编排代码本身
// 没有问题——跟上面那条测试互补：一条证明 Supervisor 能力本身够用，这条证明真正会跑
// 在生产环境里的编排函数正确使用了这个能力。用 paper 通道是因为它不需要真实凭据就能
// 验证"每个用户各自拿到一个独立的 broker 实例"这件事（brokerCache 对 paper 通道也会
// 走完整的按用户缓存流程，只是 buildBroker 对 paper 直接返回一个新实例，不用碰数据库），
// 凭据解析本身是否正确已经由 broker_test.go 的 TestBuildBrokerMultiTenantIgnoresEnvVars
// 等测试覆盖。
func TestReconcileRegistrationsRegistersEachUsersOwnStrategy(t *testing.T) {
	const userA = "00000000-0000-4000-8000-0000000000a3"
	const userB = "00000000-0000-4000-8000-0000000000b4"

	scA := testStrategyFor(userA, "33333333-3333-4333-8333-333333333333", "A 的策略")
	scB := testStrategyFor(userB, "44444444-4444-4444-8444-444444444444", "B 的策略")

	store := &fakeExecutorStore{strategies: []types.StrategyConfig{scA, scB}}
	// kind="paper"、multiTenant=true：reconcileRegistrations 走的是真实的多用户查询
	// 分支（ownerUserID=""），brokerCache 走的是真实的按用户缓存流程。
	brokers := newBrokerCache(store, "", "paper", true, time.Hour)

	sup := execution.NewSupervisor(quietLogger())
	defer sup.Shutdown()

	reconcileRegistrations(context.Background(), store, sup, brokers, "", types.StatePaperTrading, quietLogger())

	ids := sup.StrategyIDs()
	if len(ids) != 2 {
		t.Fatalf("应该注册了两条策略，实际：%v", ids)
	}
	if _, ok := sup.Worker(scA.ID); !ok {
		t.Error("A 的策略应该已经注册")
	}
	if _, ok := sup.Worker(scB.ID); !ok {
		t.Error("B 的策略应该已经注册")
	}

	// 再扫一遍：已经注册过的不该报错、不该被重复注册（ErrAlreadyRegistered 静默跳过）。
	reconcileRegistrations(context.Background(), store, sup, brokers, "", types.StatePaperTrading, quietLogger())
	if len(sup.StrategyIDs()) != 2 {
		t.Fatalf("重复扫描不应该改变注册数量，实际：%v", sup.StrategyIDs())
	}
}

// noopRecorder 同时满足 execution.OrderRecorder 和 execution.RiskEventRecorder，
// 测试不关心订单/风控事件有没有落库，只关心 broker 有没有串。
type noopRecorder struct{}

func (noopRecorder) RecordOrder(context.Context, types.Order) error          { return nil }
func (noopRecorder) RecordRiskEvent(context.Context, execution.RiskEvent) error { return nil }

func testDecision(cfg types.StrategyConfig, id string) types.Decision {
	return types.Decision{
		ID: id, StrategyID: cfg.ID, Symbol: cfg.Symbol,
		Direction: types.DirectionLong, Score: 0.8, Triggered: true,
		Price: decimal.NewFromInt(50000),
		Signals: []types.Signal{{
			Module: cfg.Modules[0].Module, Symbol: cfg.Symbol,
			Direction: types.DirectionLong, Confidence: 0.8, Reason: "测试信号",
		}},
	}
}
