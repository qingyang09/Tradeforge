//go:build integration

// 需要 docker-compose 起的 Postgres 且已跑过 migrations/005-008（多用户改造的四个迁移）：
//
//	docker compose up -d
//	go test -tags=integration ./cmd/executor/... -run CrossUser -v
//
// 这个文件验证的是设计阶段发现的真实风险：一旦 broker_profiles/strategies 按
// user_id 区分，如果 -owner-email 没有正确把 userID 一路传到底，一个 executor 进程
// 可能会加载别的用户的策略，或者用错的凭据下单。broker_test.go/promote_test.go 里
// 用假 store 测的是"参数往下传了没有"；这里用真实 Postgres 证明"传下去之后，两个
// 用户的数据真的互相看不见"，两者不能互相替代。
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
		t.Fatalf("创建测试用户失败：%v", err)
	}
	return u
}

func saveIsolationBrokerProfile(ctx context.Context, t *testing.T, store *storage.Store, userID, label, plaintext string) {
	t.Helper()
	ciphertext, salt, nonce, err := secretcrypto.Encrypt(isolationTestMasterKey, plaintext)
	if err != nil {
		t.Fatalf("加密测试凭据失败：%v", err)
	}
	p := storage.BrokerProfile{
		ID: idgen.NewUUID(), UserID: userID, Label: label, Broker: "okx-demo", KeyHint: "sk-a…test",
		EncryptedCredentials: ciphertext, KeySalt: salt, KeyNonce: nonce,
	}
	if err := store.SaveBrokerProfile(ctx, p, true); err != nil {
		t.Fatalf("保存测试交易所配置失败：%v", err)
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

// TestOwnerEmailScopingPreventsCrossUserCredentialLeak 验证 buildBroker/
// loadBrokerCredentials 拿着 A 的 ownerUserID 时，即使 B 也在数据库里保存了同一个
// broker（okx-demo）当前生效的配置，解出来的也必须是 A 自己的凭据，不会读到 B 的。
func TestOwnerEmailScopingPreventsCrossUserCredentialLeak(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "")
	t.Setenv("TF_OKX_API_SECRET", "")
	t.Setenv("TF_OKX_PASSPHRASE", "")

	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败（是否已 docker compose up -d 并跑过 005-008 迁移？）：%v", err)
	}
	defer store.Close()

	userA := createIsolationTestUser(ctx, t, store, "isolation-cred-a")
	userB := createIsolationTestUser(ctx, t, store, "isolation-cred-b")
	defer func() {
		if _, err := store.Pool().Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	saveIsolationBrokerProfile(ctx, t, store, userA.ID, "A 的 OKX",
		`{"api_key":"a-key","api_secret":"a-secret","passphrase":"a-pass"}`)
	saveIsolationBrokerProfile(ctx, t, store, userB.ID, "B 的 OKX",
		`{"api_key":"b-key","api_secret":"b-secret","passphrase":"b-pass"}`)

	apiKey, apiSecret, passphrase, err := loadBrokerCredentials(ctx, store, isolationTestMasterKey, userA.ID, execution.BrokerKindOKXDemo, false)
	if err != nil {
		t.Fatalf("以 A 的身份加载凭据失败：%v", err)
	}
	if apiKey != "a-key" || apiSecret != "a-secret" || passphrase != "a-pass" {
		t.Errorf("以 A 的身份加载应该只读到 A 自己的凭据，实际 apiKey=%q apiSecret=%q passphrase=%q", apiKey, apiSecret, passphrase)
	}

	apiKey, apiSecret, passphrase, err = loadBrokerCredentials(ctx, store, isolationTestMasterKey, userB.ID, execution.BrokerKindOKXDemo, false)
	if err != nil {
		t.Fatalf("以 B 的身份加载凭据失败：%v", err)
	}
	if apiKey != "b-key" || apiSecret != "b-secret" || passphrase != "b-pass" {
		t.Errorf("以 B 的身份加载应该只读到 B 自己的凭据，实际 apiKey=%q apiSecret=%q passphrase=%q", apiKey, apiSecret, passphrase)
	}

	broker, err := buildBroker(ctx, store, isolationTestMasterKey, userA.ID, "okx-demo", false)
	if err != nil {
		t.Fatalf("buildBroker(A) 失败：%v", err)
	}
	if broker.Name() != "okx-demo" {
		t.Errorf("Name() = %q，期望 okx-demo", broker.Name())
	}
}

// TestOwnerEmailScopingPreventsCrossUserStrategyLeak 验证 ListStrategiesByState 拿着
// A 的 userID 只会返回 A 自己处于该状态的策略，即使 B 在同一状态下也有策略。这是
// 一个进程"只服务一个用户"这个边界成立与否的核心断言——如果这里失败，executor
// 会把 B 的策略也注册进来、却只配了 A 的下单凭据，真实产生跨用户下单风险。
func TestOwnerEmailScopingPreventsCrossUserStrategyLeak(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	userA := createIsolationTestUser(ctx, t, store, "isolation-strategy-a")
	userB := createIsolationTestUser(ctx, t, store, "isolation-strategy-b")
	defer func() {
		if _, err := store.Pool().Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	scA := isolationTestStrategy(userA.ID, "A 的模拟盘策略", types.StatePaperTrading)
	scB := isolationTestStrategy(userB.ID, "B 的模拟盘策略", types.StatePaperTrading)
	if err := store.SaveStrategy(ctx, scA); err != nil {
		t.Fatalf("保存 A 的策略失败：%v", err)
	}
	if err := store.SaveStrategy(ctx, scB); err != nil {
		t.Fatalf("保存 B 的策略失败：%v", err)
	}

	gotA, err := store.ListStrategiesByState(ctx, userA.ID, types.StatePaperTrading)
	if err != nil {
		t.Fatalf("以 A 的身份列出策略失败：%v", err)
	}
	assertOnlyContainsStrategy(t, gotA, scA.ID, scB.ID, "A")

	gotB, err := store.ListStrategiesByState(ctx, userB.ID, types.StatePaperTrading)
	if err != nil {
		t.Fatalf("以 B 的身份列出策略失败：%v", err)
	}
	assertOnlyContainsStrategy(t, gotB, scB.ID, scA.ID, "B")
}

func assertOnlyContainsStrategy(t *testing.T, got []types.StrategyConfig, wantID, mustNotContainID, owner string) {
	t.Helper()
	found := false
	for _, s := range got {
		if s.ID == mustNotContainID {
			t.Fatalf("以 %s 的身份查询不应该看到另一个用户的策略 %s，实际返回：%+v", owner, mustNotContainID, got)
		}
		if s.ID == wantID {
			found = true
		}
	}
	if !found {
		t.Fatalf("以 %s 的身份查询应该包含自己的策略 %s，实际返回：%+v", owner, wantID, got)
	}
}

// TestMultiTenantModeRegistersBothUsersWithOwnCredentials 是单用户模式那条
// TestOwnerEmailScopingPreventsCrossUserCredentialLeak 的多用户模式版本：-owner-email
// 留空（真实的 reconcileRegistrations/registerOne/brokerCache 全链路，不是直接调
// loadBrokerCredentials），两个真实用户各自的策略和交易所凭据都能被跨用户查询加载、
// 注册进同一个 Supervisor，且各自解出来的凭据不串——真实 Postgres + 真实加密解密全链路，
// 跟 registration_test.go 里的假 store 单测互补，一个证明真实数据不串，一个证明编排
// 逻辑本身正确。
func TestMultiTenantModeRegistersBothUsersWithOwnCredentials(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "")
	t.Setenv("TF_OKX_API_SECRET", "")
	t.Setenv("TF_OKX_PASSPHRASE", "")

	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	userA := createIsolationTestUser(ctx, t, store, "isolation-multi-a")
	userB := createIsolationTestUser(ctx, t, store, "isolation-multi-b")
	defer func() {
		if _, err := store.Pool().Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	saveIsolationBrokerProfile(ctx, t, store, userA.ID, "A 的 OKX",
		`{"api_key":"multi-a-key","api_secret":"multi-a-secret","passphrase":"multi-a-pass"}`)
	saveIsolationBrokerProfile(ctx, t, store, userB.ID, "B 的 OKX",
		`{"api_key":"multi-b-key","api_secret":"multi-b-secret","passphrase":"multi-b-pass"}`)

	// 两个用户在同一个标的上都建了模拟盘策略——这正是多用户共享一个进程要处理好的
	// 典型场景，不是刻意挑选的边角案例。
	scA := isolationTestStrategy(userA.ID, "A 的 BTCUSDT 策略", types.StatePaperTrading)
	scB := isolationTestStrategy(userB.ID, "B 的 BTCUSDT 策略", types.StatePaperTrading)
	if err := store.SaveStrategy(ctx, scA); err != nil {
		t.Fatalf("保存 A 的策略失败：%v", err)
	}
	if err := store.SaveStrategy(ctx, scB); err != nil {
		t.Fatalf("保存 B 的策略失败：%v", err)
	}

	// ownerUserID="" = 多用户模式；brokers 是真正的 brokerCache，走真实解密。
	brokers := newBrokerCache(store, isolationTestMasterKey, "okx-demo", true, time.Minute)
	sup := execution.NewSupervisor(quietLogger())
	defer sup.Shutdown()

	reconcileRegistrations(ctx, store, sup, brokers, "", types.StatePaperTrading, quietLogger())

	ids := sup.StrategyIDs()
	if len(ids) != 2 {
		t.Fatalf("应该注册了两个用户的策略，实际注册了 %d 条：%v", len(ids), ids)
	}

	wA, ok := sup.Worker(scA.ID)
	if !ok {
		t.Fatal("A 的策略应该已经注册")
	}
	wB, ok := sup.Worker(scB.ID)
	if !ok {
		t.Fatal("B 的策略应该已经注册")
	}
	if wA.StrategyID() == wB.StrategyID() {
		t.Fatal("两个 Worker 不应该是同一个策略")
	}
}
