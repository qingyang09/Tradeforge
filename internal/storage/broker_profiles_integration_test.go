//go:build integration

// 需要 docker-compose 起的 Postgres，且已经跑过 migrations/004_broker_profiles.sql：
//
//	docker compose up -d
//	go test -tags=integration ./internal/storage/... -run BrokerProfile -v
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradeforge/internal/config"
	"tradeforge/pkg/idgen"
)

func newBrokerProfileForTest(userID, label, broker string) BrokerProfile {
	return BrokerProfile{
		ID: idgen.NewUUID(), UserID: userID, Label: label, Broker: broker, KeyHint: "sk-a…b12d",
		EncryptedCredentials: []byte{0x01, 0x02, 0x03, 0xff},
		KeySalt:              []byte{0xaa, 0xbb},
		KeyNonce:             []byte{0xcc, 0xdd, 0xee},
	}
}

func TestBrokerProfilesRoundTripAndActivationSwitchesWithinSameBroker(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败（是否已 docker compose up -d 并跑过 004 迁移？）：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "broker-profiles-roundtrip")
	a := newBrokerProfileForTest(u.ID, "集成测试 A", "okx-demo")
	b := newBrokerProfileForTest(u.ID, "集成测试 B", "okx-demo")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败，请手动检查 users/broker_profiles 表：%v", err)
		}
	}()

	if err := store.SaveBrokerProfile(ctx, a, true); err != nil {
		t.Fatalf("保存配置 A 失败：%v", err)
	}
	if err := store.SaveBrokerProfile(ctx, b, true); err != nil {
		t.Fatalf("保存配置 B 失败：%v", err)
	}

	active, err := store.ActiveBrokerProfile(ctx, u.ID, "okx-demo")
	if err != nil {
		t.Fatalf("读取当前生效配置失败：%v", err)
	}
	if active.ID != b.ID {
		t.Errorf("当前生效的应该是 B，实际：%+v", active)
	}

	gotA, err := store.GetBrokerProfile(ctx, u.ID, a.ID)
	if err != nil {
		t.Fatalf("读取配置 A 失败：%v", err)
	}
	if gotA.IsActive {
		t.Error("保存 B 并激活后，A 应该被切换成不生效")
	}
	if string(gotA.EncryptedCredentials) != string(a.EncryptedCredentials) {
		t.Errorf("密文往返不一致：got=%v want=%v", gotA.EncryptedCredentials, a.EncryptedCredentials)
	}

	if err := store.ActivateBrokerProfile(ctx, u.ID, a.ID); err != nil {
		t.Fatalf("激活配置 A 失败：%v", err)
	}
	active, err = store.ActiveBrokerProfile(ctx, u.ID, "okx-demo")
	if err != nil || active.ID != a.ID {
		t.Errorf("激活 A 后当前生效的应该是 A，实际：%+v err=%v", active, err)
	}

	all, err := store.ListBrokerProfiles(ctx, u.ID)
	if err != nil {
		t.Fatalf("列出全部配置失败：%v", err)
	}
	if len(all) < 2 {
		t.Errorf("至少应包含刚保存的两份配置，实际 %d 份", len(all))
	}

	if err := store.DeleteBrokerProfile(ctx, u.ID, b.ID); err != nil {
		t.Fatalf("删除配置 B 失败：%v", err)
	}
	if _, err := store.GetBrokerProfile(ctx, u.ID, b.ID); err == nil {
		t.Error("删除后应查不到配置 B")
	}
}

// TestBrokerProfilesActivationIsScopedPerBroker 是这个表跟 agent_profiles 最大的设计
// 差异所在：agent_profiles 每个用户只有一份生效配置，broker_profiles 按"用户+broker"
// 分组——激活一个 broker 的配置不该影响同一用户另一个 broker 已经生效的配置。
func TestBrokerProfilesActivationIsScopedPerBroker(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "broker-profiles-per-broker")
	okx := newBrokerProfileForTest(u.ID, "集成测试 OKX", "okx-demo")
	binance := newBrokerProfileForTest(u.ID, "集成测试 Binance", "binance-testnet")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败，请手动检查 users/broker_profiles 表：%v", err)
		}
	}()

	if err := store.SaveBrokerProfile(ctx, okx, true); err != nil {
		t.Fatalf("保存 OKX 配置失败：%v", err)
	}
	if err := store.SaveBrokerProfile(ctx, binance, true); err != nil {
		t.Fatalf("保存 Binance 配置失败：%v", err)
	}

	activeOKX, err := store.ActiveBrokerProfile(ctx, u.ID, "okx-demo")
	if err != nil || activeOKX.ID != okx.ID {
		t.Errorf("激活 Binance 不该影响 OKX 当前生效的配置，实际：%+v err=%v", activeOKX, err)
	}
	activeBinance, err := store.ActiveBrokerProfile(ctx, u.ID, "binance-testnet")
	if err != nil || activeBinance.ID != binance.ID {
		t.Errorf("Binance 当前生效的配置应该是刚保存的那份，实际：%+v err=%v", activeBinance, err)
	}
}

func TestActivateBrokerProfileRejectsUnknownID(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "broker-profiles-unknown-id")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	if err := store.ActivateBrokerProfile(ctx, u.ID, idgen.NewUUID()); err == nil {
		t.Fatal("激活一个不存在的配置 ID 应该报错")
	}
}

// TestBrokerProfileGetActivateDeleteRejectOtherUsersRow 是多用户隔离的核心属性：
// B 用户 Get/Activate/Delete A 用户的交易所配置，得到的错误必须跟"这行根本不存在"
// 完全一样。
func TestBrokerProfileGetActivateDeleteRejectOtherUsersRow(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "broker-profiles-cross-a")
	userB := createTestUserForStorage(ctx, t, store, "broker-profiles-cross-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	a := newBrokerProfileForTest(userA.ID, "A 的配置", "okx-demo")
	if err := store.SaveBrokerProfile(ctx, a, true); err != nil {
		t.Fatalf("保存配置 A 失败：%v", err)
	}

	if _, err := store.GetBrokerProfile(ctx, userB.ID, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B 读取 A 的配置应该跟读一个不存在的 ID 一样返回 ErrNotFound，实际：%v", err)
	}
	if err := store.ActivateBrokerProfile(ctx, userB.ID, a.ID); err == nil {
		t.Error("B 不应该能激活 A 的配置")
	}
	if err := store.DeleteBrokerProfile(ctx, userB.ID, a.ID); err == nil {
		t.Error("B 不应该能删除 A 的配置")
	}

	gotA, err := store.GetBrokerProfile(ctx, userA.ID, a.ID)
	if err != nil || !gotA.IsActive {
		t.Errorf("A 的配置应该完好无损地保持生效，实际：%+v err=%v", gotA, err)
	}
}
