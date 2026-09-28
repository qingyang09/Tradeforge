//go:build integration

// 需要 docker-compose 起的 Postgres，且已经跑过 migrations/009_notification_channels.sql
// 和 migrations/010_notification_deliveries.sql：
//
//	docker compose up -d
//	go test -tags=integration ./internal/storage/... -run NotificationChannel -v
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradeforge/internal/config"
	"tradeforge/pkg/idgen"
)

func newChannelForTest(userID, kind, label string) NotificationChannel {
	return NotificationChannel{
		ID: idgen.NewUUID(), UserID: userID, Kind: kind, Label: label, KeyHint: "hint",
		EncryptedConfig: []byte{0x01, 0x02, 0x03},
		KeySalt:         []byte{0xaa, 0xbb},
		KeyNonce:        []byte{0xcc, 0xdd, 0xee},
		IsEnabled:       true,
	}
}

// TestNotificationChannelsAllowMultipleEnabledOfSameKind 直接证明这个设计的核心
// 语义：同一个用户、同一个 kind 可以同时有多行 is_enabled=true——跟 broker_profiles
// "同一 broker 最多一行 active" 刻意不同，这里必须直接验证，不能只靠假 store 测试。
func TestNotificationChannelsAllowMultipleEnabledOfSameKind(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败（是否已 docker compose up -d 并跑过 009/010 迁移？）：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "notification-channels-multi-enabled")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	a := newChannelForTest(u.ID, "webhook", "第一个 webhook")
	b := newChannelForTest(u.ID, "webhook", "第二个 webhook")
	if err := store.SaveNotificationChannel(ctx, a); err != nil {
		t.Fatalf("保存渠道 A 失败：%v", err)
	}
	if err := store.SaveNotificationChannel(ctx, b); err != nil {
		t.Fatalf("保存渠道 B 失败：%v", err)
	}

	list, err := store.ListNotificationChannels(ctx, u.ID)
	if err != nil {
		t.Fatalf("列出渠道失败：%v", err)
	}
	if len(list) != 2 {
		t.Fatalf("应该有两条渠道，实际 %d 条", len(list))
	}
	enabledCount := 0
	for _, c := range list {
		if c.IsEnabled {
			enabledCount++
		}
	}
	if enabledCount != 2 {
		t.Errorf("两条同 kind 的渠道应该都保持 is_enabled=true，实际只有 %d 条启用", enabledCount)
	}
}

func TestNotificationChannelToggleAndDelete(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "notification-channels-toggle-delete")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	ch := newChannelForTest(u.ID, "email", "我的邮箱")
	if err := store.SaveNotificationChannel(ctx, ch); err != nil {
		t.Fatalf("保存渠道失败：%v", err)
	}

	if err := store.SetNotificationChannelEnabled(ctx, u.ID, ch.ID, false); err != nil {
		t.Fatalf("停用渠道失败：%v", err)
	}
	got, err := store.GetNotificationChannel(ctx, u.ID, ch.ID)
	if err != nil || got.IsEnabled {
		t.Errorf("停用后应该 is_enabled=false，实际：%+v err=%v", got, err)
	}

	if err := store.SetNotificationChannelEnabled(ctx, u.ID, ch.ID, true); err != nil {
		t.Fatalf("重新启用渠道失败：%v", err)
	}
	got, err = store.GetNotificationChannel(ctx, u.ID, ch.ID)
	if err != nil || !got.IsEnabled {
		t.Errorf("重新启用后应该 is_enabled=true，实际：%+v err=%v", got, err)
	}

	if err := store.DeleteNotificationChannel(ctx, u.ID, ch.ID); err != nil {
		t.Fatalf("删除渠道失败：%v", err)
	}
	if _, err := store.GetNotificationChannel(ctx, u.ID, ch.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除后应该返回 ErrNotFound，实际：%v", err)
	}
}

// TestNotificationChannelGetSetDeleteRejectOtherUsersRow 是多用户隔离的核心属性：
// B 用户 Get/切换/删除 A 用户的渠道，得到的错误必须跟"这行根本不存在"完全一样。
func TestNotificationChannelGetSetDeleteRejectOtherUsersRow(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "notification-channels-cross-a")
	userB := createTestUserForStorage(ctx, t, store, "notification-channels-cross-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	ch := newChannelForTest(userA.ID, "webhook", "A 的 webhook")
	if err := store.SaveNotificationChannel(ctx, ch); err != nil {
		t.Fatalf("保存渠道失败：%v", err)
	}

	if _, err := store.GetNotificationChannel(ctx, userB.ID, ch.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B 读取 A 的渠道应该返回 ErrNotFound，实际：%v", err)
	}
	if err := store.SetNotificationChannelEnabled(ctx, userB.ID, ch.ID, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("B 停用 A 的渠道应该返回 ErrNotFound，实际：%v", err)
	}
	if err := store.DeleteNotificationChannel(ctx, userB.ID, ch.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B 删除 A 的渠道应该返回 ErrNotFound，实际：%v", err)
	}

	got, err := store.GetNotificationChannel(ctx, userA.ID, ch.ID)
	if err != nil || !got.IsEnabled {
		t.Errorf("A 的渠道应该完好无损，实际：%+v err=%v", got, err)
	}
}

// TestAlreadyDeliveredTracksSentButNotFailed 验证幂等检查的三种情况：无记录、
// 只有失败记录、有成功记录——只有成功记录才应该让 AlreadyDelivered 返回 true，
// 失败记录不该阻止重试。
func TestAlreadyDeliveredTracksSentButNotFailed(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "notification-deliveries-idempotent")
	sc := newTestStrategy(t, u.ID)
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("保存策略失败：%v", err)
	}

	// decisions 表有外键约束，直接手工插入一条最小可用的决策行。
	decisionID := idgen.NewUUID()
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO decisions (id, strategy_id, symbol, direction, score, triggered, reason, price, signals, bar_time)
		VALUES ($1, $2, 'BTCUSDT', 'LONG', 0.8, true, '测试', 100, '[]', now())`,
		decisionID, sc.ID); err != nil {
		t.Fatalf("写入测试决策失败：%v", err)
	}

	ch := newChannelForTest(u.ID, "email", "测试渠道")
	if err := store.SaveNotificationChannel(ctx, ch); err != nil {
		t.Fatalf("保存渠道失败：%v", err)
	}

	already, err := store.AlreadyDelivered(ctx, decisionID, ch.ID)
	if err != nil || already {
		t.Fatalf("尚未有任何投递记录时应该返回 false，实际 already=%v err=%v", already, err)
	}

	if err := store.RecordDelivery(ctx, decisionID, ch.ID, "failed", "boom"); err != nil {
		t.Fatalf("记录失败投递失败：%v", err)
	}
	already, err = store.AlreadyDelivered(ctx, decisionID, ch.ID)
	if err != nil || already {
		t.Fatalf("只有失败记录时应该仍然返回 false（允许重试），实际 already=%v err=%v", already, err)
	}

	if err := store.RecordDelivery(ctx, decisionID, ch.ID, "sent", ""); err != nil {
		t.Fatalf("记录成功投递失败：%v", err)
	}
	already, err = store.AlreadyDelivered(ctx, decisionID, ch.ID)
	if err != nil || !already {
		t.Fatalf("有成功记录后应该返回 true，实际 already=%v err=%v", already, err)
	}
}
