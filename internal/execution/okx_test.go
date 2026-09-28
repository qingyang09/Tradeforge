package execution

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// testOKXBroker 直接构造 OKXBroker，绕开 NewOKXDemoBroker 的必填密钥校验——
// 那道校验是给真实调用方防呆用的，跟"用假 HTTP 服务器验证签名/轮询/解析逻辑"
// 这个测试目的无关。轮询间隔调短，避免测试因为默认 300ms 的间隔跑得很慢。
func testOKXBroker(t *testing.T, baseURL string) *OKXBroker {
	t.Helper()
	return &OKXBroker{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       "test-key",
		apiSecret:    "test-secret",
		passphrase:   "test-pass",
		client:       http.DefaultClient,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  200 * time.Millisecond,
	}
}

// fakeOKXServer 模拟 OKX 的下单 + 查订单详情两个接口：下单接口只返回订单号，
// 查订单详情接口返回真正的成交结果——这跟 OKX 的真实行为一致（不像币安一次
// 下单响应就带全部成交明细）。
func fakeOKXServer(t *testing.T, orderDetailBody string, orderDetailStatus int, checkPlaceReq func(*http.Request, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v5/trade/order":
			body := make([]byte, r.ContentLength)
			r.Body.Read(body)
			if checkPlaceReq != nil {
				checkPlaceReq(r, string(body))
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"312269865356374016","sCode":"0","sMsg":""}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/trade/order":
			w.WriteHeader(orderDetailStatus)
			w.Write([]byte(orderDetailBody))
		default:
			t.Fatalf("未预期的请求：%s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const realOKXFilledOrderJSON = `{
	"code": "0", "msg": "",
	"data": [{
		"instId": "BTC-USDT", "ordId": "312269865356374016",
		"avgPx": "100155", "accFillSz": "0.1",
		"fee": "-0.0001", "state": "filled", "cTime": "1507725176595"
	}]
}`

func TestOKXBrokerPlaceOrderSignsAndSendsRequest(t *testing.T) {
	var gotHeaders http.Header
	var gotBody string
	srv := fakeOKXServer(t, realOKXFilledOrderJSON, http.StatusOK, func(r *http.Request, body string) {
		gotHeaders = r.Header.Clone()
		gotBody = body
	})
	b := testOKXBroker(t, srv.URL)

	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
		Provenance: types.OrderProvenance{DecisionID: "d1"},
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}

	if gotHeaders.Get("OK-ACCESS-KEY") != "test-key" {
		t.Errorf("OK-ACCESS-KEY = %q，期望 test-key", gotHeaders.Get("OK-ACCESS-KEY"))
	}
	if gotHeaders.Get("OK-ACCESS-PASSPHRASE") != "test-pass" {
		t.Errorf("OK-ACCESS-PASSPHRASE = %q，期望 test-pass", gotHeaders.Get("OK-ACCESS-PASSPHRASE"))
	}
	if gotHeaders.Get("OK-ACCESS-SIGN") == "" {
		t.Error("请求必须带签名，实际为空")
	}
	if gotHeaders.Get("x-simulated-trading") != "1" {
		t.Errorf("必须带模拟盘标记 x-simulated-trading: 1，实际 %q", gotHeaders.Get("x-simulated-trading"))
	}
	if !strings.Contains(gotBody, `"instId":"BTC-USDT"`) {
		t.Errorf("下单请求体里标的应转换成 BTC-USDT，实际：%s", gotBody)
	}
	if !strings.Contains(gotBody, `"side":"buy"`) {
		t.Errorf("下单请求体方向应为 buy，实际：%s", gotBody)
	}
	if !strings.Contains(gotBody, `"tgtCcy":"base_ccy"`) {
		t.Errorf("必须强制 tgtCcy=base_ccy，保证 sz 语义跟买卖方向无关，实际：%s", gotBody)
	}
	if !strings.Contains(gotBody, `"sz":"0.1"`) {
		t.Errorf("sz 应为下单数量 0.1，实际：%s", gotBody)
	}
}

func TestOKXBrokerPlaceOrderParsesFilledDetailIntoOrder(t *testing.T) {
	srv := fakeOKXServer(t, realOKXFilledOrderJSON, http.StatusOK, nil)
	b := testOKXBroker(t, srv.URL)

	order, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
		Provenance: types.OrderProvenance{DecisionID: "d1"},
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}

	if order.Status != types.OrderFilled {
		t.Errorf("Status = %s，期望 FILLED", order.Status)
	}
	if !order.Quantity.Equal(decimal.NewFromFloat(0.1)) {
		t.Errorf("Quantity = %s，期望 0.1（来自 accFillSz）", order.Quantity)
	}
	if !order.FilledPrice.Equal(decimal.NewFromInt(100155)) {
		t.Errorf("FilledPrice = %s，期望 100155（来自 avgPx）", order.FilledPrice)
	}
	// OKX 的 fee 是负数（-0.0001，表示扣费），Order.Fee 应存成非负的 0.0001。
	if !order.Fee.Equal(decimal.NewFromFloat(0.0001)) {
		t.Errorf("Fee = %s，期望 0.0001（fee 取绝对值）", order.Fee)
	}
	if order.ExchangeOrderID != "312269865356374016" {
		t.Errorf("ExchangeOrderID = %s，期望 312269865356374016", order.ExchangeOrderID)
	}
	if order.Mode != types.ModePaper {
		t.Errorf("Mode = %s，期望 PAPER（模拟盘恒按模拟盘对待）", order.Mode)
	}
	if order.Provenance.DecisionID != "d1" {
		t.Error("Provenance 应该原样透传")
	}
}

func TestOKXBrokerPlaceOrderRejectedWhenCanceled(t *testing.T) {
	body := `{"code":"0","msg":"","data":[{"instId":"BTC-USDT","ordId":"1","avgPx":"","accFillSz":"0","fee":"0","state":"canceled","cTime":"1"}]}`
	srv := fakeOKXServer(t, body, http.StatusOK, nil)
	b := testOKXBroker(t, srv.URL)

	order, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if order.Status != types.OrderRejected {
		t.Errorf("Status = %s，期望 REJECTED（订单被取消、未成交）", order.Status)
	}
}

func TestOKXBrokerPlaceOrderErrorsOnExchangeRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":"1","msg":"Operation failed.","data":[{"ordId":"","sCode":"51008","sMsg":"Order failed. Insufficient balance."}]}`))
	}))
	t.Cleanup(srv.Close)
	b := testOKXBroker(t, srv.URL)

	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("交易所拒单时应该报错")
	}
	if !strings.Contains(err.Error(), "OKX") {
		t.Errorf("错误信息应说明是 OKX 拒单，实际：%v", err)
	}
}

func TestOKXBrokerPlaceOrderRejectsNonPositiveQuantity(t *testing.T) {
	b := testOKXBroker(t, "http://unused.invalid")
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.Zero, RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("下单数量非正时应该在发请求前就拒绝")
	}
}

func TestOKXBrokerPlaceOrderRejectsUnknownSymbol(t *testing.T) {
	b := testOKXBroker(t, "http://unused.invalid")
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "NOTASYMBOL", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("无法识别计价货币的标的应该被拒绝")
	}
}
