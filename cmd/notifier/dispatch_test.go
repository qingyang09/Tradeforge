package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/notify"
	"tradeforge/internal/secretcrypto"
	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const testMasterKey = "dispatch-test-master-key-32-bytes-plus!"

// fakeNotifierStore 是 notifierStore 的内存实现，让 handleDecision/dispatchToChannel
// 的核心逻辑不需要真实 Postgres 就能测试。
type fakeNotifierStore struct {
	mu sync.Mutex

	strategies map[string]types.StrategyConfig // strategyID -> config
	channels   map[string][]storage.NotificationChannel // userID -> channels
	delivered  map[string]bool                          // decisionID+"|"+channelID -> sent

	deletedChannels []string
	deliveries      []recordedDelivery
}

type recordedDelivery struct {
	decisionID, channelID, status, errMsg string
}

func newFakeNotifierStore() *fakeNotifierStore {
	return &fakeNotifierStore{
		strategies: map[string]types.StrategyConfig{},
		channels:   map[string][]storage.NotificationChannel{},
		delivered:  map[string]bool{},
	}
}

func (f *fakeNotifierStore) GetStrategyAllUsers(_ context.Context, id string) (types.StrategyConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sc, ok := f.strategies[id]
	if !ok {
		return types.StrategyConfig{}, fmt.Errorf("策略 %s：%w", id, storage.ErrNotFound)
	}
	return sc, nil
}

func (f *fakeNotifierStore) ListNotificationChannels(_ context.Context, userID string) ([]storage.NotificationChannel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.channels[userID], nil
}

func (f *fakeNotifierStore) AlreadyDelivered(_ context.Context, decisionID, channelID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.delivered[decisionID+"|"+channelID], nil
}

func (f *fakeNotifierStore) RecordDelivery(_ context.Context, decisionID, channelID, status, errMsg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deliveries = append(f.deliveries, recordedDelivery{decisionID, channelID, status, errMsg})
	if status == "sent" {
		f.delivered[decisionID+"|"+channelID] = true
	}
	return nil
}

func (f *fakeNotifierStore) DeleteNotificationChannel(_ context.Context, _ string, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedChannels = append(f.deletedChannels, id)
	return nil
}

// ---- 假渠道发送器 ----

type fakeEmailSender struct {
	mu    sync.Mutex
	sent  []string // to
	err   error
}

func (f *fakeEmailSender) SendEmail(_ context.Context, to, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, to)
	return nil
}

func (f *fakeEmailSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

type fakeTelegramSender struct {
	mu   sync.Mutex
	sent []string
	err  error
}

func (f *fakeTelegramSender) SendMessage(_ context.Context, chatID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, chatID)
	return nil
}

func (f *fakeTelegramSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

type fakeWebhookSender struct {
	mu   sync.Mutex
	sent []string
	err  error
}

func (f *fakeWebhookSender) SendWebhook(_ context.Context, url, _ string, _ notify.WebhookPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, url)
	return nil
}

func (f *fakeWebhookSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

type fakeWebPushSender struct {
	mu   sync.Mutex
	sent int
	err  error
}

func (f *fakeWebPushSender) SendPush(_ context.Context, _ notify.PushSubscription, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent++
	return nil
}

// encryptedChannel 构造一条测试用的、真实加密过的渠道配置。
func encryptedChannel(t *testing.T, userID, kind, label, plaintext string) storage.NotificationChannel {
	t.Helper()
	ciphertext, salt, nonce, err := secretcrypto.Encrypt(testMasterKey, plaintext)
	if err != nil {
		t.Fatalf("加密测试数据失败：%v", err)
	}
	return storage.NotificationChannel{
		ID: idgen.NewUUID(), UserID: userID, Kind: kind, Label: label, KeyHint: "hint",
		EncryptedConfig: ciphertext, KeySalt: salt, KeyNonce: nonce, IsEnabled: true,
	}
}

func testSenders(email notify.EmailSender, telegram notify.TelegramSender, webhook notify.WebhookSender, webpush notify.WebPushSender) channelSenders {
	return channelSenders{
		masterKey: testMasterKey, logger: quietLogger(),
		email: email, telegram: telegram, webhook: webhook, webpush: webpush,
	}
}

func testDecision(strategyID string, triggered bool) types.Decision {
	return types.Decision{
		ID: idgen.NewUUID(), StrategyID: strategyID, Symbol: "BTCUSDT",
		Direction: types.DirectionLong, Score: 0.8, Triggered: triggered,
		Reason: "测试原因", Price: decimal.NewFromInt(50000), Timestamp: time.Now(),
	}
}

func testStrategyConfig(id, userID string, state types.StrategyState) types.StrategyConfig {
	return types.StrategyConfig{ID: id, UserID: userID, Name: "测试策略", Symbol: "BTCUSDT", State: state}
}

func TestHandleDecisionSkipsUntriggered(t *testing.T) {
	store := newFakeNotifierStore()
	email := &fakeEmailSender{}
	senders := testSenders(email, &fakeTelegramSender{}, &fakeWebhookSender{}, &fakeWebPushSender{})

	sc := testStrategyConfig("s1", "u1", types.StateLive)
	store.strategies["s1"] = sc
	store.channels["u1"] = []storage.NotificationChannel{encryptedChannel(t, "u1", "email", "我的邮箱", `{"address":"a@test.com"}`)}

	handleDecision(context.Background(), store, senders, "", testDecision("s1", false), quietLogger())

	if email.count() != 0 {
		t.Errorf("未触发的决策不应该发送任何提醒，实际发送了 %d 次", email.count())
	}
}

func TestHandleDecisionSkipsUnknownStrategy(t *testing.T) {
	store := newFakeNotifierStore()
	email := &fakeEmailSender{}
	senders := testSenders(email, &fakeTelegramSender{}, &fakeWebhookSender{}, &fakeWebPushSender{})

	handleDecision(context.Background(), store, senders, "", testDecision("does-not-exist", true), quietLogger())

	if email.count() != 0 {
		t.Errorf("查不到策略时不应该发送任何提醒，实际发送了 %d 次", email.count())
	}
}

func TestHandleDecisionSkipsNonAlertingStates(t *testing.T) {
	allStates := []types.StrategyState{
		types.StateDraft, types.StateBacktested, types.StatePaperTrading,
		types.StateLiveEligible, types.StateLive, types.StateSuspended,
	}
	alertingStates := map[types.StrategyState]bool{
		types.StatePaperTrading: true,
		types.StateLive:         true,
	}

	for _, state := range allStates {
		t.Run(string(state), func(t *testing.T) {
			store := newFakeNotifierStore()
			email := &fakeEmailSender{}
			senders := testSenders(email, &fakeTelegramSender{}, &fakeWebhookSender{}, &fakeWebPushSender{})

			store.strategies["s1"] = testStrategyConfig("s1", "u1", state)
			store.channels["u1"] = []storage.NotificationChannel{encryptedChannel(t, "u1", "email", "邮箱", `{"address":"a@test.com"}`)}

			handleDecision(context.Background(), store, senders, "", testDecision("s1", true), quietLogger())

			wantSent := alertingStates[state]
			gotSent := email.count() > 0
			if gotSent != wantSent {
				t.Errorf("状态 %s：应该提醒=%v，实际发送了=%v", state, wantSent, gotSent)
			}
		})
	}
}

func TestHandleDecisionSendsToAllEnabledChannels(t *testing.T) {
	store := newFakeNotifierStore()
	email := &fakeEmailSender{}
	telegram := &fakeTelegramSender{}
	webhook := &fakeWebhookSender{}
	senders := testSenders(email, telegram, webhook, &fakeWebPushSender{})

	store.strategies["s1"] = testStrategyConfig("s1", "u1", types.StateLive)
	disabled := encryptedChannel(t, "u1", "telegram", "已停用", `{"chat_id":"123"}`)
	disabled.IsEnabled = false
	store.channels["u1"] = []storage.NotificationChannel{
		encryptedChannel(t, "u1", "email", "邮箱", `{"address":"a@test.com"}`),
		encryptedChannel(t, "u1", "webhook", "webhook", `{"url":"https://example.com/hook"}`),
		disabled,
	}

	handleDecision(context.Background(), store, senders, "", testDecision("s1", true), quietLogger())

	if email.count() != 1 {
		t.Errorf("邮件应该发送 1 次，实际 %d 次", email.count())
	}
	if webhook.count() != 1 {
		t.Errorf("webhook 应该发送 1 次，实际 %d 次", webhook.count())
	}
	if telegram.count() != 0 {
		t.Errorf("已停用的渠道不应该发送，实际发送了 %d 次", telegram.count())
	}
}

func TestHandleDecisionIsolatesChannelFailures(t *testing.T) {
	store := newFakeNotifierStore()
	email := &fakeEmailSender{}
	webhook := &fakeWebhookSender{}
	telegram := &fakeTelegramSender{err: errors.New("boom")}
	senders := testSenders(email, telegram, webhook, &fakeWebPushSender{})

	store.strategies["s1"] = testStrategyConfig("s1", "u1", types.StateLive)
	store.channels["u1"] = []storage.NotificationChannel{
		encryptedChannel(t, "u1", "email", "邮箱", `{"address":"a@test.com"}`),
		encryptedChannel(t, "u1", "telegram", "telegram", `{"chat_id":"123"}`),
		encryptedChannel(t, "u1", "webhook", "webhook", `{"url":"https://example.com/hook"}`),
	}

	handleDecision(context.Background(), store, senders, "", testDecision("s1", true), quietLogger())

	if email.count() != 1 || webhook.count() != 1 {
		t.Errorf("Telegram 发送失败不应该影响邮件/webhook，实际 email=%d webhook=%d", email.count(), webhook.count())
	}

	sentCount, failedCount := 0, 0
	for _, d := range store.deliveries {
		switch d.status {
		case "sent":
			sentCount++
		case "failed":
			failedCount++
		}
	}
	if sentCount != 2 || failedCount != 1 {
		t.Errorf("投递审计应该是 2 条成功 1 条失败，实际 sent=%d failed=%d", sentCount, failedCount)
	}
}

func TestHandleDecisionSkipsAlreadyDelivered(t *testing.T) {
	store := newFakeNotifierStore()
	email := &fakeEmailSender{}
	senders := testSenders(email, &fakeTelegramSender{}, &fakeWebhookSender{}, &fakeWebPushSender{})

	store.strategies["s1"] = testStrategyConfig("s1", "u1", types.StateLive)
	ch := encryptedChannel(t, "u1", "email", "邮箱", `{"address":"a@test.com"}`)
	store.channels["u1"] = []storage.NotificationChannel{ch}

	d := testDecision("s1", true)
	store.delivered[d.ID+"|"+ch.ID] = true

	handleDecision(context.Background(), store, senders, "", d, quietLogger())

	if email.count() != 0 {
		t.Errorf("已经投递过的决策+渠道组合不应该重新发送，实际发送了 %d 次", email.count())
	}
}

func TestDispatchCleansUpGoneSubscription(t *testing.T) {
	store := newFakeNotifierStore()
	webpush := &fakeWebPushSender{err: notify.ErrSubscriptionGone}
	senders := testSenders(&fakeEmailSender{}, &fakeTelegramSender{}, &fakeWebhookSender{}, webpush)

	store.strategies["s1"] = testStrategyConfig("s1", "u1", types.StateLive)
	ch := encryptedChannel(t, "u1", "webpush", "此设备", `{"endpoint":"https://push.example.com/x","p256dh":"k","auth":"a"}`)
	store.channels["u1"] = []storage.NotificationChannel{ch}

	handleDecision(context.Background(), store, senders, "", testDecision("s1", true), quietLogger())

	if len(store.deletedChannels) != 1 || store.deletedChannels[0] != ch.ID {
		t.Errorf("订阅失效应该触发删除这条渠道，实际删除记录：%v", store.deletedChannels)
	}
}

func TestHandleDecisionSingleOwnerModeFiltersOtherUsers(t *testing.T) {
	store := newFakeNotifierStore()
	email := &fakeEmailSender{}
	senders := testSenders(email, &fakeTelegramSender{}, &fakeWebhookSender{}, &fakeWebPushSender{})

	store.strategies["s1"] = testStrategyConfig("s1", "userB", types.StateLive)
	store.channels["userB"] = []storage.NotificationChannel{encryptedChannel(t, "userB", "email", "邮箱", `{"address":"b@test.com"}`)}

	// 单用户模式：只服务 userA，但这条决策属于 userB。
	handleDecision(context.Background(), store, senders, "userA", testDecision("s1", true), quietLogger())

	if email.count() != 0 {
		t.Errorf("单用户模式下不该给别的用户发提醒，实际发送了 %d 次", email.count())
	}
}

// consume() 本身没有单测——它的签名直接依赖具体类型 *messaging.DecisionReader
// （不是接口），跟 cmd/executor/main.go 里同名同结构的 consume() 是同一个情况，
// 那边同样没有单测：要测的话需要一个真实 Kafka 连接或者把 DecisionReader 改成接口，
// 后者是个不小的改动，为了测这个直接照抄过来、逻辑本身极简单（读-判断-退避-分发）
// 的循环不值得。handleDecision 本身的行为已经被上面这些测试充分覆盖。
