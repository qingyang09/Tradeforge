//go:build integration

// 需要 docker-compose 起的 Postgres 与 Kafka 才能运行：
//
//	docker compose up -d
//	go test -tags=integration ./internal/engine/... -run Live -v
package engine

import (
	"context"
	"testing"
	"time"

	"tradeforge/internal/config"
	"tradeforge/internal/messaging"
	"tradeforge/internal/modules"
	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// createEngineTestUser 插入一个专用于本文件集成测试的用户行——strategies.user_id
// 外键要求真的存在一行 users，不能直接塞一个随便编的 UUID（见
// internal/storage 下同名模式的集成测试）。
func createEngineTestUser(ctx context.Context, t *testing.T, store *storage.Store, label string) storage.User {
	t.Helper()
	u := storage.User{
		ID: idgen.NewUUID(), Email: label + "-" + idgen.NewUUID() + "@engine.test",
		PasswordHash: []byte("x"),
	}
	if err := store.CreateUser(ctx, u); err != nil {
		t.Fatalf("创建测试用户失败：%v", err)
	}
	return u
}

// 真链路：三个真实模块 → 组合引擎 → Postgres 审计 + Kafka 发布 → 消费回来核对。
func TestLiveEndToEndThroughKafkaAndPostgres(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败（是否已 docker compose up -d？）：%v", err)
	}
	defer store.Close()

	u := createEngineTestUser(ctx, t, store, "live-e2e")
	defer func() {
		if _, err := store.Pool().Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	if err := messaging.EnsureTopics(ctx, cfg.Kafka); err != nil {
		t.Fatalf("创建 Kafka topic 失败（是否已 docker compose up -d？）：%v", err)
	}
	pub := messaging.NewKafkaPublisher(cfg.Kafka)
	defer pub.Close()

	// 决策表有外键指向 strategies，必须先把策略本体落库。
	sc := realStrategy(types.CombineAll, 0)
	sc.ID = newUUID()
	sc.UserID = u.ID
	sc.SourceUtterance = "集成测试用策略"
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("保存策略失败：%v", err)
	}

	// 先建好消费者再发布，避免消息早于订阅落地而读不到。
	reader := messaging.NewDecisionReader(cfg.Kafka, "tradeforge-test-"+sc.ID)
	defer reader.Close()

	e := New(modules.NewDefaultRegistry(),
		WithAuditor(store), WithPublisher(pub), WithLogger(quietLogger()))

	decision, err := e.Process(ctx, sc, feedsFor(bullishBreakoutData()))
	if err != nil {
		t.Fatalf("Process 失败：%v", err)
	}
	if !decision.Triggered {
		t.Fatalf("期望触发，实际未触发：%s", decision.Reason)
	}
	t.Logf("已发布决策 %s：%s", decision.ID, decision.Reason)

	// 1) 审计落库核对
	stored, err := store.ListDecisions(ctx, u.ID, sc.ID, 10)
	if err != nil {
		t.Fatalf("读取决策审计失败：%v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("审计表中有 %d 条记录，期望 1 条", len(stored))
	}
	got := stored[0]
	if got.ID != decision.ID {
		t.Errorf("审计记录 ID = %s，期望 %s", got.ID, decision.ID)
	}
	if got.Direction != decision.Direction || got.Triggered != decision.Triggered {
		t.Errorf("审计记录与内存决策不一致：%+v vs %+v", got, decision)
	}
	if !got.Price.Equal(decision.Price) {
		t.Errorf("落库后价格 = %s，期望 %s（NUMERIC 往返不得丢精度）", got.Price, decision.Price)
	}
	if len(got.Signals) != 3 {
		t.Errorf("审计记录中信号数 = %d，期望 3", len(got.Signals))
	}

	// 2) Kafka 消费核对
	//
	// decisions topic 是跨测试运行共享、有留存期的：一个全新的消费者组在没有提交过
	// offset 时默认从最早的消息开始读，topic 里之前任何一次运行留下的旧决策都会先于
	// 本次发布的这条被读到。逐条跳过不匹配的消息，而不是假设第一条读到的就是它。
	readCtx, readCancel := context.WithTimeout(ctx, 60*time.Second)
	defer readCancel()
	var consumed types.Decision
	for {
		consumed, err = reader.Read(readCtx)
		if err != nil {
			t.Fatalf("从 Kafka 读取决策失败：%v", err)
		}
		if consumed.ID == decision.ID {
			break
		}
		t.Logf("跳过历史遗留消息 %s（非本次测试发布）", consumed.ID)
	}
	if consumed.Direction != decision.Direction {
		t.Errorf("消费到的方向 = %s，期望 %s", consumed.Direction, decision.Direction)
	}
	if len(consumed.Signals) != 3 {
		t.Errorf("消费到的信号数 = %d，期望 3（可解释性信息必须完整穿过消息队列）", len(consumed.Signals))
	}
	t.Logf("已从 Kafka 消费回决策 %s，信号 %d 条", consumed.ID, len(consumed.Signals))
}

// 状态流转与策略读写的真库往返。
func TestLiveStrategyRoundTrip(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	u := createEngineTestUser(ctx, t, store, "live-roundtrip")
	defer func() {
		if _, err := store.Pool().Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	sc := realStrategy(types.CombineWeighted, 0.6)
	sc.ID = newUUID()
	sc.UserID = u.ID
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("保存策略失败：%v", err)
	}

	loaded, err := store.GetStrategy(ctx, u.ID, sc.ID)
	if err != nil {
		t.Fatalf("读取策略失败：%v", err)
	}
	if loaded.Symbol != sc.Symbol || len(loaded.Modules) != len(sc.Modules) {
		t.Errorf("往返后配置不一致：%+v", loaded)
	}
	if !loaded.Risk.MaxPositionSizeQuote.Equal(sc.Risk.MaxPositionSizeQuote) {
		t.Errorf("风控金额往返后 = %s，期望 %s",
			loaded.Risk.MaxPositionSizeQuote, sc.Risk.MaxPositionSizeQuote)
	}
	if loaded.State != types.StateDraft {
		t.Errorf("新策略状态 = %s，期望 DRAFT", loaded.State)
	}

	// 状态推进 + 审计必须同事务生效。
	if err := store.UpdateStrategyState(ctx, u.ID, storage.Transition{
		StrategyID: sc.ID, From: types.StateDraft, To: types.StateBacktested,
		Actor: "system:test", Reason: "集成测试推进",
		Evidence: map[string]any{"out_of_sample_sharpe": 1.2},
	}); err != nil {
		t.Fatalf("状态推进失败：%v", err)
	}

	after, err := store.GetStrategy(ctx, u.ID, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != types.StateBacktested {
		t.Errorf("推进后状态 = %s，期望 BACKTESTED", after.State)
	}

	// 乐观锁：from_state 已经不匹配，重复推进必须被拒绝。
	err = store.UpdateStrategyState(ctx, u.ID, storage.Transition{
		StrategyID: sc.ID, From: types.StateDraft, To: types.StateBacktested,
		Actor: "system:test", Reason: "重复推进",
	})
	if err == nil {
		t.Error("from_state 不匹配时应拒绝推进（乐观锁失效）")
	}

	trans, err := store.ListTransitions(ctx, u.ID, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trans) != 1 {
		t.Fatalf("审计记录 %d 条，期望 1 条（被拒绝的推进不该留痕）", len(trans))
	}
	if trans[0].Evidence["out_of_sample_sharpe"] == nil {
		t.Error("流转依据未落库，事后无法回答'凭什么推进'")
	}
}
