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

// testBitgetBroker 直接构造 BitgetBroker，绕开 NewBitgetDemoBroker 的必填密钥校验——
// 那道校验是给真实调用方防呆用的，跟"用假 HTTP 服务器验证签名/轮询/解析逻辑"这个
// 测试目的无关。轮询间隔调短，避免测试因为默认 300ms 的间隔跑得很慢。
func testBitgetBroker(t *testing.T, baseURL string) *BitgetBroker {
	t.Helper()
	return &BitgetBroker{
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       "test-key",
		apiSecret:    "test-secret",
		passphrase:   "test-pass",
		client:       http.DefaultClient,
		pollInterval: 5 * time.Millisecond,
		pollTimeout:  200 * time.Millisecond,
	}
}

// fakeBitgetServer 模拟 Bitget 的下单 + 查订单详情两个接口：下单接口只返回订单号，
// 查订单详情接口返回真正的成交结果（跟 OKX/Bybit 是同一个模式，不像币安一次
// 下单响应就带全部成交明细）。
func fakeBitgetServer(t *testing.T, orderDetailBody string, orderDetailStatus int, checkPlaceReq func(*http.Request, string)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/spot/trade/place-order":
			body := make([]byte, r.ContentLength)
			r.Body.Read(body)
			if checkPlaceReq != nil {
				checkPlaceReq(r, string(body))
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"code":"00000","msg":"success","data":{"orderId":"1234567890"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/spot/trade/orderInfo":
			w.WriteHeader(orderDetailStatus)
			w.Write([]byte(orderDetailBody))
		default:
			t.Fatalf("未预期的请求：%s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

const realBitgetFilledOrderJSON = `{
	"code": "00000", "msg": "success",
	"data": [{
		"orderId": "1234567890", "symbol": "BTCUSDT",
		"priceAvg": "100155", "baseVolume": "0.1", "status": "filled", "cTime": "1507725176595",
		"feeDetail": {"USDT": {"totalFee": "-0.0001"}}
	}]
}`

func TestBitgetBrokerPlaceOrderSignsAndSendsRequest(t *testing.T) {
	var gotHeaders http.Header
	var gotBody string
	srv := fakeBitgetServer(t, realBitgetFilledOrderJSON, http.StatusOK, func(r *http.Request, body string) {
		gotHeaders = r.Header.Clone()
		gotBody = body
	})
	b := testBitgetBroker(t, srv.URL)

	// 卖单：size 应该直接等于基础货币数量，不做换算。
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideSell,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
		Provenance: types.OrderProvenance{DecisionID: "d1"},
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}

	if gotHeaders.Get("ACCESS-KEY") != "test-key" {
		t.Errorf("ACCESS-KEY = %q，期望 test-key", gotHeaders.Get("ACCESS-KEY"))
	}
	if gotHeaders.Get("ACCESS-PASSPHRASE") != "test-pass" {
		t.Errorf("ACCESS-PASSPHRASE = %q，期望 test-pass", gotHeaders.Get("ACCESS-PASSPHRASE"))
	}
	if gotHeaders.Get("ACCESS-SIGN") == "" {
		t.Error("请求必须带签名，实际为空")
	}
	if gotHeaders.Get("paptrading") != "1" {
		t.Errorf("必须带模拟盘标记 paptrading: 1，实际 %q", gotHeaders.Get("paptrading"))
	}
	if !strings.Contains(gotBody, `"symbol":"BTCUSDT"`) {
		t.Errorf("下单请求体标的应为 BTCUSDT（不需要转换），实际：%s", gotBody)
	}
	if !strings.Contains(gotBody, `"side":"sell"`) {
		t.Errorf("下单请求体方向应为 sell，实际：%s", gotBody)
	}
	if !strings.Contains(gotBody, `"size":"0.1"`) {
		t.Errorf("卖单 size 应直接等于基础货币数量 0.1，实际：%s", gotBody)
	}
}

// TestBitgetBrokerPlaceOrderConvertsBuySizeToQuoteCurrency 覆盖 Bitget 这里特有的
// 不对称：市价买单的 size 是计价货币金额，不是基础货币数量，需要用参考价换算。
func TestBitgetBrokerPlaceOrderConvertsBuySizeToQuoteCurrency(t *testing.T) {
	var gotBody string
	srv := fakeBitgetServer(t, realBitgetFilledOrderJSON, http.StatusOK, func(r *http.Request, body string) {
		gotBody = body
	})
	b := testBitgetBroker(t, srv.URL)

	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	// 0.1 (基础货币) * 100000 (参考价) = 10000 (计价货币金额)。
	if !strings.Contains(gotBody, `"size":"10000"`) {
		t.Errorf("买单 size 应该是数量*参考价换算出的计价货币金额 10000，实际：%s", gotBody)
	}
}

func TestBitgetBrokerPlaceOrderRejectsBuyWithoutRefPrice(t *testing.T) {
	b := testBitgetBroker(t, "http://unused.invalid")
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.Zero,
	})
	if err == nil {
		t.Fatal("买单没有参考价时应该在发请求前就拒绝（换算不出计价货币金额）")
	}
}

func TestBitgetBrokerPlaceOrderParsesFilledDetailIntoOrder(t *testing.T) {
	srv := fakeBitgetServer(t, realBitgetFilledOrderJSON, http.StatusOK, nil)
	b := testBitgetBroker(t, srv.URL)

	order, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideSell,
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
		t.Errorf("Quantity = %s，期望 0.1（来自 baseVolume）", order.Quantity)
	}
	if !order.FilledPrice.Equal(decimal.NewFromInt(100155)) {
		t.Errorf("FilledPrice = %s，期望 100155（来自 priceAvg）", order.FilledPrice)
	}
	if !order.Fee.Equal(decimal.NewFromFloat(0.0001)) {
		t.Errorf("Fee = %s，期望 0.0001（feeDetail 里 totalFee 取绝对值）", order.Fee)
	}
	if order.ExchangeOrderID != "1234567890" {
		t.Errorf("ExchangeOrderID = %s，期望 1234567890", order.ExchangeOrderID)
	}
	if order.Mode != types.ModePaper {
		t.Errorf("Mode = %s，期望 PAPER（模拟盘恒按模拟盘对待）", order.Mode)
	}
	if order.Provenance.DecisionID != "d1" {
		t.Error("Provenance 应该原样透传")
	}
}

func TestBitgetBrokerPlaceOrderRejectedWhenCancelled(t *testing.T) {
	body := `{"code":"00000","msg":"success","data":[{"orderId":"1","symbol":"BTCUSDT","priceAvg":"","baseVolume":"0","status":"cancelled","cTime":"1"}]}`
	srv := fakeBitgetServer(t, body, http.StatusOK, nil)
	b := testBitgetBroker(t, srv.URL)

	order, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideSell,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if order.Status != types.OrderRejected {
		t.Errorf("Status = %s，期望 REJECTED（订单被取消、未成交）", order.Status)
	}
}

func TestBitgetBrokerPlaceOrderErrorsOnExchangeRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"code":"40762","msg":"The order size is greater than the max order size."}`))
	}))
	t.Cleanup(srv.Close)
	b := testBitgetBroker(t, srv.URL)

	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideSell,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("交易所拒单时应该报错")
	}
	if !strings.Contains(err.Error(), "40762") {
		t.Errorf("错误信息应带上交易所的错误码，实际：%v", err)
	}
}

func TestBitgetBrokerPlaceOrderRejectsNonPositiveQuantity(t *testing.T) {
	b := testBitgetBroker(t, "http://unused.invalid")
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideSell,
		Quantity: decimal.Zero, RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("下单数量非正时应该在发请求前就拒绝")
	}
}
