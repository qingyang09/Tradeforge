package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"tradeforge/pkg/types"
)

func postJSON(srv *Server, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	attachTestSession(srv, req)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	return w
}

const validBuilderConfig = `{
	"name": "画板策略",
	"symbol": "BTCUSDT",
	"timeframe": "1h",
	"combine": "ALL",
	"modules": [
		{"module": "volume_breakout", "params": {"multiplier": 2.5, "window": 20}}
	],
	"risk": {"max_position_size_quote": "1000"}
}`

// 画板不依赖 LLM key：newTestServer 构造的 Server 没有配置 agent，
// 提交一份合法配置应该照样能走到复述确认页——用的是确定性兜底复述。
func TestHandleBuilderDescribeRendersConfirmFragmentWithoutAgent(t *testing.T) {
	srv := newTestServer(t, newFakeStore())

	w := postJSON(srv, "/builder/describe", validBuilderConfig)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "我理解你的策略是这样") {
		t.Errorf("应渲染复述确认片段，实际：%s", body)
	}
	if !strings.Contains(body, "volume_breakout") {
		t.Errorf("复述里应带上模块信息，实际：%s", body)
	}
	if !strings.Contains(body, `name="state"`) {
		t.Errorf("确认表单应带上隐藏的 state 字段，实际：%s", body)
	}
}

func TestHandleBuilderDescribeRejectsInvalidConfig(t *testing.T) {
	srv := newTestServer(t, newFakeStore())

	invalid := `{
		"name": "画板策略", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": [{"module": "volume_breakout", "params": {"multiplier": 999}}],
		"risk": {"max_position_size_quote": "1000"}
	}`
	w := postJSON(srv, "/builder/describe", invalid)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "校验") {
		t.Errorf("超出范围的参数应被拒绝并给出具体原因，实际：%s", w.Body.String())
	}
}

func TestHandleBuilderDescribeRejectsMalformedJSON(t *testing.T) {
	srv := newTestServer(t, newFakeStore())
	w := postJSON(srv, "/builder/describe", "不是 JSON")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "格式") {
		t.Errorf("非法 JSON 应给出格式错误提示，实际：%s", w.Body.String())
	}
}

// 端到端：画板生成的配置 → 复述确认 → 用户点确认 → 真的落库为 DRAFT。
// 这条断言验证两条建策路径（文字向导 / 可视化建策）殊途同归，共用同一套保存逻辑。
func TestBuilderDescribeThenConfirmSavesStrategyAsDraft(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)

	describeResp := postJSON(srv, "/builder/describe", validBuilderConfig)
	state := extractHiddenState(t, describeResp.Body.String())

	confirmResp := postForm(srv, "/wizard/confirm", urlValues(map[string]string{
		"state": state, "decision": "confirm",
	}))
	if confirmResp.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", confirmResp.Code, confirmResp.Body.String())
	}
	if len(store.saved) != 1 {
		t.Fatalf("应保存 1 条策略，实际 %d 条", len(store.saved))
	}
	saved := store.saved[0]
	if saved.State != types.StateDraft {
		t.Errorf("策略状态 = %s，期望 DRAFT", saved.State)
	}
	if saved.Symbol != "BTCUSDT" || len(saved.Modules) != 1 || saved.Modules[0].Module != "volume_breakout" {
		t.Errorf("保存的配置跟画板提交的不一致：%+v", saved)
	}
	if saved.SourceUtterance != "可视化建策" {
		t.Errorf("SourceUtterance = %q，期望标注来自可视化建策", saved.SourceUtterance)
	}
}

// handleWizardConfirm 重构后不应该再要求 Agent 就绪——这是可视化建策"不依赖 LLM key"
// 这个目标的直接前提，之前这里是硬性拦截在 ag == nil 的。
func TestHandleWizardConfirmSavesWithoutAgent(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store) // 没有 agent

	describeResp := postJSON(srv, "/builder/describe", validBuilderConfig)
	state := extractHiddenState(t, describeResp.Body.String())

	w := postForm(srv, "/wizard/confirm", urlValues(map[string]string{
		"state": state, "decision": "confirm",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if len(store.saved) != 1 {
		t.Fatalf("无 agent 时也应能确认保存，实际保存 %d 条", len(store.saved))
	}
}

// ---------- ?strategy=<id> 深链：策略详情页"去画板查看"要能重新画出保存过的模块 ----------

func strategyWithVolumeBreakout(id string) types.StrategyConfig {
	sc := draftStrategy(id)
	sc.Name = "带模块的策略"
	sc.Modules = []types.ModuleConfig{
		{Module: "volume_breakout", Params: map[string]any{"multiplier": 2.5, "window": 20}},
	}
	return sc
}

// 这是本次要修的缺口本身：带着 ?strategy=<id> 打开画板，应该能拿到这条策略的配置
// JSON，供页面 JS 的 applyStrategyConfig 把模块重新画回图上，而不是永远落回空白画布。
func TestHandleBuilderViewEmbedsStrategyConfigWhenStrategyParamGiven(t *testing.T) {
	store := newFakeStore()
	id := "aaaaaaaa-2222-4222-8222-222222222222"
	store.strategies[id] = strategyWithVolumeBreakout(id)
	srv := newTestServer(t, store)

	w := getPage(srv, "/builder/BTCUSDT?timeframe=1h&strategy="+id)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="builder-initial-config"`) {
		t.Fatalf("应该嵌入初始配置容器，实际：%s", body)
	}
	// 用策略名字这个专属字符串做信号，不用 "volume_breakout"——那个词本身在空画布的
	// 提示文案里就会无条件出现（"先在右侧模块栏添加一个（比如 volume_breakout）"），
	// 只检查它在不在没法证明真的是嵌入的配置 JSON 带来的。
	if !strings.Contains(body, "带模块的策略") {
		t.Errorf("嵌入的配置 JSON 里应该带上保存过的策略名，实际：%s", body)
	}
}

// 没带 ?strategy= 是最常见的情况（单纯看图、或者从"新建策略"点进来），
// 必须照旧是空白画布，不能因为这次改动意外要求这个参数。
func TestHandleBuilderViewIsBlankCanvasWithoutStrategyParam(t *testing.T) {
	srv := newTestServer(t, newFakeStore())

	w := getPage(srv, "/builder/BTCUSDT")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `id="builder-initial-config"`) {
		t.Error("没有 ?strategy= 参数时不应该嵌入任何初始配置")
	}
}

// 不属于当前用户的策略 ID：不能把别人的模块组合/风控参数泄露到这个用户的画板上，
// 静默退回空白画布——跟策略详情页 404 掉不属于自己的策略是同一条安全边界。
func TestHandleBuilderViewIgnoresStrategyBelongingToAnotherUser(t *testing.T) {
	store := newFakeStore()
	id := "aaaaaaaa-3333-4333-8333-333333333333"
	other := strategyWithVolumeBreakout(id)
	other.UserID = "someone-else"
	store.strategies[id] = other
	srv := newTestServer(t, store)

	w := getPage(srv, "/builder/BTCUSDT?strategy="+id)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	// 不检查 "volume_breakout" 这个词本身在不在——页面自己空画布提示文案里就举了这个
	// 模块名当例子（"先在右侧模块栏添加一个（比如 volume_breakout）"），会无条件出现，
	// 拿它当信号是假阳性。真正要确认的是"根本没有嵌入任何初始配置容器"。
	if strings.Contains(w.Body.String(), `id="builder-initial-config"`) {
		t.Error("不属于当前用户的策略不应该嵌入任何初始配置容器")
	}
}

// 策略 ID 查不到（已删除/纯手输的假 ID）：同样静默退回空白画布，不报错。
func TestHandleBuilderViewIgnoresUnknownStrategyID(t *testing.T) {
	srv := newTestServer(t, newFakeStore())

	w := getPage(srv, "/builder/BTCUSDT?strategy=aaaaaaaa-4444-4444-8444-444444444444")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `id="builder-initial-config"`) {
		t.Error("查不到的策略 ID 不应该嵌入任何初始配置")
	}
}

func urlValues(m map[string]string) url.Values {
	v := make(url.Values)
	for k, val := range m {
		v[k] = []string{val}
	}
	return v
}
