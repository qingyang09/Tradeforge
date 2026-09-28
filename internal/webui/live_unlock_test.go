package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"tradeforge/pkg/types"
)

const unlockTestID = "dddddddd-4444-4444-8444-444444444444"

func eligibleStrategy(id string) types.StrategyConfig {
	sc := draftStrategy(id)
	sc.State = types.StateLiveEligible
	return sc
}

func TestHandleUnlockLiveAdvancesEligibleStrategy(t *testing.T) {
	store := newFakeStore()
	store.strategies[unlockTestID] = eligibleStrategy(unlockTestID)
	srv := newTestServer(t, store)

	w := postForm(srv, "/strategies/"+unlockTestID+"/unlock-live", url.Values{
		"actor_id": {"alice"}, "reason": {"人工复核通过"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "已解锁实盘") {
		t.Errorf("应提示解锁成功，实际：%s", w.Body.String())
	}
	if store.strategies[unlockTestID].State != types.StateLive {
		t.Errorf("策略状态 = %s，期望 LIVE", store.strategies[unlockTestID].State)
	}
	if len(store.updateStateCalls) != 1 {
		t.Fatalf("应调用 UpdateStrategyState 一次，实际 %d 次", len(store.updateStateCalls))
	}
	call := store.updateStateCalls[0]
	if call.From != types.StateLiveEligible || call.To != types.StateLive {
		t.Errorf("流转记录不符：%+v", call)
	}
	if !strings.Contains(call.Actor, "alice") {
		t.Errorf("操作者应带上 actor_id，实际：%s", call.Actor)
	}
}

func TestHandleUnlockLiveRejectsWrongState(t *testing.T) {
	store := newFakeStore()
	store.strategies[unlockTestID] = draftStrategy(unlockTestID) // 仍是 DRAFT，不该能解锁
	srv := newTestServer(t, store)

	w := postForm(srv, "/strategies/"+unlockTestID+"/unlock-live", url.Values{
		"actor_id": {"alice"}, "reason": {"想跳过流程"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "banner-err") {
		t.Errorf("非法流转应展示错误提示，实际：%s", w.Body.String())
	}
	if store.strategies[unlockTestID].State != types.StateDraft {
		t.Errorf("非法流转不应改变状态，实际状态 %s", store.strategies[unlockTestID].State)
	}
	if len(store.updateStateCalls) != 0 {
		t.Errorf("非法流转不应调用 UpdateStrategyState")
	}
}

func TestHandleUnlockLiveRequiresActorIDAndReason(t *testing.T) {
	store := newFakeStore()
	store.strategies[unlockTestID] = eligibleStrategy(unlockTestID)
	srv := newTestServer(t, store)

	w := postForm(srv, "/strategies/"+unlockTestID+"/unlock-live", url.Values{"actor_id": {""}, "reason": {""}})
	if !strings.Contains(w.Body.String(), "banner-err") {
		t.Errorf("缺操作者标识/理由应被拒绝，实际：%s", w.Body.String())
	}
	if store.strategies[unlockTestID].State != types.StateLiveEligible {
		t.Errorf("校验失败不应改变状态")
	}
}

func TestHandleUnlockLiveRejectsGET(t *testing.T) {
	store := newFakeStore()
	store.strategies[unlockTestID] = eligibleStrategy(unlockTestID)
	srv := newTestServer(t, store)

	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/strategies/"+unlockTestID+"/unlock-live", nil))
	if w.Code == http.StatusOK {
		t.Errorf("GET 请求不应被当作解锁操作接受，状态码 = %d", w.Code)
	}
}

func TestHandleUnlockLiveUnknownStrategyReturns404(t *testing.T) {
	srv := newTestServer(t, newFakeStore())
	// 合法 UUID 形状但库里没有这一条：走 Store.GetStrategy 返回 ErrNotFound 的路径。
	w := postForm(srv, "/strategies/eeeeeeee-5555-4555-8555-555555555555/unlock-live", url.Values{
		"actor_id": {"alice"}, "reason": {"x"},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", w.Code)
	}
}

// 真实 Postgres 里 strategies.id 是 UUID 列，格式不对的路径参数会让驱动直接报
// "invalid input syntax for type uuid"——这个错误不是 ErrNotFound，如果不提前拦截，
// looksLikeUUID 加在 handleStrategyDetail 那份守卫必须在 handleUnlockLive 这里也生效，
// 否则同一类请求走这条路径时又会冒充成 500。
func TestHandleUnlockLiveMalformedIDReturns404NotServerError(t *testing.T) {
	srv := newTestServer(t, newFakeStore())
	w := postForm(srv, "/strategies/does-not-exist/unlock-live", url.Values{
		"actor_id": {"alice"}, "reason": {"x"},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404（格式不对的 ID 不该冒充成 500）", w.Code)
	}
}
