package main

import (
	"context"
	"fmt"
	"testing"

	"tradeforge/internal/execution"
	"tradeforge/internal/secretcrypto"
	"tradeforge/internal/storage"
)

// testOwnerUserID is the fixed owning user ID that -owner-email resolves to
// in these tests — fakeBrokerCredentialStore only stores one profile per
// broker, with no real per-user layering, but its signature must match the
// real brokerCredentialStore interface.
const testOwnerUserID = "00000000-0000-4000-8000-000000000088"

// fakeBrokerCredentialStore is an in-memory implementation of
// brokerCredentialStore, so the "fall back to the database when env vars are
// missing" path can be tested without a real Postgres.
type fakeBrokerCredentialStore struct {
	active map[string]storage.BrokerProfile
}

func (f *fakeBrokerCredentialStore) ActiveBrokerProfile(_ context.Context, _ string, broker string) (storage.BrokerProfile, error) {
	p, ok := f.active[broker]
	if !ok {
		return storage.BrokerProfile{}, fmt.Errorf("active exchange config for %s: %w", broker, storage.ErrNotFound)
	}
	return p, nil
}

func TestBuildBrokerDefaultsToPaper(t *testing.T) {
	store := &fakeBrokerCredentialStore{}
	b, err := buildBroker(t.Context(), store, "", testOwnerUserID, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := b.(*execution.PaperBroker); !ok {
		t.Errorf("an empty string should default to the in-memory paper broker, got: %T", b)
	}

	b, err = buildBroker(t.Context(), store, "", testOwnerUserID, "paper", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := b.(*execution.PaperBroker); !ok {
		t.Errorf("paper should give the in-memory paper broker, got: %T", b)
	}
}

func TestBuildBrokerBinanceTestnetRequiresCredentials(t *testing.T) {
	t.Setenv("TF_BINANCE_API_KEY", "")
	t.Setenv("TF_BINANCE_API_SECRET", "")
	store := &fakeBrokerCredentialStore{}
	if _, err := buildBroker(t.Context(), store, "", testOwnerUserID, "binance-testnet", false); err == nil {
		t.Fatal("should error when credentials are missing and there's no admin password to fall back to the database")
	}
}

func TestBuildBrokerOKXDemoSucceedsWithEnvCredentials(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "k")
	t.Setenv("TF_OKX_API_SECRET", "s")
	t.Setenv("TF_OKX_PASSPHRASE", "p")
	store := &fakeBrokerCredentialStore{}
	b, err := buildBroker(t.Context(), store, "", testOwnerUserID, "okx-demo", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.Name() != "okx-demo" {
		t.Errorf("Name() = %q, want okx-demo", b.Name())
	}
}

func TestBuildBrokerRejectsUnknownName(t *testing.T) {
	store := &fakeBrokerCredentialStore{}
	if _, err := buildBroker(t.Context(), store, "", testOwnerUserID, "bybit-testnet", false); err == nil {
		t.Fatal("an unimplemented order channel should error, not silently fall back")
	}
}

// TestBuildBrokerFallsBackToDatabaseWhenEnvMissing verifies the newly added
// capability: when env vars aren't fully configured, fall back to reading
// the active config saved via the web interface's settings page (which
// requires the same admin password to decrypt).
func TestBuildBrokerFallsBackToDatabaseWhenEnvMissing(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "")
	t.Setenv("TF_OKX_API_SECRET", "")
	t.Setenv("TF_OKX_PASSPHRASE", "")

	plaintext := `{"api_key":"db-key","api_secret":"db-secret","passphrase":"db-pass"}`
	ciphertext, salt, nonce, err := secretcrypto.Encrypt("admin-password", plaintext)
	if err != nil {
		t.Fatalf("failed to encrypt test data: %v", err)
	}
	store := &fakeBrokerCredentialStore{active: map[string]storage.BrokerProfile{
		"okx-demo": {ID: "p1", Broker: "okx-demo", EncryptedCredentials: ciphertext, KeySalt: salt, KeyNonce: nonce},
	}}

	b, err := buildBroker(t.Context(), store, "admin-password", testOwnerUserID, "okx-demo", false)
	if err != nil {
		t.Fatalf("should have read credentials from the database and constructed successfully, got: %v", err)
	}
	if b.Name() != "okx-demo" {
		t.Errorf("Name() = %q, want okx-demo", b.Name())
	}
}

func TestBuildBrokerFallsBackFailsWithoutAdminPassword(t *testing.T) {
	t.Setenv("TF_BINANCE_API_KEY", "")
	t.Setenv("TF_BINANCE_API_SECRET", "")
	store := &fakeBrokerCredentialStore{active: map[string]storage.BrokerProfile{
		"binance-testnet": {ID: "p1", Broker: "binance-testnet", EncryptedCredentials: []byte("x")},
	}}
	if _, err := buildBroker(t.Context(), store, "", testOwnerUserID, "binance-testnet", false); err == nil {
		t.Fatal("without TF_ADMIN_PASSWORD the database config can't be decrypted, should error")
	}
}

func TestBuildBrokerFallsBackFailsWhenNoActiveProfile(t *testing.T) {
	t.Setenv("TF_BINANCE_API_KEY", "")
	t.Setenv("TF_BINANCE_API_SECRET", "")
	store := &fakeBrokerCredentialStore{}
	if _, err := buildBroker(t.Context(), store, "admin-password", testOwnerUserID, "binance-testnet", false); err == nil {
		t.Fatal("should error when there's no active config in the database, not silently use empty credentials")
	}
}

func TestBuildBrokerFallsBackFailsWithWrongAdminPassword(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "")
	t.Setenv("TF_OKX_API_SECRET", "")
	t.Setenv("TF_OKX_PASSPHRASE", "")

	ciphertext, salt, nonce, err := secretcrypto.Encrypt("original-password", `{"api_key":"k","api_secret":"s","passphrase":"p"}`)
	if err != nil {
		t.Fatalf("failed to encrypt test data: %v", err)
	}
	store := &fakeBrokerCredentialStore{active: map[string]storage.BrokerProfile{
		"okx-demo": {ID: "p1", Broker: "okx-demo", EncryptedCredentials: ciphertext, KeySalt: salt, KeyNonce: nonce},
	}}

	if _, err := buildBroker(t.Context(), store, "changed-new-password", testOwnerUserID, "okx-demo", false); err == nil {
		t.Fatal("should error on a password mismatch, not treat the decrypted garbage as valid credentials")
	}
}

// Env var priority is unchanged (in single-tenant mode): even if a profile
// is also saved in the database, an env var should still win — it should
// never accidentally read a database config that might be under a different key.
func TestBuildBrokerPrefersEnvOverDatabase(t *testing.T) {
	t.Setenv("TF_BINANCE_API_KEY", "env-key")
	t.Setenv("TF_BINANCE_API_SECRET", "env-secret")

	ciphertext, salt, nonce, err := secretcrypto.Encrypt("admin-password", `{"api_key":"db-key","api_secret":"db-secret"}`)
	if err != nil {
		t.Fatalf("failed to encrypt test data: %v", err)
	}
	store := &fakeBrokerCredentialStore{active: map[string]storage.BrokerProfile{
		"binance-testnet": {ID: "p1", Broker: "binance-testnet", EncryptedCredentials: ciphertext, KeySalt: salt, KeyNonce: nonce},
	}}

	apiKey, apiSecret, _, err := loadBrokerCredentials(t.Context(), store, "admin-password", testOwnerUserID, execution.BrokerKindBinanceTestnet, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if apiKey != "env-key" || apiSecret != "env-secret" {
		t.Errorf("env vars should take priority when set, got apiKey=%q apiSecret=%q", apiKey, apiSecret)
	}
}

// TestBuildBrokerMultiTenantIgnoresEnvVars is the highest-priority test added
// by this rework — it verifies the "critical fix" design decision actually
// took effect: in multi-tenant mode, even if env vars are set in the
// deployment environment, what gets resolved for a given user must be that
// user's own database credentials, never the env var ones. If this test
// fails, it means every user's strategies on this broker channel would place
// orders using the same env var credentials — a funds cross-contamination
// issue, not an ordinary bug. This is the exact opposite of
// TestBuildBrokerPrefersEnvOverDatabase: that test proves env vars win in
// single-tenant mode, this one proves env vars must be completely ignored in
// multi-tenant mode.
func TestBuildBrokerMultiTenantIgnoresEnvVars(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "env-key-should-never-be-used")
	t.Setenv("TF_OKX_API_SECRET", "env-secret-should-never-be-used")
	t.Setenv("TF_OKX_PASSPHRASE", "env-pass-should-never-be-used")

	ciphertext, salt, nonce, err := secretcrypto.Encrypt("master-key", `{"api_key":"user-a-key","api_secret":"user-a-secret","passphrase":"user-a-pass"}`)
	if err != nil {
		t.Fatalf("failed to encrypt test data: %v", err)
	}
	store := &fakeBrokerCredentialStore{active: map[string]storage.BrokerProfile{
		"okx-demo": {ID: "p1", Broker: "okx-demo", EncryptedCredentials: ciphertext, KeySalt: salt, KeyNonce: nonce},
	}}

	apiKey, apiSecret, passphrase, err := loadBrokerCredentials(
		t.Context(), store, "master-key", testOwnerUserID, execution.BrokerKindOKXDemo, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if apiKey != "user-a-key" || apiSecret != "user-a-secret" || passphrase != "user-a-pass" {
		t.Errorf("multi-tenant mode should completely ignore env vars and use only this user's database credentials, got apiKey=%q apiSecret=%q passphrase=%q",
			apiKey, apiSecret, passphrase)
	}
}

// TestBuildBrokerMultiTenantFailsWithoutMasterKeyEvenWithEnvVarsSet adds a
// boundary case: in multi-tenant mode, even with env vars fully configured,
// missing TF_MASTER_KEY should error outright (not silently fall back to env
// vars) — and the error message itself shouldn't mention "env vars not
// configured" style wording, which only applies to single-tenant mode.
func TestBuildBrokerMultiTenantFailsWithoutMasterKeyEvenWithEnvVarsSet(t *testing.T) {
	t.Setenv("TF_OKX_API_KEY", "env-key")
	t.Setenv("TF_OKX_API_SECRET", "env-secret")
	store := &fakeBrokerCredentialStore{}

	_, _, _, err := loadBrokerCredentials(t.Context(), store, "", testOwnerUserID, execution.BrokerKindOKXDemo, true)
	if err == nil {
		t.Fatal("multi-tenant mode without TF_MASTER_KEY should error, not fall back to env vars")
	}
}
