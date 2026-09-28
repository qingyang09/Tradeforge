package webui

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"tradeforge/internal/agent"
	"tradeforge/internal/modules"
)

// configJSONWrongSymbol 模拟模型没能（或没必要）猜对标的的情况：用户在 ETHUSDT 的画板上
// 描述规则，模型却给了 BTCUSDT——handleBuilderTranslate 必须把它强制纠正回画板当前的标的，
// 不能原样相信模型的输出。
const configJSONWrongSymbol = `{
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
      "stop_loss_pct": 0.02,
      "take_profit_pct": 0.05
    }
  }
}`

func TestHandleBuilderTranslateForcesSymbolFromPath(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), newTestAgent(configJSONWrongSymbol))

	w := postForm(srv, "/builder/ETHUSDT/translate", url.Values{"utterance": {"放量突破做多，止损2%，止盈5%"}})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	if !strings.Contains(body, "<td>ETHUSDT</td>") {
		t.Errorf("复述表格应显示画板当前标的 ETHUSDT（强制覆盖），实际：%s", body)
	}
	if strings.Contains(body, "<td>BTCUSDT</td>") {
		t.Errorf("不应出现模型自己猜的 BTCUSDT，实际：%s", body)
	}

	dataConfig := extractDataConfig(t, body)
	if !strings.Contains(dataConfig, "ETHUSDT") {
		t.Errorf("嵌入给 JS 的 data-config 也应带上强制覆盖后的标的，实际：%s", dataConfig)
	}
	if strings.Contains(dataConfig, "BTCUSDT") {
		t.Errorf("data-config 不应残留模型原始猜的 BTCUSDT，实际：%s", dataConfig)
	}
}

func TestHandleBuilderTranslateEmbedsConfigJSONForCanvas(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), newTestAgent(configJSONWrongSymbol))

	w := postForm(srv, "/builder/ETHUSDT/translate", url.Values{"utterance": {"放量突破做多"}})
	body := w.Body.String()

	if !strings.Contains(body, `id="wizard-proposal-config"`) {
		t.Fatalf("应嵌入 #wizard-proposal-config 供画板 JS 读取，实际：%s", body)
	}
	dataConfig := extractDataConfig(t, body)
	for _, want := range []string{"volume_breakout", "stop_loss_pct", "0.02"} {
		if !strings.Contains(dataConfig, want) {
			t.Errorf("data-config 里应包含 %q，实际：%s", want, dataConfig)
		}
	}
}

func TestHandleBuilderTranslateClarificationPreservesForcedSymbolAcrossRounds(t *testing.T) {
	stub := &agent.StubLLM{Responses: []string{clarifyJSON, configJSONWrongSymbol}}
	ag := newTestAgentWithLLM(stub)
	srv := newTestServerWithAgent(t, newFakeStore(), ag)

	first := postForm(srv, "/builder/ETHUSDT/translate", url.Values{"utterance": {"做多"}})
	if !strings.Contains(first.Body.String(), "用哪个标的？") {
		t.Fatalf("第一轮应触发澄清问题，实际：%s", first.Body.String())
	}
	state := extractHiddenState(t, first.Body.String())

	second := postForm(srv, "/wizard/clarify", url.Values{"answer": {"就在这个标的上"}, "state": {state}})
	if second.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", second.Code, second.Body.String())
	}
	body := second.Body.String()

	// 第二轮模型又给了 BTCUSDT（configJSONWrongSymbol），但澄清问答是通过通用的
	// /wizard/clarify 完成的——ForceSymbol 必须跟着 state 传过来，继续把结果纠正回
	// 最初画板锁定的 ETHUSDT，不能因为多问一轮就失效。
	if !strings.Contains(body, "<td>ETHUSDT</td>") {
		t.Errorf("澄清后仍应显示强制覆盖的 ETHUSDT，实际：%s", body)
	}
	if strings.Contains(body, "<td>BTCUSDT</td>") {
		t.Errorf("澄清轮次不应让模型的 BTCUSDT 漏出来，实际：%s", body)
	}
}

// TestHandleBuilderTranslateKeepsSymbolContextInHistoryAcrossClarifyRounds 是一次真实
// 事故的回归测试：之前 handleBuilderTranslate 会把注入给模型的"当前标的是 XXX"这句
// 上下文从 p.SourceUtterance 里去掉，只留用户原话（为了让存库的审计记录干净）。
// 但 handleWizardClarify 重建对话历史时，恰恰是拿 ws.Proposal.SourceUtterance 当成
// "用户说的第一轮话"回灌给模型——上下文被去掉之后，模型从第二轮澄清问答开始就不再
// 知道标的已经定死，会反复追问"请问标的是什么"，用真实 DeepSeek 复现过这个循环。
// 这里验证：第二次调用 LLM 时，历史记录里第一轮的文本仍然带着标的上下文。
func TestHandleBuilderTranslateKeepsSymbolContextInHistoryAcrossClarifyRounds(t *testing.T) {
	stub := &agent.StubLLM{Responses: []string{clarifyJSON, configJSONWrongSymbol}}
	ag := newTestAgentWithLLM(stub)
	srv := newTestServerWithAgent(t, newFakeStore(), ag)

	first := postForm(srv, "/builder/ETHUSDT/translate", url.Values{"utterance": {"做多"}})
	state := extractHiddenState(t, first.Body.String())
	postForm(srv, "/wizard/clarify", url.Values{"answer": {"继续"}, "state": {state}})

	if len(stub.Calls) != 2 {
		t.Fatalf("应调用 LLM 两次，实际 %d 次", len(stub.Calls))
	}
	secondCallTurns := stub.Calls[1]
	if len(secondCallTurns) == 0 {
		t.Fatal("第二次调用应带上历史对话，实际是空的")
	}
	firstTurnText := secondCallTurns[0].Text
	if !strings.Contains(firstTurnText, "ETHUSDT") {
		t.Errorf("第二轮送给模型的历史里，第一轮用户话应该还带着标的上下文（ETHUSDT），实际：%q", firstTurnText)
	}
}

func TestBuilderTranslateThenConfirmSavesWithForcedSymbol(t *testing.T) {
	store := newFakeStore()
	srv := newTestServerWithAgent(t, store, newTestAgent(configJSONWrongSymbol))

	translate := postForm(srv, "/builder/ETHUSDT/translate", url.Values{"utterance": {"放量突破做多，止损2%，止盈5%"}})
	state := extractHiddenState(t, translate.Body.String())

	confirm := postForm(srv, "/wizard/confirm", url.Values{"state": {state}, "decision": {"confirm"}})
	if confirm.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", confirm.Code, confirm.Body.String())
	}
	if len(store.saved) != 1 {
		t.Fatalf("应保存 1 条策略，实际 %d 条", len(store.saved))
	}
	if got := store.saved[0].Symbol; got != "ETHUSDT" {
		t.Errorf("落库的标的 = %q，期望被强制纠正为画板当前标的 ETHUSDT", got)
	}
}

func TestHandleBuilderTranslateDegradesGracefullyWithNilAgent(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), nil)

	w := postForm(srv, "/builder/BTCUSDT/translate", url.Values{"utterance": {"随便什么"}})
	if !strings.Contains(w.Body.String(), "未就绪") {
		t.Errorf("Agent 未配置时应给出事实性提示，实际：%s", w.Body.String())
	}
}

func TestHandleBuilderTranslateRejectsEmptyUtterance(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), newTestAgent(configJSONWrongSymbol))

	w := postForm(srv, "/builder/BTCUSDT/translate", url.Values{"utterance": {"   "}})
	if !strings.Contains(w.Body.String(), "不能为空") {
		t.Errorf("空描述应被拒绝，实际：%s", w.Body.String())
	}
}

func TestHandleBuilderTranslateRejectsMalformedSymbolPath(t *testing.T) {
	srv := newTestServerWithAgent(t, newFakeStore(), newTestAgent(configJSONWrongSymbol))

	w := postForm(srv, "/builder/BTC-USD/translate", url.Values{"utterance": {"做多"}})
	if w.Code != http.StatusNotFound {
		t.Errorf("状态码 = %d，期望路径里的非法标的返回 404", w.Code)
	}
}

func TestHandleBuilderViewShowsNLBoxOnlyWhenAgentReady(t *testing.T) {
	withAgent := newTestServerWithAgent(t, newFakeStore(), newTestAgent(configJSONWrongSymbol))
	w := getPage(withAgent, "/builder/BTCUSDT")
	if !strings.Contains(w.Body.String(), `id="nl-plan-form"`) {
		t.Errorf("Agent 就绪时应显示自然语言入口，实际：%s", w.Body.String())
	}

	withoutAgent := newTestServer(t, newFakeStore())
	w2 := getPage(withoutAgent, "/builder/BTCUSDT")
	body := w2.Body.String()
	if strings.Contains(body, `id="nl-plan-form"`) {
		t.Errorf("Agent 未就绪时不应渲染表单，实际：%s", body)
	}
	if !strings.Contains(body, "需要先配置模型") {
		t.Errorf("Agent 未就绪时应给出事实性提示，实际：%s", body)
	}
}

// newTestAgentWithLLM 跟 newTestAgent 一样构造一个用真实模块注册表校验的 Agent，
// 只是直接接受一个已经构造好的 StubLLM（用来在测试里检查 stub.Calls）。
func newTestAgentWithLLM(llm agent.LLM) *agent.Agent {
	return agent.New(llm, modules.NewDefaultRegistry(), 0)
}

func extractDataConfig(t *testing.T, body string) string {
	t.Helper()
	const marker = `data-config="`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("响应里没有找到 data-config 属性：%s", body)
	}
	rest := body[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("data-config 属性值未正确闭合：%s", body)
	}
	return rest[:j]
}
