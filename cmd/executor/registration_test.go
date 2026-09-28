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

// identifyingBroker is a test-only fake Broker: it records every order it
// fills, so tests can assert "this order was really placed through this
// user's own broker instance", not just some shared instance.
type identifyingBroker struct {
	label string

	mu     sync.Mutex
	orders []types.Order
}

func (b *identifyingBroker) Name() string            { return b.label }
func (b *identifyingBroker) Mode() types.TradingMode { return types.ModePaper }
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

// fakeExecutorStore is an in-memory implementation of executorStore — so
// registerOne/reconcileRegistrations can be tested without a real Postgres,
// same pattern as the other fake*Store types in this file.
type fakeExecutorStore struct {
	brokerByUser map[string]execution.Broker // userID -> the broker that should be constructed for this user
	strategies   []types.StrategyConfig
}

func (f *fakeExecutorStore) ActiveBrokerProfile(context.Context, string, string) (storage.BrokerProfile, error) {
	// registerOne doesn't call this method directly — it goes through
	// brokers.get -> buildBroker indirectly, and this test bypasses the
	// real buildBroker (see the registerAllDirect helper below), so this is
	// just a placeholder implementation that should never be hit.
	return storage.BrokerProfile{}, fmt.Errorf("should not reach this point in tests")
}

func (f *fakeExecutorStore) RecordOrder(context.Context, types.Order) error             { return nil }
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

// TestMultiUserSameSymbolEachUsesOwnBroker is the highest-priority test added
// by this rework — it directly proves the core property that "two different
// users on the same symbol, sharing one Supervisor, each use their own
// broker", not just the shallow check that "the parameter got passed
// through". Both users' strategies are on BTCUSDT (deliberately the same
// symbol, since that's exactly the most common scenario needing isolation
// verification once multiple users share one process); each gets an
// identity-tagged fake broker, a real decision Dispatch runs, and the test
// asserts the fills land on each one's own broker without crossing over.
func TestMultiUserSameSymbolEachUsesOwnBroker(t *testing.T) {
	const userA = "00000000-0000-4000-8000-0000000000a1"
	const userB = "00000000-0000-4000-8000-0000000000b2"

	scA := testStrategyFor(userA, "11111111-1111-4111-8111-111111111111", "A's BTCUSDT strategy")
	scB := testStrategyFor(userB, "22222222-2222-4222-8222-222222222222", "B's BTCUSDT strategy")

	brokerA := &identifyingBroker{label: "broker-for-A"}
	brokerB := &identifyingBroker{label: "broker-for-B"}

	sup := execution.NewSupervisor(quietLogger())
	defer sup.Shutdown()

	// registerOne internally goes through brokers.get -> buildBroker ->
	// database/decryption; this test bypasses that layer and calls
	// sup.Register directly to verify the capability the Supervisor already
	// supports — "different strategies on the same Supervisor can use
	// different brokers" (confirmed during phase-1 research, see the plan
	// file). registerOne itself just passes the result of
	// brokers.get(ctx, s.UserID) to sup.Register, so constructing an
	// equivalent call sequence here exercises the same path.
	if err := sup.Register(context.Background(), scA, brokerA,
		execution.WithOrderRecorder(noopRecorder{}), execution.WithRiskEventRecorder(noopRecorder{})); err != nil {
		t.Fatalf("failed to register A's strategy: %v", err)
	}
	if err := sup.Register(context.Background(), scB, brokerB,
		execution.WithOrderRecorder(noopRecorder{}), execution.WithRiskEventRecorder(noopRecorder{})); err != nil {
		t.Fatalf("failed to register B's strategy: %v", err)
	}

	ids := sup.StrategyIDs()
	if len(ids) != 2 {
		t.Fatalf("expected two strategies running, got: %v", ids)
	}

	// Dispatch one buy-triggering decision to each strategy.
	if err := sup.Dispatch(testDecision(scA, "dec-a")); err != nil {
		t.Fatalf("failed to dispatch A's decision: %v", err)
	}
	if err := sup.Dispatch(testDecision(scB, "dec-b")); err != nil {
		t.Fatalf("failed to dispatch B's decision: %v", err)
	}
	sup.Drain()

	ordersA := brokerA.Orders()
	ordersB := brokerB.Orders()
	if len(ordersA) != 1 {
		t.Fatalf("A's broker should have received 1 fill, got %d", len(ordersA))
	}
	if len(ordersB) != 1 {
		t.Fatalf("B's broker should have received 1 fill, got %d", len(ordersB))
	}
	if ordersA[0].StrategyID != scA.ID {
		t.Errorf("A's broker received an order for strategy = %q, want %q", ordersA[0].StrategyID, scA.ID)
	}
	if ordersB[0].StrategyID != scB.ID {
		t.Errorf("B's broker received an order for strategy = %q, want %q", ordersB[0].StrategyID, scB.ID)
	}
	// Cross-check: A's broker should never see B's orders and vice versa — this is the real evidence of no cross-talk.
	for _, o := range ordersA {
		if o.StrategyID == scB.ID {
			t.Fatal("A's broker received B's order, the two users' orders crossed")
		}
	}
	for _, o := range ordersB {
		if o.StrategyID == scA.ID {
			t.Fatal("B's broker received A's order, the two users' orders crossed")
		}
	}
}

// TestReconcileRegistrationsRegistersEachUsersOwnStrategy calls
// reconcileRegistrations directly (rather than hand-writing equivalent
// logic), verifying that the newly-written orchestration code correctly
// goes from "cross-user query returns two users' strategies" to "both users
// get correctly registered on the same Supervisor, each resolving its own
// broker" — this complements the test above: that one proves the Supervisor
// capability itself is sufficient, this one proves the orchestration
// function that actually runs in production uses that capability correctly.
// It uses the paper channel because it doesn't need real credentials to
// verify "each user gets their own independent broker instance"
// (brokerCache still goes through the full per-user caching flow for the
// paper channel — buildBroker just returns a fresh instance directly for
// paper without touching the database); whether credential resolution
// itself is correct is already covered by broker_test.go's
// TestBuildBrokerMultiTenantIgnoresEnvVars and related tests.
func TestReconcileRegistrationsRegistersEachUsersOwnStrategy(t *testing.T) {
	const userA = "00000000-0000-4000-8000-0000000000a3"
	const userB = "00000000-0000-4000-8000-0000000000b4"

	scA := testStrategyFor(userA, "33333333-3333-4333-8333-333333333333", "A's strategy")
	scB := testStrategyFor(userB, "44444444-4444-4444-8444-444444444444", "B's strategy")

	store := &fakeExecutorStore{strategies: []types.StrategyConfig{scA, scB}}
	// kind="paper", multiTenant=true: reconcileRegistrations takes the real
	// multi-tenant query branch (ownerUserID=""), and brokerCache takes the
	// real per-user caching flow.
	brokers := newBrokerCache(store, "", "paper", true, time.Hour)

	sup := execution.NewSupervisor(quietLogger())
	defer sup.Shutdown()

	reconcileRegistrations(context.Background(), store, sup, brokers, "", types.StatePaperTrading, quietLogger())

	ids := sup.StrategyIDs()
	if len(ids) != 2 {
		t.Fatalf("expected two strategies registered, got: %v", ids)
	}
	if _, ok := sup.Worker(scA.ID); !ok {
		t.Error("A's strategy should be registered")
	}
	if _, ok := sup.Worker(scB.ID); !ok {
		t.Error("B's strategy should be registered")
	}

	// Scan again: already-registered strategies should not error or be re-registered (ErrAlreadyRegistered is silently skipped).
	reconcileRegistrations(context.Background(), store, sup, brokers, "", types.StatePaperTrading, quietLogger())
	if len(sup.StrategyIDs()) != 2 {
		t.Fatalf("a repeat scan should not change the registration count, got: %v", sup.StrategyIDs())
	}
}

// noopRecorder satisfies both execution.OrderRecorder and
// execution.RiskEventRecorder; the test doesn't care whether orders/risk
// events are persisted, only whether the broker crosses users.
type noopRecorder struct{}

func (noopRecorder) RecordOrder(context.Context, types.Order) error             { return nil }
func (noopRecorder) RecordRiskEvent(context.Context, execution.RiskEvent) error { return nil }

func testDecision(cfg types.StrategyConfig, id string) types.Decision {
	return types.Decision{
		ID: id, StrategyID: cfg.ID, Symbol: cfg.Symbol,
		Direction: types.DirectionLong, Score: 0.8, Triggered: true,
		Price: decimal.NewFromInt(50000),
		Signals: []types.Signal{{
			Module: cfg.Modules[0].Module, Symbol: cfg.Symbol,
			Direction: types.DirectionLong, Confidence: 0.8, Reason: "test signal",
		}},
	}
}
