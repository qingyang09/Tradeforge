package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// realTickersResponseJSON 是从 GET /api/v5/market/tickers?instType=SPOT 抓到的真实响应
// 里挑出的几条，字段值照抄真实响应，只裁掉了 ListTickers 用不到的字段。
const realTickersResponseJSON = `{"code":"0","msg":"","data":[` +
	`{"instId":"BTC-USDT","last":"100150","volCcy24h":"850000000.5"},` +
	`{"instId":"ETH-USDT","last":"3500","volCcy24h":"320000000.25"},` +
	`{"instId":"DOGE-USDT","last":"0.15","volCcy24h":"45000000"}` +
	`]}`

func TestListTickersParsesRealResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v5/market/tickers") {
			t.Errorf("请求路径 = %s，期望 /api/v5/market/tickers 前缀", r.URL.Path)
		}
		w.Write([]byte(realTickersResponseJSON))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	got, err := c.ListTickers(context.Background())
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(got) != 3 {
		t.Fatalf("返回 %d 条，期望 3 条", len(got))
	}

	bySymbol := map[string]Ticker{}
	for _, tk := range got {
		bySymbol[tk.Symbol] = tk
	}
	// decimal.Decimal 内部含指针字段，用 == 比较的是指针身份不是数值，两次独立解析出的
	// "数值相等"的 Decimal 未必是同一个指针——必须用 Equal 方法按数值比较。
	wantVol := decimal.RequireFromString("850000000.5")
	btc, ok := bySymbol["BTCUSDT"]
	if !ok {
		t.Fatalf("解析结果里没有 BTCUSDT")
	}
	if !btc.Vol24hQuote.Equal(wantVol) {
		t.Errorf("BTC-USDT 的 Vol24hQuote = %s，期望 %s", btc.Vol24hQuote, wantVol)
	}
}

func TestListTickersRejectsErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":"50000","msg":"System error","data":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	if _, err := c.ListTickers(context.Background()); err == nil {
		t.Fatal("OKX 返回错误 code 时应报错")
	}
}

func TestListTickersSkipsRowsWithUnparseableVolume(t *testing.T) {
	body := `{"code":"0","msg":"","data":[` +
		`{"instId":"BTC-USDT","last":"100150","volCcy24h":"850000000.5"},` +
		`{"instId":"BROKEN-USDT","last":"1","volCcy24h":"not-a-number"}` +
		`]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	got, err := c.ListTickers(context.Background())
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(got) != 1 {
		t.Fatalf("成交额解析失败的行应被跳过而不是让整批失败，返回 %d 条，期望 1 条", len(got))
	}
	if got[0].Symbol != "BTCUSDT" {
		t.Errorf("剩下的应该是 BTCUSDT，实际 %q", got[0].Symbol)
	}
}
