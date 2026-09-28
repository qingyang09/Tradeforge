//go:build integration

// 需要 docker-compose 起的 Postgres，且已经跑过 migrations/002_agent_profiles.sql：
//
//	docker compose up -d
//	go test -tags=integration ./internal/storage/... -run AgentProfile -v
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradeforge/internal/config"
	"tradeforge/pkg/idgen"
)

// createTestUserForStorage 插入一个专用于本次测试的用户行——agent_profiles/
// broker_profiles 的 user_id 外键要求真的存在一行 users，不能直接塞一个随便编的
// UUID。邮箱按测试名加随机 ID 拼，避免并发跑多个集成测试时撞上唯一索引。
func createTestUserForStorage(ctx context.Context, t *testing.T, store *Store, label string) User {
	t.Helper()
	u := User{
		ID: idgen.NewUUID(), Email: label + "-" + idgen.NewUUID() + "@integration.test",
		PasswordHash: []byte("x"),
	}
	if err := store.CreateUser(ctx, u); err != nil {
		t.Fatalf("创建测试用户失败：%v", err)
	}
	return u
}

func newProfileForTest(userID, label string) AgentProfile {
	return AgentProfile{
		ID: idgen.NewUUID(), UserID: userID, Label: label, Provider: "anthropic", Model: "claude-opus-5",
		BaseURL: "", KeyHint: "sk-a…b12d",
		EncryptedAPIKey: []byte{0x01, 0x02, 0x03, 0xff},
		KeySalt:         []byte{0xaa, 0xbb},
		KeyNonce:        []byte{0xcc, 0xdd, 0xee},
	}
}

func TestAgentProfilesRoundTripAndActivationSwitches(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败（是否已 docker compose up -d 并跑过 002 迁移？）：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "agent-profiles-roundtrip")
	a := newProfileForTest(u.ID, "集成测试 A")
	b := newProfileForTest(u.ID, "集成测试 B")
	// 用 defer 而不是 t.Cleanup：t.Cleanup 注册的回调在测试函数体返回之后才跑，
	// 晚于函数体里已经登记的 defer store.Close()——那样这里再用 store.pool 会
	// 拿到"pool 已关闭"的错误，清理直接失败，垃圾数据就留在了共享的开发数据库里
	// （真的发生过一次：一条留下来的假密文行让 LoadActiveAgentProfile 在真实进程
	// 启动时 panic）。defer 是 LIFO，写在 defer store.Close() 之后就会先于它执行。
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败，请手动检查 users/agent_profiles 表：%v", err)
		}
	}()

	if err := store.SaveAgentProfile(ctx, a, true); err != nil {
		t.Fatalf("保存配置 A 失败：%v", err)
	}
	if err := store.SaveAgentProfile(ctx, b, true); err != nil {
		t.Fatalf("保存配置 B 失败：%v", err)
	}

	// B 是最后保存并激活的，应该是当前生效的那份；A 应该被自动置为不生效。
	active, err := store.ActiveAgentProfile(ctx, u.ID)
	if err != nil {
		t.Fatalf("读取当前生效配置失败：%v", err)
	}
	if active.ID != b.ID {
		t.Errorf("当前生效的应该是 B，实际：%+v", active)
	}

	gotA, err := store.GetAgentProfile(ctx, u.ID, a.ID)
	if err != nil {
		t.Fatalf("读取配置 A 失败：%v", err)
	}
	if gotA.IsActive {
		t.Error("保存 B 并激活后，A 应该被切换成不生效")
	}
	if string(gotA.EncryptedAPIKey) != string(a.EncryptedAPIKey) {
		t.Errorf("密文往返不一致：got=%v want=%v", gotA.EncryptedAPIKey, a.EncryptedAPIKey)
	}

	// 切回 A。
	if err := store.ActivateAgentProfile(ctx, u.ID, a.ID); err != nil {
		t.Fatalf("激活配置 A 失败：%v", err)
	}
	active, err = store.ActiveAgentProfile(ctx, u.ID)
	if err != nil || active.ID != a.ID {
		t.Errorf("激活 A 后当前生效的应该是 A，实际：%+v err=%v", active, err)
	}

	all, err := store.ListAgentProfiles(ctx, u.ID)
	if err != nil {
		t.Fatalf("列出全部配置失败：%v", err)
	}
	if len(all) < 2 {
		t.Errorf("至少应包含刚保存的两份配置，实际 %d 份", len(all))
	}

	if err := store.DeleteAgentProfile(ctx, u.ID, b.ID); err != nil {
		t.Fatalf("删除配置 B 失败：%v", err)
	}
	if _, err := store.GetAgentProfile(ctx, u.ID, b.ID); err == nil {
		t.Error("删除后应查不到配置 B")
	}
}

func TestActivateAgentProfileRejectsUnknownID(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "agent-profiles-unknown-id")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	if err := store.ActivateAgentProfile(ctx, u.ID, idgen.NewUUID()); err == nil {
		t.Fatal("激活一个不存在的配置 ID 应该报错")
	}
}

// TestAgentProfileGetActivateDeleteRejectOtherUsersRow 是多用户隔离的核心属性：
// B 用户 Get/Activate/Delete A 用户的行，得到的错误必须跟"这行根本不存在"完全一样——
// 不能让请求方通过错误类型探测出"这行存在，只是不是你的"。
func TestAgentProfileGetActivateDeleteRejectOtherUsersRow(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "agent-profiles-cross-a")
	userB := createTestUserForStorage(ctx, t, store, "agent-profiles-cross-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	a := newProfileForTest(userA.ID, "A 的配置")
	if err := store.SaveAgentProfile(ctx, a, true); err != nil {
		t.Fatalf("保存配置 A 失败：%v", err)
	}

	if _, err := store.GetAgentProfile(ctx, userB.ID, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B 读取 A 的配置应该跟读一个不存在的 ID 一样返回 ErrNotFound，实际：%v", err)
	}
	if err := store.ActivateAgentProfile(ctx, userB.ID, a.ID); err == nil {
		t.Error("B 不应该能激活 A 的配置")
	}
	if err := store.DeleteAgentProfile(ctx, userB.ID, a.ID); err == nil {
		t.Error("B 不应该能删除 A 的配置")
	}

	// 确认 A 的配置真的还在、没有被 B 的失败操作意外改动。
	gotA, err := store.GetAgentProfile(ctx, userA.ID, a.ID)
	if err != nil || !gotA.IsActive {
		t.Errorf("A 的配置应该完好无损地保持生效，实际：%+v err=%v", gotA, err)
	}
}
