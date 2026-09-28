package webui

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

const promoteTestID = "12121212-1212-4212-8212-121212121212"

func passingBacktest(strategyID string) types.BacktestResult {
	return types.BacktestResult{
		ID: "bt1", StrategyID: strategyID, Symbol: "BTCUSDT",
		OutOfSample: types.PerformanceMetrics{
			TradeCount: 10, SharpeRatio: 1.2, MaxDrawdown: 0.1, TotalReturn: 0.2,
		},
		InitialCapital: decimal.NewFromInt(10000),
	}
}

func TestHandleConfirmBacktestAdvancesDraftStrategy(t *testing.T) {
	store := newFakeStore()
	store.strategies[promoteTestID] = draftStrategy(promoteTestID)
	store.backtests[promoteTestID] = passingBacktest(promoteTestID)
	srv := newTestServer(t, store)

	w := postForm(srv, "/strategies/"+promoteTestID+"/confirm-backtest", url.Values{
		"actor_id": {"alice"}, "reason": {"样本外指标达标"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "已确认") {
		t.Errorf("应提示已确认，实际：%s", w.Body.String())
	}
	if store.strategies[promoteTestID].State != types.StateBacktested {
		t.Errorf("策略状态 = %s，期望 BACKTESTED", store.strategies[promoteTestID].State)
	}
}

func TestHandleConfirmBacktestRejectsWhenNoBacktestResult(t *testing.T) {
	store := newFakeStore()
	store.strategies[promoteTestID] = draftStrategy(promoteTestID)
	srv := newTestServer(t, store)

	w := postForm(srv, "/strategies/"+promoteTestID+"/confirm-backtest", url.Values{
		"actor_id": {"alice"}, "reason": {"想跳过回测"},
	})
	if !strings.Contains(w.Body.String(), "banner-err") || !strings.Contains(w.Body.String(), "尚无回测结果") {
		t.Errorf("没有回测结果时应给出事实性拒绝理由，实际：%s", w.Body.String())
	}
	if store.strategies[promoteTestID].State != types.StateDraft {
		t.Errorf("拒绝时不应改变状态")
	}
}

func TestHandleConfirmBacktestRejectsFailingOutOfSample(t *testing.T) {
	store := newFakeStore()
	store.strategies[promoteTestID] = draftStrategy(promoteTestID)
	store.backtests[promoteTestID] = types.BacktestResult{
		ID: "bt1", StrategyID: promoteTestID, Symbol: "BTCUSDT",
		OutOfSample:    types.PerformanceMetrics{TradeCount: 1, SharpeRatio: -0.5},
		InitialCapital: decimal.NewFromInt(10000),
	}
	srv := newTestServer(t, store)

	w := postForm(srv, "/strategies/"+promoteTestID+"/confirm-backtest", url.Values{
		"actor_id": {"alice"}, "reason": {"想凑合过"},
	})
	if !strings.Contains(w.Body.String(), "banner-err") {
		t.Errorf("样本外未达标应被拒绝，实际：%s", w.Body.String())
	}
	if store.strategies[promoteTestID].State != types.StateDraft {
		t.Errorf("拒绝时不应改变状态")
	}
}

func TestHandleStartPaperTradingAdvancesBacktestedStrategy(t *testing.T) {
	store := newFakeStore()
	sc := draftStrategy(promoteTestID)
	sc.State = types.StateBacktested
	store.strategies[promoteTestID] = sc
	srv := newTestServer(t, store)

	w := postForm(srv, "/strategies/"+promoteTestID+"/start-paper-trading", url.Values{
		"actor_id": {"alice"}, "reason": {"准备开始模拟盘"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "已进入模拟盘") {
		t.Errorf("应提示已进入模拟盘，实际：%s", w.Body.String())
	}
	if store.strategies[promoteTestID].State != types.StatePaperTrading {
		t.Errorf("策略状态 = %s，期望 PAPER_TRADING", store.strategies[promoteTestID].State)
	}
}

func TestHandleStartPaperTradingRejectsWrongState(t *testing.T) {
	store := newFakeStore()
	store.strategies[promoteTestID] = draftStrategy(promoteTestID) // 仍是 DRAFT
	srv := newTestServer(t, store)

	w := postForm(srv, "/strategies/"+promoteTestID+"/start-paper-trading", url.Values{
		"actor_id": {"alice"}, "reason": {"想跳过回测确认"},
	})
	if !strings.Contains(w.Body.String(), "banner-err") {
		t.Errorf("非法流转应展示错误提示，实际：%s", w.Body.String())
	}
	if store.strategies[promoteTestID].State != types.StateDraft {
		t.Errorf("非法流转不应改变状态")
	}
}

func TestStrategyDetailShowsPromotionFormsBasedOnState(t *testing.T) {
	store := newFakeStore()
	store.strategies[promoteTestID] = draftStrategy(promoteTestID)
	srv := newTestServer(t, store)

	body := getPage(srv, "/strategies/"+promoteTestID).Body.String()
	if !strings.Contains(body, "确认回测结果") {
		t.Errorf("DRAFT 状态应展示确认回测表单，实际：%s", body)
	}
	if strings.Contains(body, "进入模拟盘") {
		t.Errorf("DRAFT 状态不应展示进入模拟盘表单，实际：%s", body)
	}

	backtested := store.strategies[promoteTestID]
	backtested.State = types.StateBacktested
	store.strategies[promoteTestID] = backtested
	body = getPage(srv, "/strategies/"+promoteTestID).Body.String()
	if !strings.Contains(body, "进入模拟盘") {
		t.Errorf("BACKTESTED 状态应展示进入模拟盘表单，实际：%s", body)
	}
	if strings.Contains(body, "确认回测结果") {
		t.Errorf("BACKTESTED 状态不应再展示确认回测表单，实际：%s", body)
	}
}

// ---------- 删除策略：只允许 DRAFT ----------

func TestHandleDeleteStrategyRemovesDraftStrategy(t *testing.T) {
	store := newFakeStore()
	store.strategies[promoteTestID] = draftStrategy(promoteTestID)
	srv := newTestServer(t, store)

	w := postForm(srv, "/strategies/"+promoteTestID+"/delete", url.Values{})
	if w.Code != http.StatusFound {
		t.Fatalf("删除成功应重定向到看板，状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("重定向目标 = %q，期望 /", loc)
	}
	if _, ok := store.strategies[promoteTestID]; ok {
		t.Error("策略应该已被删除，但仍然存在")
	}
}

func TestHandleDeleteStrategyRejectsNonDraftState(t *testing.T) {
	store := newFakeStore()
	sc := draftStrategy(promoteTestID)
	sc.State = types.StateBacktested
	store.strategies[promoteTestID] = sc
	srv := newTestServer(t, store)

	w := postForm(srv, "/strategies/"+promoteTestID+"/delete", url.Values{})
	if w.Code != http.StatusOK {
		t.Fatalf("拒绝时应正常渲染详情页（带错误 banner），状态码 = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `class="banner-err"`) {
		t.Errorf("应展示错误 banner，实际：%s", w.Body.String())
	}
	if _, ok := store.strategies[promoteTestID]; !ok {
		t.Error("非 DRAFT 状态时不应删除策略")
	}
}

func TestHandleDeleteStrategyReturns404ForUnknownStrategy(t *testing.T) {
	srv := newTestServer(t, newFakeStore())
	w := postForm(srv, "/strategies/ffffffff-9999-4999-8999-999999999999/delete", url.Values{})
	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", w.Code)
	}
}

// 详情页只在 DRAFT 状态展示"删除策略"这个入口——跟"确认回测"是同一个门槛。
func TestStrategyDetailShowsDeleteFormOnlyForDraft(t *testing.T) {
	store := newFakeStore()
	store.strategies[promoteTestID] = draftStrategy(promoteTestID)
	srv := newTestServer(t, store)

	body := getPage(srv, "/strategies/"+promoteTestID).Body.String()
	if !strings.Contains(body, "永久删除这条策略") {
		t.Errorf("DRAFT 状态应展示删除入口，实际：%s", body)
	}

	backtested := store.strategies[promoteTestID]
	backtested.State = types.StateBacktested
	store.strategies[promoteTestID] = backtested
	body = getPage(srv, "/strategies/"+promoteTestID).Body.String()
	if strings.Contains(body, "永久删除这条策略") {
		t.Errorf("BACKTESTED 状态不应展示删除入口，实际：%s", body)
	}
}
