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

// testBybitBroker builds a BybitBroker directly, bypassing
// NewBybitTestnetBroker's required-credentials check — that check
// fool-proofs real callers and has nothing to do with this test's purpose of
// verifying signing/polling/parsing logic against a fake HTTP server. The
// poll interval is shortened so the test doesn't run slowly because of the
// default 300ms interval.
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

// fakeBybitServer simulates Bybit's two endpoints — place order and get order
// detail: the place-order endpoint only returns an order ID, and the
// order-detail endpoint returns the actual fill result — matching Bybit's
// real behavior (unlike Binance, which returns all fill details in one
// response).
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
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
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
		t.Fatalf("unexpected error: %v", err)
	}

	if gotHeaders.Get("X-BAPI-API-KEY") != "test-key" {
		t.Errorf("X-BAPI-API-KEY = %q, want test-key", gotHeaders.Get("X-BAPI-API-KEY"))
	}
	if gotHeaders.Get("X-BAPI-SIGN") == "" {
		t.Error("request must carry a signature, got empty")
	}
	if gotHeaders.Get("X-BAPI-TIMESTAMP") == "" {
		t.Error("request must carry a timestamp, got empty")
	}
	if !strings.Contains(gotBody, `"symbol":"BTCUSDT"`) {
		t.Errorf("order request body symbol should be BTCUSDT (no conversion needed), got: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"side":"Buy"`) {
		t.Errorf("order request body side should be Buy, got: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"marketUnit":"baseCoin"`) {
		t.Errorf("must force marketUnit=baseCoin so qty's meaning is independent of buy/sell side, got: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"qty":"0.1"`) {
		t.Errorf("qty should be the order quantity 0.1, got: %s", gotBody)
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
		t.Fatalf("unexpected error: %v", err)
	}

	if order.Status != types.OrderFilled {
		t.Errorf("Status = %s, want FILLED", order.Status)
	}
	if !order.Quantity.Equal(decimal.NewFromFloat(0.1)) {
		t.Errorf("Quantity = %s, want 0.1 (from cumExecQty)", order.Quantity)
	}
	if !order.FilledPrice.Equal(decimal.NewFromInt(100155)) {
		t.Errorf("FilledPrice = %s, want 100155 (from avgPrice)", order.FilledPrice)
	}
	if !order.Fee.Equal(decimal.NewFromFloat(0.0001)) {
		t.Errorf("Fee = %s, want 0.0001", order.Fee)
	}
	if order.ExchangeOrderID != "1321003749386327552" {
		t.Errorf("ExchangeOrderID = %s, want 1321003749386327552", order.ExchangeOrderID)
	}
	if order.Mode != types.ModePaper {
		t.Errorf("Mode = %s, want PAPER (testnet is always treated as paper)", order.Mode)
	}
	if order.Provenance.DecisionID != "d1" {
		t.Error("Provenance should be passed through unchanged")
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
		t.Fatalf("unexpected error: %v", err)
	}
	if order.Status != types.OrderRejected {
		t.Errorf("Status = %s, want REJECTED (order was cancelled, never filled)", order.Status)
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
		t.Fatal("exchange rejection should produce an error")
	}
	if !strings.Contains(err.Error(), "110007") {
		t.Errorf("error message should include the exchange's error code, got: %v", err)
	}
}

func TestBybitBrokerPlaceOrderRejectsNonPositiveQuantity(t *testing.T) {
	b := testBybitBroker(t, "http://unused.invalid")
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.Zero, RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("a non-positive order quantity should be rejected before the request is even sent")
	}
}
