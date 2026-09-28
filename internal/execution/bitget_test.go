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

// testBitgetBroker builds a BitgetBroker directly, bypassing
// NewBitgetDemoBroker's required-credentials check — that check fool-proofs
// real callers and has nothing to do with this test's purpose of verifying
// signing/polling/parsing logic against a fake HTTP server. The poll
// interval is shortened so the test doesn't run slowly because of the
// default 300ms interval.
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

// fakeBitgetServer simulates Bitget's two endpoints — place order and get
// order detail: the place-order endpoint only returns an order ID, and the
// order-detail endpoint returns the actual fill result (same pattern as
// OKX/Bybit, unlike Binance which returns all fill details in one response).
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
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
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

	// Sell order: size should equal the base-currency quantity directly, with no conversion.
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideSell,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
		Provenance: types.OrderProvenance{DecisionID: "d1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotHeaders.Get("ACCESS-KEY") != "test-key" {
		t.Errorf("ACCESS-KEY = %q, want test-key", gotHeaders.Get("ACCESS-KEY"))
	}
	if gotHeaders.Get("ACCESS-PASSPHRASE") != "test-pass" {
		t.Errorf("ACCESS-PASSPHRASE = %q, want test-pass", gotHeaders.Get("ACCESS-PASSPHRASE"))
	}
	if gotHeaders.Get("ACCESS-SIGN") == "" {
		t.Error("request must carry a signature, got empty")
	}
	if gotHeaders.Get("paptrading") != "1" {
		t.Errorf("must carry the demo-trading flag paptrading: 1, got %q", gotHeaders.Get("paptrading"))
	}
	if !strings.Contains(gotBody, `"symbol":"BTCUSDT"`) {
		t.Errorf("order request body symbol should be BTCUSDT (no conversion needed), got: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"side":"sell"`) {
		t.Errorf("order request body side should be sell, got: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"size":"0.1"`) {
		t.Errorf("sell order size should equal the base-currency quantity 0.1 directly, got: %s", gotBody)
	}
}

// TestBitgetBrokerPlaceOrderConvertsBuySizeToQuoteCurrency covers Bitget's own
// asymmetry: a market buy's size is a quote-currency amount, not a
// base-currency quantity, and needs converting via the reference price.
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
		t.Fatalf("unexpected error: %v", err)
	}
	// 0.1 (base currency) * 100000 (reference price) = 10000 (quote-currency amount).
	if !strings.Contains(gotBody, `"size":"10000"`) {
		t.Errorf("buy order size should be the quote-currency amount 10000 from quantity*refPrice, got: %s", gotBody)
	}
}

func TestBitgetBrokerPlaceOrderRejectsBuyWithoutRefPrice(t *testing.T) {
	b := testBitgetBroker(t, "http://unused.invalid")
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.Zero,
	})
	if err == nil {
		t.Fatal("a buy order with no reference price should be rejected before the request is sent (can't convert to a quote-currency amount)")
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
		t.Fatalf("unexpected error: %v", err)
	}

	if order.Status != types.OrderFilled {
		t.Errorf("Status = %s, want FILLED", order.Status)
	}
	if !order.Quantity.Equal(decimal.NewFromFloat(0.1)) {
		t.Errorf("Quantity = %s, want 0.1 (from baseVolume)", order.Quantity)
	}
	if !order.FilledPrice.Equal(decimal.NewFromInt(100155)) {
		t.Errorf("FilledPrice = %s, want 100155 (from priceAvg)", order.FilledPrice)
	}
	if !order.Fee.Equal(decimal.NewFromFloat(0.0001)) {
		t.Errorf("Fee = %s, want 0.0001 (feeDetail's totalFee, absolute value)", order.Fee)
	}
	if order.ExchangeOrderID != "1234567890" {
		t.Errorf("ExchangeOrderID = %s, want 1234567890", order.ExchangeOrderID)
	}
	if order.Mode != types.ModePaper {
		t.Errorf("Mode = %s, want PAPER (demo trading is always treated as paper)", order.Mode)
	}
	if order.Provenance.DecisionID != "d1" {
		t.Error("Provenance should be passed through unchanged")
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
		t.Fatalf("unexpected error: %v", err)
	}
	if order.Status != types.OrderRejected {
		t.Errorf("Status = %s, want REJECTED (order was cancelled, never filled)", order.Status)
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
		t.Fatal("exchange rejection should produce an error")
	}
	if !strings.Contains(err.Error(), "40762") {
		t.Errorf("error message should include the exchange's error code, got: %v", err)
	}
}

func TestBitgetBrokerPlaceOrderRejectsNonPositiveQuantity(t *testing.T) {
	b := testBitgetBroker(t, "http://unused.invalid")
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideSell,
		Quantity: decimal.Zero, RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("a non-positive order quantity should be rejected before the request is even sent")
	}
}
