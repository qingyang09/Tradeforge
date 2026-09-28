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

// testOKXBroker builds an OKXBroker directly, bypassing NewOKXDemoBroker's
// required-credentials check — that check fool-proofs real callers and has
// nothing to do with this test's purpose of verifying signing/polling/parsing
// logic against a fake HTTP server. The poll interval is shortened so the
// test doesn't run slowly because of the default 300ms interval.
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

// fakeOKXServer simulates OKX's two endpoints — place order and get order
// detail: the place-order endpoint only returns an order ID, and the
// order-detail endpoint returns the actual fill result — matching OKX's real
// behavior (unlike Binance, which returns all fill details in one response).
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
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
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
		t.Fatalf("unexpected error: %v", err)
	}

	if gotHeaders.Get("OK-ACCESS-KEY") != "test-key" {
		t.Errorf("OK-ACCESS-KEY = %q, want test-key", gotHeaders.Get("OK-ACCESS-KEY"))
	}
	if gotHeaders.Get("OK-ACCESS-PASSPHRASE") != "test-pass" {
		t.Errorf("OK-ACCESS-PASSPHRASE = %q, want test-pass", gotHeaders.Get("OK-ACCESS-PASSPHRASE"))
	}
	if gotHeaders.Get("OK-ACCESS-SIGN") == "" {
		t.Error("request must carry a signature, got empty")
	}
	if gotHeaders.Get("x-simulated-trading") != "1" {
		t.Errorf("must carry the demo-trading flag x-simulated-trading: 1, got %q", gotHeaders.Get("x-simulated-trading"))
	}
	if !strings.Contains(gotBody, `"instId":"BTC-USDT"`) {
		t.Errorf("order request body should convert the symbol to BTC-USDT, got: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"side":"buy"`) {
		t.Errorf("order request body side should be buy, got: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"tgtCcy":"base_ccy"`) {
		t.Errorf("must force tgtCcy=base_ccy so sz's meaning is independent of buy/sell side, got: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"sz":"0.1"`) {
		t.Errorf("sz should be the order quantity 0.1, got: %s", gotBody)
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
		t.Fatalf("unexpected error: %v", err)
	}

	if order.Status != types.OrderFilled {
		t.Errorf("Status = %s, want FILLED", order.Status)
	}
	if !order.Quantity.Equal(decimal.NewFromFloat(0.1)) {
		t.Errorf("Quantity = %s, want 0.1 (from accFillSz)", order.Quantity)
	}
	if !order.FilledPrice.Equal(decimal.NewFromInt(100155)) {
		t.Errorf("FilledPrice = %s, want 100155 (from avgPx)", order.FilledPrice)
	}
	// OKX's fee is negative (-0.0001, meaning a charge); Order.Fee should be stored as the non-negative 0.0001.
	if !order.Fee.Equal(decimal.NewFromFloat(0.0001)) {
		t.Errorf("Fee = %s, want 0.0001 (fee's absolute value)", order.Fee)
	}
	if order.ExchangeOrderID != "312269865356374016" {
		t.Errorf("ExchangeOrderID = %s, want 312269865356374016", order.ExchangeOrderID)
	}
	if order.Mode != types.ModePaper {
		t.Errorf("Mode = %s, want PAPER (demo trading is always treated as paper)", order.Mode)
	}
	if order.Provenance.DecisionID != "d1" {
		t.Error("Provenance should be passed through unchanged")
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
		t.Fatalf("unexpected error: %v", err)
	}
	if order.Status != types.OrderRejected {
		t.Errorf("Status = %s, want REJECTED (order was canceled, never filled)", order.Status)
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
		t.Fatal("exchange rejection should produce an error")
	}
	if !strings.Contains(err.Error(), "OKX") {
		t.Errorf("error message should state that OKX rejected the order, got: %v", err)
	}
}

func TestOKXBrokerPlaceOrderRejectsNonPositiveQuantity(t *testing.T) {
	b := testOKXBroker(t, "http://unused.invalid")
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.Zero, RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("a non-positive order quantity should be rejected before the request is even sent")
	}
}

func TestOKXBrokerPlaceOrderRejectsUnknownSymbol(t *testing.T) {
	b := testOKXBroker(t, "http://unused.invalid")
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "NOTASYMBOL", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("a symbol whose quote currency can't be identified should be rejected")
	}
}
