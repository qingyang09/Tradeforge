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

// testBybitBroker 直接构造 BybitBroker，绕开 NewBybitTestnetBroker 的必填密钥校验——
// 那道校验是给真实调用方防呆用的，跟"用假 HTTP 服务器验证签名/轮询/解析逻辑"这个
// 测试目的无关。轮询间隔调短，避免测试因为默认 300ms 的间隔跑得很慢。
func testBybitBroker(t *testing.T, baseURL string) *BybitBroker {
	t.Helper()
	return &BybitBroker{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       "test-key",
		apiSecret:    "test-secret",
		recvWindow:   "5000",
		client:       http.DefaultClient,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  200 * time.Millisecond,
	}
}

// fakeBybitServer 模拟 Bybit 的下单 + 查订单详情两个接口：下单接口只返回订单号，
// 查订单详情接口返回真正的成交结果——这跟 Bybit 的真实行为一致（不像币安一次
// 下单响应就带全部成交明细）。
func fakeBybitServer(t *testing.T, orderDetailBody string, orderDetailStatus int, checkPlaceReq func(*http.Request, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v5/order/create":
			body := make([]byte, r.ContentLength)
			r.Body.Read(body)
			if checkPlaceReq != nil {
				checkPlaceReq(r, string(body))
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"retCode":0,"retMsg":"OK","result":{"orderId":"1321003749386327552"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v5/order/realtime":
			w.WriteHeader(orderDetailStatus)
			w.Write([]byte(orderDetailBody))
		default:
			t.Fatalf("未预期的请求：%s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const realBybitFilledOrderJSON = `{
	"retCode": 0, "retMsg": "OK",
	"result": {
		"list": [{
			"orderId": "1321003749386327552", "symbol": "BTCUSDT",
			"avgPrice": "100155", "cumExecQty": "0.1",
			"cumExecFee": "0.0001", "orderStatus": "Filled", "createdTime": "1507725176595"
		}]
	}
}`

func TestBybitBrokerPlaceOrderSignsAndSendsRequest(t *testing.T) {
	var gotHeaders http.Header
	var gotBody string
	srv := fakeBybitServer(t, realBybitFilledOrderJSON, http.StatusOK, func(r *http.Request, body string) {
		gotHeaders = r.Header.Clone()
		gotBody = body
	})
	b := testBybitBroker(t, srv.URL)

	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
		Provenance: types.OrderProvenance{DecisionID: "d1"},
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}

	if gotHeaders.Get("X-BAPI-API-KEY") != "test-key" {
		t.Errorf("X-BAPI-API-KEY = %q，期望 test-key", gotHeaders.Get("X-BAPI-API-KEY"))
	}
	if gotHeaders.Get("X-BAPI-SIGN") == "" {
		t.Error("请求必须带签名，实际为空")
	}
	if gotHeaders.Get("X-BAPI-TIMESTAMP") == "" {
		t.Error("请求必须带时间戳，实际为空")
	}
	if !strings.Contains(gotBody, `"symbol":"BTCUSDT"`) {
		t.Errorf("下单请求体标的应为 BTCUSDT（不需要转换），实际：%s", gotBody)
	}
	if !strings.Contains(gotBody, `"side":"Buy"`) {
		t.Errorf("下单请求体方向应为 Buy，实际：%s", gotBody)
	}
	if !strings.Contains(gotBody, `"marketUnit":"baseCoin"`) {
		t.Errorf("必须强制 marketUnit=baseCoin，保证 qty 语义跟买卖方向无关，实际：%s", gotBody)
	}
	if !strings.Contains(gotBody, `"qty":"0.1"`) {
		t.Errorf("qty 应为下单数量 0.1，实际：%s", gotBody)
	}
}

func TestBybitBrokerPlaceOrderParsesFilledDetailIntoOrder(t *testing.T) {
	srv := fakeBybitServer(t, realBybitFilledOrderJSON, http.StatusOK, nil)
	b := testBybitBroker(t, srv.URL)

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
		t.Errorf("Quantity = %s，期望 0.1（来自 cumExecQty）", order.Quantity)
	}
	if !order.FilledPrice.Equal(decimal.NewFromInt(100155)) {
		t.Errorf("FilledPrice = %s，期望 100155（来自 avgPrice）", order.FilledPrice)
	}
	if !order.Fee.Equal(decimal.NewFromFloat(0.0001)) {
		t.Errorf("Fee = %s，期望 0.0001", order.Fee)
	}
	if order.ExchangeOrderID != "1321003749386327552" {
		t.Errorf("ExchangeOrderID = %s，期望 1321003749386327552", order.ExchangeOrderID)
	}
	if order.Mode != types.ModePaper {
		t.Errorf("Mode = %s，期望 PAPER（测试网恒按模拟盘对待）", order.Mode)
	}
	if order.Provenance.DecisionID != "d1" {
		t.Error("Provenance 应该原样透传")
	}
}

func TestBybitBrokerPlaceOrderRejectedWhenCancelled(t *testing.T) {
	body := `{"retCode":0,"retMsg":"OK","result":{"list":[{"orderId":"1","symbol":"BTCUSDT","avgPrice":"","cumExecQty":"0","cumExecFee":"0","orderStatus":"Cancelled","createdTime":"1"}]}}`
	srv := fakeBybitServer(t, body, http.StatusOK, nil)
	b := testBybitBroker(t, srv.URL)

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

func TestBybitBrokerPlaceOrderErrorsOnExchangeRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"retCode":110007,"retMsg":"ab not enough for new order","result":{}}`))
	}))
	t.Cleanup(srv.Close)
	b := testBybitBroker(t, srv.URL)

	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("交易所拒单时应该报错")
	}
	if !strings.Contains(err.Error(), "110007") {
		t.Errorf("错误信息应带上交易所的错误码，实际：%v", err)
	}
}

func TestBybitBrokerPlaceOrderRejectsNonPositiveQuantity(t *testing.T) {
	b := testBybitBroker(t, "http://unused.invalid")
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.Zero, RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("下单数量非正时应该在发请求前就拒绝")
	}
}
