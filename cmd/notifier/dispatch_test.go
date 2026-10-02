package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
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

// fakeNotifierStore is an in-memory implementation of notifierStore, so
// handleDecision/dispatchToChannel's core logic can be tested without a
// real Postgres.
type fakeNotifierStore struct {
	mu sync.Mutex

	strategies map[string]types.StrategyConfig          // strategyID -> config
	channels   map[string][]storage.NotificationChannel // userID -> channels
	delivered  map[string]bool                          // decisionID+"|"+channelID -> sent
	users      map[string]storage.User                  // userID -> account

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
		users:      map[string]storage.User{},
	}
}

func (f *fakeNotifierStore) GetStrategyAllUsers(_ context.Context, id string) (types.StrategyConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sc, ok := f.strategies[id]
	if !ok {
		return types.StrategyConfig{}, fmt.Errorf("strategy %s: %w", id, storage.ErrNotFound)
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

func (f *fakeNotifierStore) GetUser(_ context.Context, id string) (storage.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return storage.User{}, fmt.Errorf("account %s: %w", id, storage.ErrNotFound)
	}
	return u, nil
}

func (f *fakeNotifierStore) DeleteNotificationChannel(_ context.Context, _ string, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedChannels = append(f.deletedChannels, id)
	return nil
}

// ---- fake channel senders ----

type fakeEmailSender struct {
	mu   sync.Mutex
	sent []string // to
	err  error
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
	mu       sync.Mutex
	sent     []string
	payloads []notify.WebhookPayload
	err      error
}

func (f *fakeWebhookSender) SendWebhook(_ context.Context, url, _ string, payload notify.WebhookPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, url)
	f.payloads = append(f.payloads, payload)
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

// encryptedChannel builds a test channel config that's actually encrypted.
func encryptedChannel(t *testing.T, userID, kind, label, plaintext string) storage.NotificationChannel {
	t.Helper()
	ciphertext, salt, nonce, err := secretcrypto.Encrypt(testMasterKey, plaintext)
	if err != nil {
		t.Fatalf("failed to encrypt test data: %v", err)
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
		Reason: types.Message{Literal: "test reason"}, Price: decimal.NewFromInt(50000), Timestamp: time.Now(),
	}
}

func testStrategyConfig(id, userID string, state types.StrategyState) types.StrategyConfig {
	return types.StrategyConfig{ID: id, UserID: userID, Name: "test strategy", Symbol: "BTCUSDT", State: state}
}

func TestHandleDecisionSkipsUntriggered(t *testing.T) {
	store := newFakeNotifierStore()
	email := &fakeEmailSender{}
	senders := testSenders(email, &fakeTelegramSender{}, &fakeWebhookSender{}, &fakeWebPushSender{})

	sc := testStrategyConfig("s1", "u1", types.StateLive)
	store.strategies["s1"] = sc
	store.channels["u1"] = []storage.NotificationChannel{encryptedChannel(t, "u1", "email", "my email", `{"address":"a@test.com"}`)}

	handleDecision(context.Background(), store, senders, "", testDecision("s1", false), quietLogger())

	if email.count() != 0 {
		t.Errorf("a non-triggered decision should not send any alert, but sent %d times", email.count())
	}
}

func TestHandleDecisionSkipsUnknownStrategy(t *testing.T) {
	store := newFakeNotifierStore()
	email := &fakeEmailSender{}
	senders := testSenders(email, &fakeTelegramSender{}, &fakeWebhookSender{}, &fakeWebPushSender{})

	handleDecision(context.Background(), store, senders, "", testDecision("does-not-exist", true), quietLogger())

	if email.count() != 0 {
		t.Errorf("should not send any alert when the strategy can't be found, but sent %d times", email.count())
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
			store.channels["u1"] = []storage.NotificationChannel{encryptedChannel(t, "u1", "email", "email", `{"address":"a@test.com"}`)}

			handleDecision(context.Background(), store, senders, "", testDecision("s1", true), quietLogger())

			wantSent := alertingStates[state]
			gotSent := email.count() > 0
			if gotSent != wantSent {
				t.Errorf("state %s: want alert=%v, got sent=%v", state, wantSent, gotSent)
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
	disabled := encryptedChannel(t, "u1", "telegram", "disabled", `{"chat_id":"123"}`)
	disabled.IsEnabled = false
	store.channels["u1"] = []storage.NotificationChannel{
		encryptedChannel(t, "u1", "email", "email", `{"address":"a@test.com"}`),
		encryptedChannel(t, "u1", "webhook", "webhook", `{"url":"https://example.com/hook"}`),
		disabled,
	}

	handleDecision(context.Background(), store, senders, "", testDecision("s1", true), quietLogger())

	if email.count() != 1 {
		t.Errorf("email should be sent once, got %d", email.count())
	}
	if webhook.count() != 1 {
		t.Errorf("webhook should be sent once, got %d", webhook.count())
	}
	if telegram.count() != 0 {
		t.Errorf("a disabled channel should not send, but sent %d times", telegram.count())
	}
}

// TestHandleDecisionUsesRecipientPreferredLanguage confirms a strategy
// owner's stored preferred_lang (migrations/011_users_preferred_lang.sql)
// actually drives which language handleDecision builds the alert in -- a
// background process like cmd/notifier has no per-request cookie to resolve
// a language from, so this stored account preference is the only signal it
// has.
func TestHandleDecisionUsesRecipientPreferredLanguage(t *testing.T) {
	store := newFakeNotifierStore()
	webhook := &fakeWebhookSender{}
	senders := testSenders(&fakeEmailSender{}, &fakeTelegramSender{}, webhook, &fakeWebPushSender{})

	store.strategies["s1"] = testStrategyConfig("s1", "u1", types.StateLive)
	store.users["u1"] = storage.User{ID: "u1", PreferredLang: "en"}
	store.channels["u1"] = []storage.NotificationChannel{
		encryptedChannel(t, "u1", "webhook", "webhook", `{"url":"https://example.com/hook"}`),
	}

	handleDecision(context.Background(), store, senders, "", testDecision("s1", true), quietLogger())

	if len(webhook.payloads) != 1 {
		t.Fatalf("expected exactly one webhook payload, got %d", len(webhook.payloads))
	}
	if !strings.Contains(webhook.payloads[0].Title, "[Live]") {
		t.Errorf("user u1 prefers English, expected the title to contain \"[Live]\", got %q", webhook.payloads[0].Title)
	}
}

// TestHandleDecisionDefaultsLanguageForUnknownUser confirms a failed
// preferred-language lookup (no stored account, e.g. a user record that's
// gone missing) falls back to i18n.DefaultLang rather than dropping the
// alert or erroring out -- same "degrade, don't fail" principle as every
// other isolation point in this file.
func TestHandleDecisionDefaultsLanguageForUnknownUser(t *testing.T) {
	store := newFakeNotifierStore()
	webhook := &fakeWebhookSender{}
	senders := testSenders(&fakeEmailSender{}, &fakeTelegramSender{}, webhook, &fakeWebPushSender{})

	store.strategies["s1"] = testStrategyConfig("s1", "u1", types.StateLive)
	// Deliberately no store.users["u1"] entry.
	store.channels["u1"] = []storage.NotificationChannel{
		encryptedChannel(t, "u1", "webhook", "webhook", `{"url":"https://example.com/hook"}`),
	}

	handleDecision(context.Background(), store, senders, "", testDecision("s1", true), quietLogger())

	if len(webhook.payloads) != 1 {
		t.Fatalf("expected exactly one webhook payload, got %d", len(webhook.payloads))
	}
	if !strings.Contains(webhook.payloads[0].Title, "[实盘]") {
		t.Errorf("an unknown user should fall back to i18n.DefaultLang (zh), expected the title to contain \"[实盘]\", got %q", webhook.payloads[0].Title)
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
		encryptedChannel(t, "u1", "email", "email", `{"address":"a@test.com"}`),
		encryptedChannel(t, "u1", "telegram", "telegram", `{"chat_id":"123"}`),
		encryptedChannel(t, "u1", "webhook", "webhook", `{"url":"https://example.com/hook"}`),
	}

	handleDecision(context.Background(), store, senders, "", testDecision("s1", true), quietLogger())

	if email.count() != 1 || webhook.count() != 1 {
		t.Errorf("a Telegram send failure should not affect email/webhook, got email=%d webhook=%d", email.count(), webhook.count())
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
		t.Errorf("delivery audit should show 2 sent and 1 failed, got sent=%d failed=%d", sentCount, failedCount)
	}
}

func TestHandleDecisionSkipsAlreadyDelivered(t *testing.T) {
	store := newFakeNotifierStore()
	email := &fakeEmailSender{}
	senders := testSenders(email, &fakeTelegramSender{}, &fakeWebhookSender{}, &fakeWebPushSender{})

	store.strategies["s1"] = testStrategyConfig("s1", "u1", types.StateLive)
	ch := encryptedChannel(t, "u1", "email", "email", `{"address":"a@test.com"}`)
	store.channels["u1"] = []storage.NotificationChannel{ch}

	d := testDecision("s1", true)
	store.delivered[d.ID+"|"+ch.ID] = true

	handleDecision(context.Background(), store, senders, "", d, quietLogger())

	if email.count() != 0 {
		t.Errorf("an already-delivered decision+channel pair should not resend, but sent %d times", email.count())
	}
}

func TestDispatchCleansUpGoneSubscription(t *testing.T) {
	store := newFakeNotifierStore()
	webpush := &fakeWebPushSender{err: notify.ErrSubscriptionGone}
	senders := testSenders(&fakeEmailSender{}, &fakeTelegramSender{}, &fakeWebhookSender{}, webpush)

	store.strategies["s1"] = testStrategyConfig("s1", "u1", types.StateLive)
	ch := encryptedChannel(t, "u1", "webpush", "this device", `{"endpoint":"https://push.example.com/x","p256dh":"k","auth":"a"}`)
	store.channels["u1"] = []storage.NotificationChannel{ch}

	handleDecision(context.Background(), store, senders, "", testDecision("s1", true), quietLogger())

	if len(store.deletedChannels) != 1 || store.deletedChannels[0] != ch.ID {
		t.Errorf("a dead subscription should trigger deletion of this channel, got deleted: %v", store.deletedChannels)
	}
}

func TestHandleDecisionSingleOwnerModeFiltersOtherUsers(t *testing.T) {
	store := newFakeNotifierStore()
	email := &fakeEmailSender{}
	senders := testSenders(email, &fakeTelegramSender{}, &fakeWebhookSender{}, &fakeWebPushSender{})

	store.strategies["s1"] = testStrategyConfig("s1", "userB", types.StateLive)
	store.channels["userB"] = []storage.NotificationChannel{encryptedChannel(t, "userB", "email", "email", `{"address":"b@test.com"}`)}

	// Single-tenant mode: only serves userA, but this decision belongs to userB.
	handleDecision(context.Background(), store, senders, "userA", testDecision("s1", true), quietLogger())

	if email.count() != 0 {
		t.Errorf("single-tenant mode should not alert another user, but sent %d times", email.count())
	}
}

// consume() itself has no unit test — its signature directly depends on the
// concrete type *messaging.DecisionReader (not an interface), the same
// situation as the identically-named, identically-structured consume() in
// cmd/executor/main.go, which also has no unit test: testing it would need
// either a real Kafka connection or turning DecisionReader into an
// interface, and the latter is a non-trivial change not worth making just to
// test a loop this simple (read-check-backoff-dispatch), copied verbatim
// from there. handleDecision's own behavior is already thoroughly covered
// by the tests above.
