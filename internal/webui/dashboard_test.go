package webui

import (
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestServer(t *testing.T, store *fakeStore) *Server {
	t.Helper()
	srv, err := New(store, nil, strategy.DefaultGate(), quietLogger())
	if err != nil {
		t.Fatalf("New() 失败：%v", err)
	}
	srv.SetMasterKey(testMasterKey)
	loginTestUser(store, srv)
	return srv
}

func riskConfig() types.RiskConfig {
	return types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)}
}

func TestHandleDashboardGroupsStrategiesByState(t *testing.T) {
	store := newFakeStore()
	store.strategies["s1"] = types.StrategyConfig{
		ID: "s1", UserID: testDefaultUserID, Name: "策略一", Symbol: "BTCUSDT", Timeframe: types.TF1h,
		Combine: types.CombineAll, Risk: riskConfig(), State: types.StateDraft,
		UpdatedAt: time.Now(),
	}
	store.strategies["s2"] = types.StrategyConfig{
		ID: "s2", UserID: testDefaultUserID, Name: "策略二", Symbol: "ETHUSDT", Timeframe: types.TF1h,
		Combine: types.CombineAll, Risk: riskConfig(), State: types.StateLive,
		UpdatedAt: time.Now(),
	}

	srv := newTestServer(t, store)
	w := getPage(srv, "/")

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200，响应体：%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "策略一") || !strings.Contains(body, "策略二") {
		t.Errorf("看板应展示两个策略，实际响应：%s", body)
	}
	if !strings.Contains(body, "DRAFT") || !strings.Contains(body, "LIVE") {
		t.Errorf("看板应展示状态标签，实际响应：%s", body)
	}
}

func TestHandleDashboardWithNoStrategies(t *testing.T) {
	srv := newTestServer(t, newFakeStore())
	w := getPage(srv, "/")

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "还没有任何策略") {
		t.Errorf("空看板应提示新建，实际响应：%s", w.Body.String())
	}
}

// ---------- 批量删除：看板上勾选多条 DRAFT 策略一次删掉 ----------

func TestHandleBulkDeleteStrategiesRemovesOnlyDraftOnes(t *testing.T) {
	store := newFakeStore()
	store.strategies["d1"] = types.StrategyConfig{
		ID: "d1", UserID: testDefaultUserID, Name: "草稿一", Symbol: "BTCUSDT", Timeframe: types.TF1h,
		Combine: types.CombineAll, Risk: riskConfig(), State: types.StateDraft,
	}
	store.strategies["d2"] = types.StrategyConfig{
		ID: "d2", UserID: testDefaultUserID, Name: "草稿二", Symbol: "BTCUSDT", Timeframe: types.TF1h,
		Combine: types.CombineAll, Risk: riskConfig(), State: types.StateDraft,
	}
	store.strategies["live1"] = types.StrategyConfig{
		ID: "live1", UserID: testDefaultUserID, Name: "实盘中的策略", Symbol: "BTCUSDT", Timeframe: types.TF1h,
		Combine: types.CombineAll, Risk: riskConfig(), State: types.StateLive,
	}
	srv := newTestServer(t, store)

	// 混一个非 DRAFT 的 id 进去，模拟"绕过界面直接拼参数"的情况——不该被删掉。
	w := postForm(srv, "/strategies/bulk-delete", url.Values{"ids": {"d1", "d2", "live1"}})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "已删除 2 条策略") {
		t.Errorf("banner 应说明删了 2 条、跳过 1 条，实际：%s", w.Body.String())
	}
	if _, ok := store.strategies["d1"]; ok {
		t.Error("d1 应该已被删除")
	}
	if _, ok := store.strategies["d2"]; ok {
		t.Error("d2 应该已被删除")
	}
	if _, ok := store.strategies["live1"]; !ok {
		t.Error("非 DRAFT 状态的 live1 不应被删除")
	}
}

func TestHandleBulkDeleteStrategiesNoSelectionShowsBanner(t *testing.T) {
	srv := newTestServer(t, newFakeStore())
	w := postForm(srv, "/strategies/bulk-delete", url.Values{})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "没有选中任何策略") {
		t.Errorf("应提示没有选中任何策略，实际：%s", w.Body.String())
	}
}

func TestHandleBulkDeleteStrategiesIgnoresUnknownID(t *testing.T) {
	store := newFakeStore()
	store.strategies["d1"] = types.StrategyConfig{
		ID: "d1", UserID: testDefaultUserID, Name: "草稿一", Symbol: "BTCUSDT", Timeframe: types.TF1h,
		Combine: types.CombineAll, Risk: riskConfig(), State: types.StateDraft,
	}
	srv := newTestServer(t, store)

	w := postForm(srv, "/strategies/bulk-delete", url.Values{"ids": {"d1", "does-not-exist"}})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "已删除 1 条策略") {
		t.Errorf("应说明删了 1 条、跳过 1 条不存在的，实际：%s", w.Body.String())
	}
	if _, ok := store.strategies["d1"]; ok {
		t.Error("d1 应该已被删除")
	}
}

// 看板上只有 DRAFT 分组才应该出现勾选框和"删除选中"按钮。
func TestDashboardShowsBulkDeleteOnlyForDraftGroup(t *testing.T) {
	store := newFakeStore()
	store.strategies["d1"] = types.StrategyConfig{
		ID: "d1", UserID: testDefaultUserID, Name: "草稿一", Symbol: "BTCUSDT", Timeframe: types.TF1h,
		Combine: types.CombineAll, Risk: riskConfig(), State: types.StateDraft,
	}
	store.strategies["live1"] = types.StrategyConfig{
		ID: "live1", UserID: testDefaultUserID, Name: "实盘中的策略", Symbol: "BTCUSDT", Timeframe: types.TF1h,
		Combine: types.CombineAll, Risk: riskConfig(), State: types.StateLive,
	}
	srv := newTestServer(t, store)

	body := getPage(srv, "/").Body.String()
	// 注意不要用 strings.Count(body, `name="ids"`) 这种朴素子串计数——"删除选中"
	// 这几个字还出现在 confirm() 弹窗提示文案里（"确定要永久删除选中的策略吗"），
	// JS 里也重复了一遍 name="ids" 选择器字符串，两种朴素计数都会把这些误算进去，
	// 得直接断言具体某个标的的勾选框在不在。
	if !strings.Contains(body, `<input type="checkbox" name="ids" value="d1">`) {
		t.Errorf("DRAFT 策略 d1 那一行应该有勾选框，实际响应：%s", body)
	}
	if strings.Contains(body, `<input type="checkbox" name="ids" value="live1">`) {
		t.Errorf("LIVE 状态的 live1 不应该出现勾选框，实际响应：%s", body)
	}
	if !strings.Contains(body, `<button type="submit" class="danger">删除选中</button>`) {
		t.Errorf("应该有一个删除选中按钮，实际响应：%s", body)
	}
}
