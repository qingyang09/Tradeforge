package main

import (
	"context"
	"fmt"
	"testing"

	"tradeforge/internal/execution"
	"tradeforge/internal/secretcrypto"
	"tradeforge/internal/storage"
)

// testOwnerUserID 是这些测试里 -owner-email 解析出的固定归属用户 ID——
// fakeBrokerCredentialStore 只按 broker 存一份，不需要真的按用户分层，
// 但签名要跟真实 brokerCredentialStore 接口保持一致。
const testOwnerUserID = "00000000-0000-4000-8000-000000000088"

// fakeBrokerCredentialStore 是 brokerCredentialStore 的内存实现，不需要真实 Postgres
// 就能测试"环境变量缺失时退回数据库"这条路径。
type fakeBrokerCredentialStore struct {
	active map[string]storage.BrokerProfile
}

func (f *fakeBrokerCredentialStore) ActiveBrokerProfile(_ context.Context, _ string, broker string) (storage.BrokerProfile, error) {
	p, ok := f.active[broker]
	if !ok {
		return storage.BrokerProfile{}, fmt.Errorf("%s 当前生效的交易所配置：%w", broker, storage.ErrNotFound)
	}
	return p, nil
}

func TestBuildBrokerDefaultsToPaper(t *testing.T) {
	store := &fakeBrokerCredentialStore{}
	b, err := buildBroker(t.Context(), store, "", testOwnerUserID, "", false)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if _, ok := b.(*execution.PaperBroker); !ok {
		t.Errorf("空字符串应该默认给纯内存模拟盘，实际：%T", b)
	}

	b, err = buildBroker(t.Context(), store, "", testOwnerUserID, "paper", false)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if _, ok := b.(*execution.PaperBroker); !ok {
		t.Errorf("paper 应该给纯内存模拟盘，实际：%T", b)
	}
}

func TestBuildBrokerBinanceTestnetRequiresCredentials(t *testing.T) {
	t.Setenv("TF_BINANCE_API_KEY", "")
	t.Setenv("TF_BINANCE_API_SECRET", "")
	store := &fakeBrokerCredentialStore{}
	if _, err := buildBroker(t.Context(), store, "", testOwnerUserID, "binance-testnet", false); err == nil {
		t.Fatal("缺少密钥、也没有登录密码可退回数据库时应该报错")
	}
}

func TestBuildBrokerOKXDemoSucceedsWithEnvCredentials(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "k")
	t.Setenv("TF_OKX_API_SECRET", "s")
	t.Setenv("TF_OKX_PASSPHRASE", "p")
	store := &fakeBrokerCredentialStore{}
	b, err := buildBroker(t.Context(), store, "", testOwnerUserID, "okx-demo", false)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if b.Name() != "okx-demo" {
		t.Errorf("Name() = %q，期望 okx-demo", b.Name())
	}
}

func TestBuildBrokerRejectsUnknownName(t *testing.T) {
	store := &fakeBrokerCredentialStore{}
	if _, err := buildBroker(t.Context(), store, "", testOwnerUserID, "bybit-testnet", false); err == nil {
		t.Fatal("未实现的下单通道应该报错，而不是静默回退")
	}
}

// TestBuildBrokerFallsBackToDatabaseWhenEnvMissing 验证这次新增的能力：环境变量没配全时，
// 退回读取 Web 界面设置页面保存的、当前生效的一份配置（需要同一个登录密码解密）。
func TestBuildBrokerFallsBackToDatabaseWhenEnvMissing(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "")
	t.Setenv("TF_OKX_API_SECRET", "")
	t.Setenv("TF_OKX_PASSPHRASE", "")

	plaintext := `{"api_key":"db-key","api_secret":"db-secret","passphrase":"db-pass"}`
	ciphertext, salt, nonce, err := secretcrypto.Encrypt("admin-密码", plaintext)
	if err != nil {
		t.Fatalf("加密测试数据失败：%v", err)
	}
	store := &fakeBrokerCredentialStore{active: map[string]storage.BrokerProfile{
		"okx-demo": {ID: "p1", Broker: "okx-demo", EncryptedCredentials: ciphertext, KeySalt: salt, KeyNonce: nonce},
	}}

	b, err := buildBroker(t.Context(), store, "admin-密码", testOwnerUserID, "okx-demo", false)
	if err != nil {
		t.Fatalf("应该能从数据库读到凭据并成功构造，实际：%v", err)
	}
	if b.Name() != "okx-demo" {
		t.Errorf("Name() = %q，期望 okx-demo", b.Name())
	}
}

func TestBuildBrokerFallsBackFailsWithoutAdminPassword(t *testing.T) {
	t.Setenv("TF_BINANCE_API_KEY", "")
	t.Setenv("TF_BINANCE_API_SECRET", "")
	store := &fakeBrokerCredentialStore{active: map[string]storage.BrokerProfile{
		"binance-testnet": {ID: "p1", Broker: "binance-testnet", EncryptedCredentials: []byte("x")},
	}}
	if _, err := buildBroker(t.Context(), store, "", testOwnerUserID, "binance-testnet", false); err == nil {
		t.Fatal("没有 TF_ADMIN_PASSWORD 就无法解密数据库里的配置，应该报错")
	}
}

func TestBuildBrokerFallsBackFailsWhenNoActiveProfile(t *testing.T) {
	t.Setenv("TF_BINANCE_API_KEY", "")
	t.Setenv("TF_BINANCE_API_SECRET", "")
	store := &fakeBrokerCredentialStore{}
	if _, err := buildBroker(t.Context(), store, "admin-密码", testOwnerUserID, "binance-testnet", false); err == nil {
		t.Fatal("数据库里没有当前生效的配置时应该报错，而不是静默用空凭据")
	}
}

func TestBuildBrokerFallsBackFailsWithWrongAdminPassword(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "")
	t.Setenv("TF_OKX_API_SECRET", "")
	t.Setenv("TF_OKX_PASSPHRASE", "")

	ciphertext, salt, nonce, err := secretcrypto.Encrypt("原密码", `{"api_key":"k","api_secret":"s","passphrase":"p"}`)
	if err != nil {
		t.Fatalf("加密测试数据失败：%v", err)
	}
	store := &fakeBrokerCredentialStore{active: map[string]storage.BrokerProfile{
		"okx-demo": {ID: "p1", Broker: "okx-demo", EncryptedCredentials: ciphertext, KeySalt: salt, KeyNonce: nonce},
	}}

	if _, err := buildBroker(t.Context(), store, "改过的新密码", testOwnerUserID, "okx-demo", false); err == nil {
		t.Fatal("密码对不上时应该报错，而不是把解密出的垃圾数据当成凭据用")
	}
}

// 环境变量优先级不变（单用户模式下）：即使数据库里也保存了一份，配了环境变量就该用
// 环境变量，不该意外读到数据库里可能是另一把 key 的配置。
func TestBuildBrokerPrefersEnvOverDatabase(t *testing.T) {
	t.Setenv("TF_BINANCE_API_KEY", "env-key")
	t.Setenv("TF_BINANCE_API_SECRET", "env-secret")

	ciphertext, salt, nonce, err := secretcrypto.Encrypt("admin-密码", `{"api_key":"db-key","api_secret":"db-secret"}`)
	if err != nil {
		t.Fatalf("加密测试数据失败：%v", err)
	}
	store := &fakeBrokerCredentialStore{active: map[string]storage.BrokerProfile{
		"binance-testnet": {ID: "p1", Broker: "binance-testnet", EncryptedCredentials: ciphertext, KeySalt: salt, KeyNonce: nonce},
	}}

	apiKey, apiSecret, _, err := loadBrokerCredentials(t.Context(), store, "admin-密码", testOwnerUserID, execution.BrokerKindBinanceTestnet, false)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if apiKey != "env-key" || apiSecret != "env-secret" {
		t.Errorf("配了环境变量时应该优先用环境变量，实际 apiKey=%q apiSecret=%q", apiKey, apiSecret)
	}
}

// TestBuildBrokerMultiTenantIgnoresEnvVars 是这一轮改造里最高优先级的一条测试——
// 验证"关键修复"那条设计决策真的生效了：多用户模式下，哪怪部署环境里设了环境变量，
// 给某个用户解析出来的也必须是这个用户自己数据库里的凭据，绝不能是环境变量里那份。
// 如果这条测试失败，意味着这个 broker 通道下所有用户的策略会用同一份环境变量凭据
// 下单——是资金串号级别的问题，不是普通 bug。跟 TestBuildBrokerPrefersEnvOverDatabase
// 正好相反：那条测试证明单用户模式下环境变量优先，这条证明多用户模式下环境变量必须
// 被完全忽略。
func TestBuildBrokerMultiTenantIgnoresEnvVars(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "env-key-should-never-be-used")
	t.Setenv("TF_OKX_API_SECRET", "env-secret-should-never-be-used")
	t.Setenv("TF_OKX_PASSPHRASE", "env-pass-should-never-be-used")

	ciphertext, salt, nonce, err := secretcrypto.Encrypt("master-key", `{"api_key":"user-a-key","api_secret":"user-a-secret","passphrase":"user-a-pass"}`)
	if err != nil {
		t.Fatalf("加密测试数据失败：%v", err)
	}
	store := &fakeBrokerCredentialStore{active: map[string]storage.BrokerProfile{
		"okx-demo": {ID: "p1", Broker: "okx-demo", EncryptedCredentials: ciphertext, KeySalt: salt, KeyNonce: nonce},
	}}

	apiKey, apiSecret, passphrase, err := loadBrokerCredentials(
		t.Context(), store, "master-key", testOwnerUserID, execution.BrokerKindOKXDemo, true)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if apiKey != "user-a-key" || apiSecret != "user-a-secret" || passphrase != "user-a-pass" {
		t.Errorf("多用户模式应该完全忽略环境变量、只用该用户数据库里的凭据，实际 apiKey=%q apiSecret=%q passphrase=%q",
			apiKey, apiSecret, passphrase)
	}
}

// TestBuildBrokerMultiTenantFailsWithoutMasterKeyEvenWithEnvVarsSet 补一条边界：
// 多用户模式下，即便环境变量配满了，没有 TF_MASTER_KEY 也应该直接报错（而不是悄悄
// 用了环境变量兜底）——报错信息本身也不该再提"环境变量未配置"这种单用户模式才有的话术。
func TestBuildBrokerMultiTenantFailsWithoutMasterKeyEvenWithEnvVarsSet(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "env-key")
	t.Setenv("TF_OKX_API_SECRET", "env-secret")
	store := &fakeBrokerCredentialStore{}

	_, _, _, err := loadBrokerCredentials(t.Context(), store, "", testOwnerUserID, execution.BrokerKindOKXDemo, true)
	if err == nil {
		t.Fatal("多用户模式下没有 TF_MASTER_KEY 应该报错，而不是退回环境变量")
	}
}
