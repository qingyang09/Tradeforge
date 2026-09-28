package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// realInstrumentsResponseJSON 是从 GET /api/v5/public/instruments?instType=SPOT 抓到的
// 真实响应里挑出来的几条（BTC-USDT、几个 ETH 计价对、一个基础货币恰好以 "ETH" 开头
// 但其实是另一个币种的 ETHFI-USDT、一条非 live 状态的），字段值照抄真实响应，
// 只是裁掉了 ListInstruments 用不到的字段。
const realInstrumentsResponseJSON = `{"code":"0","msg":"","data":[` +
	`{"instId":"BTC-USDT","baseCcy":"BTC","quoteCcy":"USDT","state":"live"},` +
	`{"instId":"ETH-USDT","baseCcy":"ETH","quoteCcy":"USDT","state":"live"},` +
	`{"instId":"ETH-USDC","baseCcy":"ETH","quoteCcy":"USDC","state":"live"},` +
	`{"instId":"ETH-BTC","baseCcy":"ETH","quoteCcy":"BTC","state":"live"},` +
	`{"instId":"ETHW-USDT","baseCcy":"ETHW","quoteCcy":"USDT","state":"live"},` +
	`{"instId":"ETHFI-USDT","baseCcy":"ETHFI","quoteCcy":"USDT","state":"live"},` +
	`{"instId":"XTESTA-USDT","baseCcy":"XTESTA","quoteCcy":"USDT","state":"rebase"}` +
	`]}`

func TestListInstrumentsParsesRealResponseAndFiltersNonLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v5/public/instruments") {
			t.Errorf("请求路径 = %s，期望 /api/v5/public/instruments 前缀", r.URL.Path)
		}
		w.Write([]byte(realInstrumentsResponseJSON))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	got, err := c.ListInstruments(context.Background())
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}

	// state="rebase" 的 XTESTA-USDT 必须被过滤掉。
	if len(got) != 6 {
		t.Fatalf("返回 %d 条，期望 6 条（真实响应里有 7 条，1 条非 live 应被过滤）", len(got))
	}

	byInstID := map[string]Instrument{}
	for _, inst := range got {
		byInstID[inst.Symbol] = inst
	}

	want := Instrument{Symbol: "ETHUSDT", Base: "ETH", Quote: "USDT"}
	if got := byInstID["ETHUSDT"]; got != want {
		t.Errorf("ETH-USDT 解析结果 = %+v，期望 %+v", got, want)
	}

	// ETHFI-USDT 的基础货币是 "ETHFI"，不是 "ETH" 前面接了别的东西——
	// 这是标的搜索排序需要能区分"基础货币精确匹配"和"字符串里恰好包含"的关键案例。
	if got := byInstID["ETHFIUSDT"].Base; got != "ETHFI" {
		t.Errorf("ETHFI-USDT 的 Base = %q，期望 %q（不能被误判成 ETH 的前缀匹配）", got, "ETHFI")
	}

	for _, inst := range got {
		if inst.Symbol == "XTESTAUSDT" {
			t.Errorf("非 live 状态的标的 %s 不应出现在结果里", inst.Symbol)
		}
	}
}

func TestListInstrumentsRejectsErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":"50000","msg":"System error","data":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	if _, err := c.ListInstruments(context.Background()); err == nil {
		t.Fatal("OKX 返回错误 code 时应报错")
	}
}
