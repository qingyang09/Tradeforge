package webui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"tradeforge/internal/agent"
	"tradeforge/internal/i18n"
	"tradeforge/internal/marketdata/okx"
	"tradeforge/internal/modules"
)

// ---------- topSymbolsByVolume：纯函数，跟网络无关 ----------

func TestTopSymbolsByVolumeSortsDescending(t *testing.T) {
	tickers := []okx.Ticker{
		{Symbol: "DOGEUSDT", Vol24hQuote: decimal.NewFromInt(100)},
		{Symbol: "BTCUSDT", Vol24hQuote: decimal.NewFromInt(900)},
		{Symbol: "ETHUSDT", Vol24hQuote: decimal.NewFromInt(500)},
	}
	got := topSymbolsByVolume(tickers, 2)
	want := []string{"BTCUSDT", "ETHUSDT"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("topSymbolsByVolume = %v，期望 %v", got, want)
	}
}

func TestTopSymbolsByVolumeClampsToAvailableCount(t *testing.T) {
	tickers := []okx.Ticker{{Symbol: "BTCUSDT", Vol24hQuote: decimal.NewFromInt(1)}}
	got := topSymbolsByVolume(tickers, 50)
	if len(got) != 1 {
		t.Errorf("N 超过实际标的数时应该只返回实际数量，实际返回 %d 个", len(got))
	}
}

func TestTopSymbolsByVolumeHandlesZeroOrNegativeN(t *testing.T) {
	tickers := []okx.Ticker{{Symbol: "BTCUSDT", Vol24hQuote: decimal.NewFromInt(1)}}
	if got := topSymbolsByVolume(tickers, 0); got != nil {
		t.Errorf("N=0 应返回空，实际 %v", got)
	}
	if got := topSymbolsByVolume(tickers, -1); got != nil {
		t.Errorf("N<0 应返回空，实际 %v", got)
	}
}

// ---------- parseBatchCount ----------

func TestParseBatchCountDefaultsWhenEmpty(t *testing.T) {
	n, err := parseBatchCount(i18n.LangZH, "")
	if err != nil || n != defaultBatchScanCount {
		t.Errorf("空输入应默认 %d，实际 n=%d err=%v", defaultBatchScanCount, n, err)
	}
}

func TestParseBatchCountClampsAboveMax(t *testing.T) {
	n, err := parseBatchCount(i18n.LangZH, "500")
	if err != nil || n != maxBatchScanCount {
		t.Errorf("超过上限应夹逼到 %d，实际 n=%d err=%v", maxBatchScanCount, n, err)
	}
}

func TestParseBatchCountRejectsNonPositive(t *testing.T) {
	if _, err := parseBatchCount(i18n.LangZH, "0"); err == nil {
		t.Error("0 应该报错")
	}
	if _, err := parseBatchCount(i18n.LangZH, "-3"); err == nil {
		t.Error("负数应该报错")
	}
	if _, err := parseBatchCount(i18n.LangZH, "abc"); err == nil {
		t.Error("非数字应该报错")
	}
}

// ---------- handler 集成测试 ----------

// tickersJSON 拼一份 OKX /market/tickers 的响应，pairs 是 (instId, volCcy24h)。
func tickersJSON(pairs ...[2]string) string {
	var b strings.Builder
	b.WriteString(`{"code":"0","msg":"","data":[`)
	for i, p := range pairs {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(fmt.Sprintf(`{"instId":%q,"last":"1","volCcy24h":%q}`, p[0], p[1]))
	}
	b.WriteString(`]}`)
	return b.String()
}

func newBatchTestServer(t *testing.T, store *fakeStore, ag *agent.Agent, tickerPairs ...[2]string) *Server {
	t.Helper()
	srv := newTestServerWithAgent(t, store, ag)
	okxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(tickersJSON(tickerPairs...)))
	}))
	t.Cleanup(okxSrv.Close)
	srv.okxClient = okx.NewClient(okx.WithRESTBaseURL(okxSrv.URL))
	return srv
}

func TestHandleBatchScanTranslateShowsSymbolListOnConfigOutcome(t *testing.T) {
	stub := &agent.StubLLM{Responses: []string{configJSON}}
	ag := agent.New(stub, modules.NewDefaultRegistry(), 0)
	srv := newBatchTestServer(t, newFakeStore(), ag,
		[2]string{"BTC-USDT", "900"}, [2]string{"ETH-USDT", "500"})

	w := postForm(srv, "/wizard/batch/scan", url.Values{
		"utterance": {"放量突破做多"}, "count": {"2"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "我理解你的规则是这样") {
		t.Errorf("应展示复述，实际：%s", body)
	}
	if !strings.Contains(body, "BTCUSDT") || !strings.Contains(body, "ETHUSDT") {
		t.Errorf("应展示扫描出的标的列表，实际：%s", body)
	}
	if !strings.Contains(body, "将应用到以下 2 个标的") {
		t.Errorf("应说明标的数量，实际：%s", body)
	}

	if len(stub.Calls) != 1 {
		t.Fatalf("应调用 LLM 一次，实际 %d 次", len(stub.Calls))
	}
	sentText := stub.Calls[0][len(stub.Calls[0])-1].Text
	if !strings.Contains(sentText, "套用到扫描出的 2 个不同标的") {
		t.Errorf("发给 LLM 的内容应包含标的无关的上下文提示，实际：%s", sentText)
	}
}

func TestHandleBatchScanTranslateRejectsEmptyUtterance(t *testing.T) {
	ag := agent.New(&agent.StubLLM{}, modules.NewDefaultRegistry(), 0)
	srv := newBatchTestServer(t, newFakeStore(), ag, [2]string{"BTC-USDT", "900"})

	w := postForm(srv, "/wizard/batch/scan", url.Values{"utterance": {""}, "count": {"5"}})
	if !strings.Contains(w.Body.String(), "不能为空") {
		t.Errorf("空规则应该报错，实际：%s", w.Body.String())
	}
}

func TestHandleBatchScanTranslateFailsWhenScanReturnsNothing(t *testing.T) {
	stub := &agent.StubLLM{Responses: []string{configJSON}}
	ag := agent.New(stub, modules.NewDefaultRegistry(), 0)
	srv := newBatchTestServer(t, newFakeStore(), ag) // 没有任何 ticker

	w := postForm(srv, "/wizard/batch/scan", url.Values{"utterance": {"放量突破做多"}, "count": {"5"}})
	if !strings.Contains(w.Body.String(), "没有扫描到任何标的") {
		t.Errorf("扫描结果为空时应该报错，实际：%s", w.Body.String())
	}
	if len(stub.Calls) != 0 {
		t.Error("扫描失败时不应该调用 LLM")
	}
}

func TestHandleBatchScanClarifyKeepsSymbolsAcrossRounds(t *testing.T) {
	stub := &agent.StubLLM{Responses: []string{clarifyJSON, configJSON}}
	ag := agent.New(stub, modules.NewDefaultRegistry(), 0)
	srv := newBatchTestServer(t, newFakeStore(), ag,
		[2]string{"BTC-USDT", "900"}, [2]string{"ETH-USDT", "500"})

	first := postForm(srv, "/wizard/batch/scan", url.Values{"utterance": {"做多"}, "count": {"2"}})
	if !strings.Contains(first.Body.String(), "还需要你补充") {
		t.Fatalf("第一轮应该要求澄清，实际：%s", first.Body.String())
	}
	state := extractHiddenState(t, first.Body.String())

	second := postForm(srv, "/wizard/batch/clarify", url.Values{"answer": {"1 小时线"}, "state": {state}})
	if second.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", second.Code, second.Body.String())
	}
	body := second.Body.String()
	if !strings.Contains(body, "BTCUSDT") || !strings.Contains(body, "ETHUSDT") {
		t.Errorf("澄清后仍应展示第一轮扫描出的标的列表，实际：%s", body)
	}
	// 只应该调用一次 ListTickers（在 scan 那一步），clarify 不重新扫描——用调用 LLM 的
	// 次数间接验证流程正确，扫描次数没有直接钩子可测，这里主要靠标的列表内容不变来验证。
	if len(stub.Calls) != 2 {
		t.Fatalf("应调用 LLM 两次，实际 %d 次", len(stub.Calls))
	}
}

func TestHandleBatchScanConfirmCreatesOneDraftPerSymbol(t *testing.T) {
	stub := &agent.StubLLM{Responses: []string{configJSON}}
	ag := agent.New(stub, modules.NewDefaultRegistry(), 0)
	store := newFakeStore()
	srv := newBatchTestServer(t, store, ag,
		[2]string{"BTC-USDT", "900"}, [2]string{"ETH-USDT", "500"})

	scan := postForm(srv, "/wizard/batch/scan", url.Values{"utterance": {"放量突破做多"}, "count": {"2"}})
	state := extractHiddenState(t, scan.Body.String())

	confirm := postForm(srv, "/wizard/batch/confirm", url.Values{"decision": {"confirm"}, "state": {state}})
	if confirm.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", confirm.Code, confirm.Body.String())
	}
	body := confirm.Body.String()
	if !strings.Contains(body, "成功创建 2 份草稿，跳过 0 个") {
		t.Errorf("应报告成功创建 2 份，实际：%s", body)
	}

	if len(store.strategies) != 2 {
		t.Fatalf("store 里应有 2 份策略，实际 %d 份", len(store.strategies))
	}
	symbols := map[string]bool{}
	for _, cfg := range store.strategies {
		symbols[cfg.Symbol] = true
		if cfg.Name != "放量突破" {
			t.Errorf("Name 应该跟模板一致，实际 %q", cfg.Name)
		}
		if len(cfg.Modules) != 1 || cfg.Modules[0].Module != "volume_breakout" {
			t.Errorf("Modules 应该跟模板一致，实际 %+v", cfg.Modules)
		}
		if string(cfg.State) != "DRAFT" {
			t.Errorf("新生成的策略应该是 DRAFT 状态，实际 %s", cfg.State)
		}
	}
	if !symbols["BTCUSDT"] || !symbols["ETHUSDT"] {
		t.Errorf("应该分别为 BTCUSDT 和 ETHUSDT 各生成一份，实际标的集合：%v", symbols)
	}
}

func TestHandleBatchScanConfirmIsolatesPerSymbolFailures(t *testing.T) {
	stub := &agent.StubLLM{Responses: []string{configJSON}}
	ag := agent.New(stub, modules.NewDefaultRegistry(), 0)
	store := newFakeStore()
	srv := newBatchTestServer(t, store, ag, [2]string{"BTC-USDT", "900"})

	scan := postForm(srv, "/wizard/batch/scan", url.Values{"utterance": {"放量突破做多"}, "count": {"1"}})
	state := extractHiddenState(t, scan.Body.String())

	// 直接解码、篡改标的列表混入一个空字符串（strategy.Validate 会因为 Symbol 为空拒绝），
	// 重新编码——模拟"一批里有一个标的注定会校验失败"的场景，验证不拖累其它标的。
	ws, err := decodeState(state)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	ws.BatchSymbols = []string{"BTCUSDT", ""}
	tamperedState, err := encodeState(ws)
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}

	confirm := postForm(srv, "/wizard/batch/confirm", url.Values{"decision": {"confirm"}, "state": {tamperedState}})
	body := confirm.Body.String()
	if !strings.Contains(body, "成功创建 1 份草稿，跳过 1 个") {
		t.Errorf("应该 1 份成功、1 份跳过，实际：%s", body)
	}
	if len(store.strategies) != 1 {
		t.Fatalf("store 里应只有 1 份成功的策略，实际 %d 份", len(store.strategies))
	}
}

func TestHandleBatchScanConfirmCancelDoesNotTouchStore(t *testing.T) {
	stub := &agent.StubLLM{Responses: []string{configJSON}}
	ag := agent.New(stub, modules.NewDefaultRegistry(), 0)
	store := newFakeStore()
	srv := newBatchTestServer(t, store, ag, [2]string{"BTC-USDT", "900"})

	scan := postForm(srv, "/wizard/batch/scan", url.Values{"utterance": {"放量突破做多"}, "count": {"1"}})
	state := extractHiddenState(t, scan.Body.String())

	w := postForm(srv, "/wizard/batch/confirm", url.Values{"decision": {"cancel"}, "state": {state}})
	if !strings.Contains(w.Body.String(), "已取消") {
		t.Errorf("应提示已取消，实际：%s", w.Body.String())
	}
	if len(store.strategies) != 0 {
		t.Errorf("取消不应该写库，实际保存了 %d 份", len(store.strategies))
	}
}

func TestHandleBatchScanConfirmRejectsMissingState(t *testing.T) {
	srv := newBatchTestServer(t, newFakeStore(), agent.New(&agent.StubLLM{}, modules.NewDefaultRegistry(), 0))
	w := postForm(srv, "/wizard/batch/confirm", url.Values{"decision": {"confirm"}})
	if !strings.Contains(w.Body.String(), "重新开始") {
		t.Errorf("缺少 state 应提示重新开始，实际：%s", w.Body.String())
	}
}
