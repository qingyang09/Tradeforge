package execution

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// realBinanceOrderResponseJSON 是币安测试网真实下单接口的响应形状（字段名/精度
// 照抄官方文档示例），不是随手编的假数据。
const realBinanceOrderResponseJSON = `{
	"symbol": "BTCUSDT", "orderId": 28457, "transactTime": 1507725176595,
	"executedQty": "0.10000000", "cummulativeQuoteQty": "10015.50000000",
	"status": "FILLED",
	"fills": [
		{"price": "100150.00000000", "qty": "0.06000000", "commission": "0.00006000", "commissionAsset": "BTC"},
		{"price": "100160.00000000", "qty": "0.04000000", "commission": "0.00004000", "commissionAsset": "BTC"}
	]
}`

func fakeBinanceServer(t *testing.T, body string, statusCode int, checkReq func(*http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if checkReq != nil {
			checkReq(r)
		}
		w.WriteHeader(statusCode)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// testBinanceBroker 绕开 NewBinanceTestnetBroker 的"必须含 testnet"安全检查来构造
// 测试用 broker——那道检查是给真实调用方防呆用的（防止改个配置就打到生产环境），
// 跟"用假 HTTP 服务器验证签名/解析逻辑"这个测试目的无关，不应该为了让测试过而放宽
// 生产代码里的这道闸门。
func testBinanceBroker(t *testing.T, baseURL string) *BinanceBroker {
	t.Helper()
	return &BinanceBroker{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     "test-key",
		apiSecret:  "test-secret",
		client:     http.DefaultClient,
		recvWindow: 5000,
	}
}

func TestBinanceBrokerPlaceOrderSignsAndSendsRequest(t *testing.T) {
	var gotHeader, gotMethod, gotPath string
	var gotQuery url.Values
	srv := fakeBinanceServer(t, realBinanceOrderResponseJSON, http.StatusOK, func(r *http.Request) {
		gotHeader = r.Header.Get("X-MBX-APIKEY")
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
	})
	b := testBinanceBroker(t, srv.URL)

	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
		Provenance: types.OrderProvenance{DecisionID: "d1"},
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}

	if gotHeader != "test-key" {
		t.Errorf("X-MBX-APIKEY = %q，期望 test-key", gotHeader)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("HTTP 方法 = %s，期望 POST", gotMethod)
	}
	if gotPath != "/api/v3/order" {
		t.Errorf("请求路径 = %s，期望 /api/v3/order", gotPath)
	}
	if gotQuery.Get("symbol") != "BTCUSDT" || gotQuery.Get("side") != "BUY" || gotQuery.Get("type") != "MARKET" {
		t.Errorf("下单参数不符：symbol=%s side=%s type=%s",
			gotQuery.Get("symbol"), gotQuery.Get("side"), gotQuery.Get("type"))
	}
	if gotQuery.Get("signature") == "" {
		t.Error("请求必须带签名，实际为空")
	}
	if gotQuery.Get("quantity") != "0.1" {
		t.Errorf("quantity = %s，期望 0.1", gotQuery.Get("quantity"))
	}
}

func TestBinanceBrokerPlaceOrderParsesFillsIntoOrder(t *testing.T) {
	srv := fakeBinanceServer(t, realBinanceOrderResponseJSON, http.StatusOK, nil)
	b := testBinanceBroker(t, srv.URL)

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
		t.Errorf("Quantity = %s，期望 0.1（来自 executedQty）", order.Quantity)
	}
	// 成交均价 = cummulativeQuoteQty / executedQty = 10015.5 / 0.1 = 100155。
	wantAvg := decimal.NewFromFloat(100155)
	if !order.FilledPrice.Equal(wantAvg) {
		t.Errorf("FilledPrice = %s，期望 %s", order.FilledPrice, wantAvg)
	}
	// 手续费 = 两笔 fills 的 commission 相加 = 0.00006 + 0.00004 = 0.0001。
	wantFee := decimal.NewFromFloat(0.0001)
	if !order.Fee.Equal(wantFee) {
		t.Errorf("Fee = %s，期望 %s（两笔 fills 的 commission 相加）", order.Fee, wantFee)
	}
	if order.ExchangeOrderID != "28457" {
		t.Errorf("ExchangeOrderID = %s，期望 28457", order.ExchangeOrderID)
	}
	if order.Mode != types.ModePaper {
		t.Errorf("Mode = %s，期望 PAPER（测试网恒按模拟盘对待）", order.Mode)
	}
	if order.Provenance.DecisionID != "d1" {
		t.Error("Provenance 应该原样透传")
	}
}

func TestBinanceBrokerPlaceOrderRejectedWhenNoFill(t *testing.T) {
	// executedQty 为 0：订单没有真的成交（比如市价单深度不够），应判定为 REJECTED
	// 而不是 FILLED——不能因为 HTTP 200 就假设成交了。
	body := `{"symbol":"BTCUSDT","orderId":1,"transactTime":1,"executedQty":"0","cummulativeQuoteQty":"0","status":"EXPIRED","fills":[]}`
	srv := fakeBinanceServer(t, body, http.StatusOK, nil)
	b := testBinanceBroker(t, srv.URL)

	order, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
	})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if order.Status != types.OrderRejected {
		t.Errorf("Status = %s，期望 REJECTED（executedQty 为 0）", order.Status)
	}
}

func TestBinanceBrokerPlaceOrderErrorsOnExchangeRejection(t *testing.T) {
	// 真实复现过的错误响应形状：code 非 0 表示交易所拒绝了这笔单
	// （比如余额不足、精度不对），必须报错，不能把这种响应当成功解析。
	body := `{"code":-2010,"msg":"Account has insufficient balance for requested action."}`
	srv := fakeBinanceServer(t, body, http.StatusBadRequest, nil)
	b := testBinanceBroker(t, srv.URL)

	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("交易所拒单时应该报错")
	}
	if !strings.Contains(err.Error(), "-2010") {
		t.Errorf("错误信息应带上交易所的错误码，实际：%v", err)
	}
}

func TestBinanceBrokerPlaceOrderRejectsNonPositiveQuantity(t *testing.T) {
	b := testBinanceBroker(t, "http://unused.invalid")
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.Zero, RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("下单数量非正时应该在发请求前就拒绝")
	}
}
