//go:build integration

// 需要 docker-compose 起的 Postgres 才能运行：
//
//	docker compose up -d
//	go test -tags=integration ./internal/storage/... -run Strategy -v
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"tradeforge/internal/config"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// TestGetStrategyReadsCreatedUpdatedFromColumnsNotConfigBlob 是真实复现过的 bug 的
// 回归测试：SaveStrategy 传进来的 cfg.CreatedAt/UpdatedAt 是调用方随手带的一份
// 快照（很多调用路径根本没填，是 Go 零值），但 created_at/updated_at 这两列由
// 数据库自己用 now() 维护，是可信的。GetStrategy/ListStrategies 之前只把 State
// 从独立列覆盖回 config，漏了这两个时间字段，导致界面上真实出现过
// "0001-01-01 00:00" 这种从未被写过的零值时间。
func TestGetStrategyReadsCreatedUpdatedFromColumnsNotConfigBlob(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "strategies-created-updated")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	sc := newTestStrategy(t, u.ID)
	// 故意不填 CreatedAt/UpdatedAt，模拟"调用方没带这两个字段"的真实场景
	// （sc.CreatedAt/UpdatedAt 此时是 Go 零值 time.Time{}）。
	before := time.Now().Add(-time.Second)
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("保存策略失败：%v", err)
	}

	got, err := store.GetStrategy(ctx, u.ID, sc.ID)
	if err != nil {
		t.Fatalf("读取策略失败：%v", err)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("CreatedAt/UpdatedAt 不应为零值：%+v", got)
	}
	if got.CreatedAt.Before(before) {
		t.Errorf("CreatedAt = %s，应该晚于测试开始时间 %s（说明用的是数据库的 now()，不是零值）",
			got.CreatedAt, before)
	}

	list, err := store.ListStrategies(ctx, u.ID)
	if err != nil {
		t.Fatalf("列出策略失败：%v", err)
	}
	found := false
	for _, s := range list {
		if s.ID == sc.ID {
			found = true
			if s.CreatedAt.IsZero() {
				t.Errorf("ListStrategies 里这条策略的 CreatedAt 也不应为零值")
			}
		}
	}
	if !found {
		t.Fatal("ListStrategies 没有返回刚保存的策略")
	}
}

func TestDeleteStrategyCascadesToBacktestResults(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "strategies-delete-cascade")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	sc := newTestStrategy(t, u.ID)
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("保存策略失败：%v", err)
	}

	btID := idgen.NewUUID()
	_, err = store.pool.Exec(ctx, `
		INSERT INTO backtest_results (
			id, strategy_id, symbol, overall, in_sample, out_of_sample,
			segments, fee_model, initial_capital, data_start, data_end, engine_version
		) VALUES ($1, $2, $3, '{}', '{}', '{}', '[]', '{}', '1000', now(), now(), 'test')`,
		btID, sc.ID, sc.Symbol)
	if err != nil {
		t.Fatalf("写入测试回测结果失败：%v", err)
	}

	if err := store.DeleteStrategy(ctx, u.ID, sc.ID); err != nil {
		t.Fatalf("删除策略失败：%v", err)
	}

	if _, err := store.GetStrategy(ctx, u.ID, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除后再读取应该返回 ErrNotFound，实际：%v", err)
	}
	if _, err := store.LatestBacktestResult(ctx, u.ID, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除策略应该级联删掉关联的回测结果，实际读到：%v", err)
	}
}

func TestDeleteStrategyErrorsWhenNotFound(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "strategies-delete-not-found")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	if err := store.DeleteStrategy(ctx, u.ID, idgen.NewUUID()); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除不存在的策略应该返回 ErrNotFound，实际：%v", err)
	}
}

// TestSaveStrategyRejectsCrossUserOverwrite 是设计阶段发现的真实漏洞的回归测试：
// SaveStrategy 用 id 做 upsert，如果不校验所有权，B 用户拿着 A 用户已存在的策略 id
// 调用 SaveStrategy 就能悄悄覆盖 A 的策略内容。
func TestSaveStrategyRejectsCrossUserOverwrite(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "strategies-cross-user-a")
	userB := createTestUserForStorage(ctx, t, store, "strategies-cross-user-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	scA := newTestStrategy(t, userA.ID)
	if err := store.SaveStrategy(ctx, scA); err != nil {
		t.Fatalf("A 保存策略失败：%v", err)
	}

	// B 伪造一份携带 A 的策略 id 的配置，尝试用自己的身份覆盖。
	forged := newTestStrategy(t, userB.ID)
	forged.ID = scA.ID
	forged.Name = "被 B 篡改的名字"
	if err := store.SaveStrategy(ctx, forged); !errors.Is(err, ErrNotFound) {
		t.Errorf("B 用自己的 UserID 覆盖 A 的策略 id 应该返回 ErrNotFound，实际：%v", err)
	}

	// A 的策略必须原封不动。
	got, err := store.GetStrategy(ctx, userA.ID, scA.ID)
	if err != nil {
		t.Fatalf("读取 A 的策略失败：%v", err)
	}
	if got.Name != scA.Name {
		t.Errorf("A 的策略名字应保持不变，实际变成了 %q", got.Name)
	}
}

// TestStrategyGetDeleteRejectOtherUsersRow 是多用户隔离的核心属性：B 用户 Get/Delete
// A 用户的策略，得到的错误必须跟"这条策略根本不存在"完全一样。
func TestStrategyGetDeleteRejectOtherUsersRow(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "strategies-get-delete-cross-a")
	userB := createTestUserForStorage(ctx, t, store, "strategies-get-delete-cross-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	sc := newTestStrategy(t, userA.ID)
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("保存策略失败：%v", err)
	}

	if _, err := store.GetStrategy(ctx, userB.ID, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B 读取 A 的策略应该跟读一个不存在的 ID 一样返回 ErrNotFound，实际：%v", err)
	}
	if err := store.DeleteStrategy(ctx, userB.ID, sc.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("B 删除 A 的策略应该返回 ErrNotFound，实际：%v", err)
	}

	if _, err := store.GetStrategy(ctx, userA.ID, sc.ID); err != nil {
		t.Errorf("A 的策略应该完好无损，实际读取失败：%v", err)
	}
}

// TestListStrategiesByStateAllUsersSpansMultipleUsers 验证多用户并发执行改造新增的
// 跨用户查询方法：两个真实用户各自存一条目标状态的策略 + 一条别的状态的诱饵策略，
// 断言跨用户方法能同时返回两个用户的目标状态策略，且状态过滤仍然生效（不会把诱饵
// 策略也带出来）——这是证明 cmd/executor/cmd/signal-engine 的多用户模式能真的看到
// "这个状态下所有用户的策略"的直接证据，不只是"参数传过去了"。
func TestListStrategiesByStateAllUsersSpansMultipleUsers(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "strategies-all-users-a")
	userB := createTestUserForStorage(ctx, t, store, "strategies-all-users-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	scA := newTestStrategy(t, userA.ID)
	scA.State = types.StatePaperTrading
	if err := store.SaveStrategy(ctx, scA); err != nil {
		t.Fatalf("A 保存策略失败：%v", err)
	}

	scB := newTestStrategy(t, userB.ID)
	scB.State = types.StatePaperTrading
	if err := store.SaveStrategy(ctx, scB); err != nil {
		t.Fatalf("B 保存策略失败：%v", err)
	}

	// 诱饵：A 的另一条策略，状态不是 PAPER_TRADING——不该出现在结果里。
	decoy := newTestStrategy(t, userA.ID)
	decoy.State = types.StateDraft
	if err := store.SaveStrategy(ctx, decoy); err != nil {
		t.Fatalf("保存诱饵策略失败：%v", err)
	}

	got, err := store.ListStrategiesByStateAllUsers(ctx, types.StatePaperTrading)
	if err != nil {
		t.Fatalf("跨用户查询失败：%v", err)
	}

	gotIDs := make(map[string]string, len(got)) // id -> user_id
	for _, s := range got {
		gotIDs[s.ID] = s.UserID
	}
	if gotIDs[scA.ID] != userA.ID {
		t.Errorf("应该包含 A 的策略且 UserID 正确，实际：%+v", gotIDs)
	}
	if gotIDs[scB.ID] != userB.ID {
		t.Errorf("应该包含 B 的策略且 UserID 正确，实际：%+v", gotIDs)
	}
	if _, ok := gotIDs[decoy.ID]; ok {
		t.Errorf("状态过滤应该排除掉诱饵策略，实际结果里出现了：%+v", gotIDs)
	}
}

// TestGetStrategyAllUsersIgnoresOwnership 是这个文件里唯一一条"应该不做用户过滤"
// 的测试，跟其它所有测试的方向相反——GetStrategyAllUsers 专供 cmd/notifier 使用，
// 用来把 Kafka 决策里的 StrategyID 反查回归属用户，必须能读到任意用户的策略。
func TestGetStrategyAllUsersIgnoresOwnership(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	userA := createTestUserForStorage(ctx, t, store, "strategies-get-all-users-a")
	userB := createTestUserForStorage(ctx, t, store, "strategies-get-all-users-b")
	defer func() {
		if _, err := store.pool.Exec(context.Background(),
			`DELETE FROM users WHERE id = ANY($1)`, []string{userA.ID, userB.ID}); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	scA := newTestStrategy(t, userA.ID)
	scB := newTestStrategy(t, userB.ID)
	if err := store.SaveStrategy(ctx, scA); err != nil {
		t.Fatalf("保存 A 的策略失败：%v", err)
	}
	if err := store.SaveStrategy(ctx, scB); err != nil {
		t.Fatalf("保存 B 的策略失败：%v", err)
	}

	gotA, err := store.GetStrategyAllUsers(ctx, scA.ID)
	if err != nil || gotA.UserID != userA.ID {
		t.Errorf("应该能不带用户身份读到 A 的策略，实际：%+v err=%v", gotA, err)
	}
	gotB, err := store.GetStrategyAllUsers(ctx, scB.ID)
	if err != nil || gotB.UserID != userB.ID {
		t.Errorf("应该能不带用户身份读到 B 的策略，实际：%+v err=%v", gotB, err)
	}

	if _, err := store.GetStrategyAllUsers(ctx, idgen.NewUUID()); !errors.Is(err, ErrNotFound) {
		t.Errorf("不存在的策略应该返回 ErrNotFound，实际：%v", err)
	}
}
