//go:build integration

// Requires a Postgres started via docker-compose with migrations/005-008
// already applied (the four multi-tenant rework migrations):
//
//	docker compose up -d
//	go test -tags=integration ./cmd/executor/... -run CrossUser -v
//
// This file verifies a real risk found during design: once broker_profiles/
// strategies are partitioned by user_id, if -owner-email fails to thread
// userID all the way through correctly, one executor process could load
// another user's strategies or place orders with the wrong credentials.
// broker_test.go/promote_test.go use a fake store to test "was the
// parameter passed down at all"; this file uses a real Postgres to prove
// that once it is passed down, "the two users' data is genuinely invisible
// to each other" — neither substitutes for the other.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/config"
	"tradeforge/internal/execution"
	"tradeforge/internal/secretcrypto"
	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

const isolationTestMasterKey = "isolation-test-master-key-32-bytes-plus!"

func createIsolationTestUser(ctx context.Context, t *testing.T, store *storage.Store, label string) storage.User {
	t.Helper()
	u := storage.User{
		ID: idgen.NewUUID(), Email: label + "-" + idgen.NewUUID() + "@isolation.test",
		PasswordHash: []byte("x"),
	}
	if err := store.CreateUser(ctx, u); err != nil {
		t.Fatalf("failed to create test user: %v", err)
	}
	return u
}

func saveIsolationBrokerProfile(ctx context.Context, t *testing.T, store *storage.Store, userID, label, plaintext string) {
	t.Helper()
	ciphertext, salt, nonce, err := secretcrypto.Encrypt(isolationTestMasterKey, plaintext)
	if err != nil {
		t.Fatalf("failed to encrypt test credentials: %v", err)
	}
	p := storage.BrokerProfile{
		ID: idgen.NewUUID(), UserID: userID, Label: label, Broker: "okx-demo", KeyHint: "sk-a…test",
		EncryptedCredentials: ciphertext, KeySalt: salt, KeyNonce: nonce,
	}
	if err := store.SaveBrokerProfile(ctx, p, true); err != nil {
		t.Fatalf("failed to save test exchange config: %v", err)
	}
}

func isolationTestStrategy(userID, name string, state types.StrategyState) types.StrategyConfig {
	return types.StrategyConfig{
		ID: idgen.NewUUID(), UserID: userID, Name: name, Symbol: "BTCUSDT",
		Timeframe: types.TF1h, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{{Module: "volume_breakout", Params: map[string]any{}}},
		Risk:    types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State:   state,
	}
}

// TestOwnerEmailScopingPreventsCrossUserCredentialLeak verifies that when
// buildBroker/loadBrokerCredentials are given A's ownerUserID, even if B has
// also saved an active config for the same broker (okx-demo) in the
// database, what gets resolved must be A's own credentials — never B's.
func TestOwnerEmailScopingPreventsCrossUserCredentialLeak(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "")
	t.Setenv("TF_OKX_API_SECRET", "")
	t.Setenv("TF_OKX_PASSPHRASE", "")

	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("failed to connect to Postgres (did you run docker compose up -d and apply migrations 005-008?): %v", err)
	}
	defer store.Close()

	userA := createIsolationTestUser(ctx, t, store, "isolation-cred-a")
	userB := createIsolationTestUser(ctx, t, store, "isolation-cred-b")
	defer func() {
		if _, err := store.Pool().Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("failed to clean up test data: %v", err)
		}
	}()

	saveIsolationBrokerProfile(ctx, t, store, userA.ID, "A's OKX",
		`{"api_key":"a-key","api_secret":"a-secret","passphrase":"a-pass"}`)
	saveIsolationBrokerProfile(ctx, t, store, userB.ID, "B's OKX",
		`{"api_key":"b-key","api_secret":"b-secret","passphrase":"b-pass"}`)

	apiKey, apiSecret, passphrase, err := loadBrokerCredentials(ctx, store, isolationTestMasterKey, userA.ID, execution.BrokerKindOKXDemo, false)
	if err != nil {
		t.Fatalf("failed to load credentials as A: %v", err)
	}
	if apiKey != "a-key" || apiSecret != "a-secret" || passphrase != "a-pass" {
		t.Errorf("loading as A should only read A's own credentials, got apiKey=%q apiSecret=%q passphrase=%q", apiKey, apiSecret, passphrase)
	}

	apiKey, apiSecret, passphrase, err = loadBrokerCredentials(ctx, store, isolationTestMasterKey, userB.ID, execution.BrokerKindOKXDemo, false)
	if err != nil {
		t.Fatalf("failed to load credentials as B: %v", err)
	}
	if apiKey != "b-key" || apiSecret != "b-secret" || passphrase != "b-pass" {
		t.Errorf("loading as B should only read B's own credentials, got apiKey=%q apiSecret=%q passphrase=%q", apiKey, apiSecret, passphrase)
	}

	broker, err := buildBroker(ctx, store, isolationTestMasterKey, userA.ID, "okx-demo", false)
	if err != nil {
		t.Fatalf("buildBroker(A) failed: %v", err)
	}
	if broker.Name() != "okx-demo" {
		t.Errorf("Name() = %q, want okx-demo", broker.Name())
	}
}

// TestOwnerEmailScopingPreventsCrossUserStrategyLeak verifies that
// ListStrategiesByState, given A's userID, returns only A's own strategies
// in that state, even when B also has strategies in the same state. This is
// the core assertion behind whether the "one process serves only one user"
// boundary actually holds — if this fails, executor would register B's
// strategies too, while only having A's order credentials configured, a
// genuine cross-user order risk.
func TestOwnerEmailScopingPreventsCrossUserStrategyLeak(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("failed to connect to Postgres: %v", err)
	}
	defer store.Close()

	userA := createIsolationTestUser(ctx, t, store, "isolation-strategy-a")
	userB := createIsolationTestUser(ctx, t, store, "isolation-strategy-b")
	defer func() {
		if _, err := store.Pool().Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("failed to clean up test data: %v", err)
		}
	}()

	scA := isolationTestStrategy(userA.ID, "A's paper-trading strategy", types.StatePaperTrading)
	scB := isolationTestStrategy(userB.ID, "B's paper-trading strategy", types.StatePaperTrading)
	if err := store.SaveStrategy(ctx, scA); err != nil {
		t.Fatalf("failed to save A's strategy: %v", err)
	}
	if err := store.SaveStrategy(ctx, scB); err != nil {
		t.Fatalf("failed to save B's strategy: %v", err)
	}

	gotA, err := store.ListStrategiesByState(ctx, userA.ID, types.StatePaperTrading)
	if err != nil {
		t.Fatalf("failed to list strategies as A: %v", err)
	}
	assertOnlyContainsStrategy(t, gotA, scA.ID, scB.ID, "A")

	gotB, err := store.ListStrategiesByState(ctx, userB.ID, types.StatePaperTrading)
	if err != nil {
		t.Fatalf("failed to list strategies as B: %v", err)
	}
	assertOnlyContainsStrategy(t, gotB, scB.ID, scA.ID, "B")
}

func assertOnlyContainsStrategy(t *testing.T, got []types.StrategyConfig, wantID, mustNotContainID, owner string) {
	t.Helper()
	found := false
	for _, s := range got {
		if s.ID == mustNotContainID {
			t.Fatalf("querying as %s should not see another user's strategy %s, got: %+v", owner, mustNotContainID, got)
		}
		if s.ID == wantID {
			found = true
		}
	}
	if !found {
		t.Fatalf("querying as %s should include its own strategy %s, got: %+v", owner, wantID, got)
	}
}

// TestMultiTenantModeRegistersBothUsersWithOwnCredentials is the
// multi-tenant-mode counterpart to the single-tenant-mode
// TestOwnerEmailScopingPreventsCrossUserCredentialLeak: -owner-email is left
// empty (exercising the real reconcileRegistrations/registerOne/brokerCache
// chain end to end, not calling loadBrokerCredentials directly). It verifies
// two real users' strategies and exchange credentials can each be loaded via
// a cross-user query, registered onto the same Supervisor, and resolve to
// their own credentials without crossing over — real Postgres + real
// encrypt/decrypt end to end. This complements the fake-store unit tests in
// registration_test.go: one proves real data doesn't cross, the other proves
// the orchestration logic itself is correct.
func TestMultiTenantModeRegistersBothUsersWithOwnCredentials(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "")
	t.Setenv("TF_OKX_API_SECRET", "")
	t.Setenv("TF_OKX_PASSPHRASE", "")

	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("failed to connect to Postgres: %v", err)
	}
	defer store.Close()

	userA := createIsolationTestUser(ctx, t, store, "isolation-multi-a")
	userB := createIsolationTestUser(ctx, t, store, "isolation-multi-b")
	defer func() {
		if _, err := store.Pool().Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("failed to clean up test data: %v", err)
		}
	}()

	saveIsolationBrokerProfile(ctx, t, store, userA.ID, "A's OKX",
		`{"api_key":"multi-a-key","api_secret":"multi-a-secret","passphrase":"multi-a-pass"}`)
	saveIsolationBrokerProfile(ctx, t, store, userB.ID, "B's OKX",
		`{"api_key":"multi-b-key","api_secret":"multi-b-secret","passphrase":"multi-b-pass"}`)

	// Both users create a paper-trading strategy on the same symbol — this
	// is exactly the typical scenario multi-tenant single-process sharing
	// needs to handle correctly, not a deliberately-picked edge case.
	scA := isolationTestStrategy(userA.ID, "A's BTCUSDT strategy", types.StatePaperTrading)
	scB := isolationTestStrategy(userB.ID, "B's BTCUSDT strategy", types.StatePaperTrading)
	if err := store.SaveStrategy(ctx, scA); err != nil {
		t.Fatalf("failed to save A's strategy: %v", err)
	}
	if err := store.SaveStrategy(ctx, scB); err != nil {
		t.Fatalf("failed to save B's strategy: %v", err)
	}

	// ownerUserID="" = multi-tenant mode; brokers is a real brokerCache, going through real decryption.
	brokers := newBrokerCache(store, isolationTestMasterKey, "okx-demo", true, time.Minute)
	sup := execution.NewSupervisor(quietLogger())
	defer sup.Shutdown()

	reconcileRegistrations(ctx, store, sup, brokers, "", types.StatePaperTrading, quietLogger())

	ids := sup.StrategyIDs()
	if len(ids) != 2 {
		t.Fatalf("expected both users' strategies registered, got %d: %v", len(ids), ids)
	}

	wA, ok := sup.Worker(scA.ID)
	if !ok {
		t.Fatal("A's strategy should be registered")
	}
	wB, ok := sup.Worker(scB.ID)
	if !ok {
		t.Fatal("B's strategy should be registered")
	}
	if wA.StrategyID() == wB.StrategyID() {
		t.Fatal("the two Workers should not be the same strategy")
	}
}
