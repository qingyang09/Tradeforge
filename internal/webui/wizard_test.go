package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"tradeforge/internal/agent"
	"tradeforge/internal/modules"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// configJSON/clarifyJSON 是 agent.StubLLM 的可编程响应，格式跟 internal/agent 的
// 测试套件（wireProposal/wireConfig）完全一致——这里手写而不是 import 那些非导出类型，
// 因为它们只在 internal/agent 包内可见。
const configJSON = `{
  "outcome": "config",
  "restatement": "在 BTCUSDT 上，成交量突破 2 倍均量时做多。",
  "questions": [],
  "config": {
    "name": "放量突破",
    "symbol": "BTCUSDT",
    "timeframe": "1h",
    "combine": "ALL",
    "threshold": 0,
    "modules": [
      {"module": "volume_breakout", "params": {"multiplier": 2.0}, "weight": 0}
    ],
    "risk": {
      "max_position_size_quote": "1000",
      "max_daily_loss_quote": "",
      "max_holding_period": "",
      "stop_loss_pct": 0,
      "take_profit_pct": 0
    }
  }
}`

const clarifyJSON = `{
  "outcome": "clarification_needed",
  "restatement": "我理解你想做多，但还不清楚标的和触发条件。",
  "questions": ["用哪个标的？"],
  "config": null
}`

func newTestAgent(responses ...string) *agent.Agent {
	return agent.New(&agent.StubLLM{Responses: responses}, modules.NewDefaultRegistry(), 0)
}

func TestWizardTranslateRendersConfigConfirmation(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), newTestAgent(configJSON))

	w := postForm(srv, "/wizard/translate", url.Values{"utterance": {"BTCUSDT 放量突破做多"}})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "我理解你的策略是这样") || !strings.Contains(body, "volume_breakout") {
		t.Errorf("应展示复述与模块摘要，实际：%s", body)
	}
	if !strings.Contains(body, `name="state"`) {
		t.Errorf("应带上隐藏的向导状态字段，实际：%s", body)
	}
}

func TestWizardTranslateRendersClarificationQuestions(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), newTestAgent(clarifyJSON))

	w := postForm(srv, "/wizard/translate", url.Values{"utterance": {"做多"}})
	body := w.Body.String()
	if !strings.Contains(body, "用哪个标的？") {
		t.Errorf("应展示澄清问题，实际：%s", body)
	}
}

func TestWizardClarifyBuildsHistoryLikeCLI(t *testing.T) {
	stub := &agent.StubLLM{Responses: []string{clarifyJSON, configJSON}}
	ag := agent.New(stub, modules.NewDefaultRegistry(), 0)
	srv := newTestServerWithAgent(t, newFakeStore(), ag)

	first := postForm(srv, "/wizard/translate", url.Values{"utterance": {"做多"}})
	state := extractHiddenState(t, first.Body.String())

	second := postForm(srv, "/wizard/clarify", url.Values{"answer": {"BTCUSDT"}, "state": {state}})
	if second.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), "我理解你的策略是这样") {
		t.Errorf("第二轮翻译成功后应展示复述，实际：%s", second.Body.String())
	}

	if len(stub.Calls) != 2 {
		t.Fatalf("应调用 LLM 两次，实际 %d 次", len(stub.Calls))
	}
	turns := stub.Calls[1]
	if len(turns) != 3 {
		t.Fatalf("第二次调用应有 3 轮对话（问题轮 2 条 + 本轮回答），实际 %d 条：%+v", len(turns), turns)
	}
	if turns[0].Role != "user" || turns[0].Text != "做多" {
		t.Errorf("第一轮应是原始用户话，实际：%+v", turns[0])
	}
	if turns[1].Role != "assistant" || !strings.Contains(turns[1].Text, "用哪个标的") {
		t.Errorf("第二轮应是 Agent 提出的问题，实际：%+v", turns[1])
	}
	if turns[2].Role != "user" || !strings.Contains(turns[2].Text, "BTCUSDT") {
		t.Errorf("第三轮应是用户的回答（含 BTCUSDT），实际：%+v", turns[2])
	}
}

func TestWizardClarifyRejectsTooManyRounds(t *testing.T) {
	// 连续 5 轮都要求澄清：round 数达到上限后应直接拒绝，不再调用 LLM。
	responses := make([]string, 6)
	for i := range responses {
		responses[i] = clarifyJSON
	}
	stub := &agent.StubLLM{Responses: responses}
	ag := agent.New(stub, modules.NewDefaultRegistry(), 0)
	srv := newTestServerWithAgent(t, newFakeStore(), ag)

	w := postForm(srv, "/wizard/translate", url.Values{"utterance": {"做多"}})
	state := extractHiddenState(t, w.Body.String())

	for i := 0; i < maxClarificationRounds-1; i++ {
		w = postForm(srv, "/wizard/clarify", url.Values{"answer": {"继续"}, "state": {state}})
		body := w.Body.String()
		if strings.Contains(body, "澄清轮次过多") {
			t.Fatalf("第 %d 轮不应提前触发轮次上限，响应：%s", i+1, body)
		}
		state = extractHiddenState(t, body)
	}

	final := postForm(srv, "/wizard/clarify", url.Values{"answer": {"继续"}, "state": {state}})
	if !strings.Contains(final.Body.String(), "澄清轮次过多") {
		t.Errorf("超过轮次上限应拒绝，实际：%s", final.Body.String())
	}
}

func TestWizardConfirmSavesStrategyAndRecordsDraftTransition(t *testing.T) {
	store := newFakeStore()
	srv := newTestServerWithAgent(t, store, newTestAgent(configJSON))

	translate := postForm(srv, "/wizard/translate", url.Values{"utterance": {"BTCUSDT 放量突破做多"}})
	state := extractHiddenState(t, translate.Body.String())

	confirm := postForm(srv, "/wizard/confirm", url.Values{"state": {state}, "decision": {"confirm"}})
	if confirm.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", confirm.Code, confirm.Body.String())
	}
	if !strings.Contains(confirm.Body.String(), "已保存") {
		t.Errorf("确认成功应提示已保存，实际：%s", confirm.Body.String())
	}

	if len(store.saved) != 1 {
		t.Fatalf("应保存 1 个策略，实际 %d 个", len(store.saved))
	}
	saved := store.saved[0]
	if saved.State != types.StateDraft {
		t.Errorf("新策略状态 = %s，期望 DRAFT", saved.State)
	}
	if saved.ID == "" {
		t.Error("应分配策略 ID")
	}

	transitions := store.transitions[saved.ID]
	if len(transitions) != 1 || transitions[0].To != types.StateDraft {
		t.Errorf("应记录一条 → DRAFT 的流转审计，实际：%+v", transitions)
	}
}

func TestWizardConfirmCancelDoesNotTouchStore(t *testing.T) {
	store := newFakeStore()
	srv := newTestServerWithAgent(t, store, newTestAgent(configJSON))

	translate := postForm(srv, "/wizard/translate", url.Values{"utterance": {"BTCUSDT 放量突破做多"}})
	state := extractHiddenState(t, translate.Body.String())

	cancel := postForm(srv, "/wizard/confirm", url.Values{"state": {state}, "decision": {"cancel"}})
	if !strings.Contains(cancel.Body.String(), "已取消") {
		t.Errorf("取消应提示已取消，实际：%s", cancel.Body.String())
	}
	if len(store.saved) != 0 {
		t.Errorf("取消不应写库，实际保存了 %d 个策略", len(store.saved))
	}
}

func TestWizardConfirmRejectsTamperedState(t *testing.T) {
	store := newFakeStore()
	srv := newTestServerWithAgent(t, store, newTestAgent(configJSON))

	w := postForm(srv, "/wizard/confirm", url.Values{"state": {"不是合法的base64"}, "decision": {"confirm"}})
	if !strings.Contains(w.Body.String(), "会话已失效") {
		t.Errorf("损坏的状态应提示重新开始，实际：%s", w.Body.String())
	}
	if len(store.saved) != 0 {
		t.Errorf("损坏状态不应写库")
	}
}

func TestWizardHandlersDegradeGracefullyWithNilAgent(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), nil)

	w := postForm(srv, "/wizard/translate", url.Values{"utterance": {"随便什么"}})
	if !strings.Contains(w.Body.String(), "未配置 Agent") && !strings.Contains(w.Body.String(), "未就绪") {
		t.Errorf("Agent 未配置时应给出事实性提示，实际：%s", w.Body.String())
	}

	page := getPage(srv, "/wizard")
	if page.Code != http.StatusOK {
		t.Fatalf("向导起始页在 Agent 未就绪时仍应返回 200，实际 %d", page.Code)
	}
}

// ---- 测试辅助 ----

func newTestServerWithAgent(t *testing.T, store *fakeStore, ag *agent.Agent) *Server {
	t.Helper()
	srv, err := New(store, ag, strategy.DefaultGate(), quietLogger())
	if err != nil {
		t.Fatalf("New() 失败：%v", err)
	}
	srv.SetMasterKey(testMasterKey)
	loginTestUser(store, srv)
	return srv
}

func postForm(srv *Server, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	attachTestSession(srv, req)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	return w
}

// extractHiddenState 从渲染出的 HTML 里把 <input type=hidden name=state value="..."> 的值挖出来。
func extractHiddenState(t *testing.T, body string) string {
	t.Helper()
	const marker = `name="state" value="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("响应里没有找到隐藏的 state 字段：%s", body)
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("state 字段值未正确闭合：%s", body)
	}
	return rest[:j]
}
