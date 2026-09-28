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

// realBinanceOrderResponseJSON is the actual response shape of the Binance
// testnet order-placement endpoint (field names/precision copied straight
// from the official docs' example) — not made-up fake data.
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

// testBinanceBroker builds a test broker by bypassing NewBinanceTestnetBroker's
// "must contain testnet" safety check — that check exists to fool-proof real
// callers (so a config edit can't accidentally hit production), and has
// nothing to do with this test's purpose of verifying signing/parsing logic
// against a fake HTTP server. We should not loosen that production-code gate
// just to make the test pass.
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
		t.Fatalf("unexpected error: %v", err)
	}

	if gotHeader != "test-key" {
		t.Errorf("X-MBX-APIKEY = %q, want test-key", gotHeader)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("HTTP method = %s, want POST", gotMethod)
	}
	if gotPath != "/api/v3/order" {
		t.Errorf("request path = %s, want /api/v3/order", gotPath)
	}
	if gotQuery.Get("symbol") != "BTCUSDT" || gotQuery.Get("side") != "BUY" || gotQuery.Get("type") != "MARKET" {
		t.Errorf("order params mismatch: symbol=%s side=%s type=%s",
			gotQuery.Get("symbol"), gotQuery.Get("side"), gotQuery.Get("type"))
	}
	if gotQuery.Get("signature") == "" {
		t.Error("request must carry a signature, got empty")
	}
	if gotQuery.Get("quantity") != "0.1" {
		t.Errorf("quantity = %s, want 0.1", gotQuery.Get("quantity"))
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
		t.Fatalf("unexpected error: %v", err)
	}

	if order.Status != types.OrderFilled {
		t.Errorf("Status = %s, want FILLED", order.Status)
	}
	if !order.Quantity.Equal(decimal.NewFromFloat(0.1)) {
		t.Errorf("Quantity = %s, want 0.1 (from executedQty)", order.Quantity)
	}
	// Average fill price = cummulativeQuoteQty / executedQty = 10015.5 / 0.1 = 100155.
	wantAvg := decimal.NewFromFloat(100155)
	if !order.FilledPrice.Equal(wantAvg) {
		t.Errorf("FilledPrice = %s, want %s", order.FilledPrice, wantAvg)
	}
	// Fee = sum of commission across both fills = 0.00006 + 0.00004 = 0.0001.
	wantFee := decimal.NewFromFloat(0.0001)
	if !order.Fee.Equal(wantFee) {
		t.Errorf("Fee = %s, want %s (sum of commission across both fills)", order.Fee, wantFee)
	}
	if order.ExchangeOrderID != "28457" {
		t.Errorf("ExchangeOrderID = %s, want 28457", order.ExchangeOrderID)
	}
	if order.Mode != types.ModePaper {
		t.Errorf("Mode = %s, want PAPER (testnet is always treated as paper)", order.Mode)
	}
	if order.Provenance.DecisionID != "d1" {
		t.Error("Provenance should be passed through unchanged")
	}
}

func TestBinanceBrokerPlaceOrderRejectedWhenNoFill(t *testing.T) {
	// executedQty is 0: the order didn't actually fill (e.g. market order with
	// insufficient depth), which should be judged REJECTED, not FILLED — an
	// HTTP 200 alone must not be taken to mean it filled.
	body := `{"symbol":"BTCUSDT","orderId":1,"transactTime":1,"executedQty":"0","cummulativeQuoteQty":"0","status":"EXPIRED","fills":[]}`
	srv := fakeBinanceServer(t, body, http.StatusOK, nil)
	b := testBinanceBroker(t, srv.URL)

	order, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if order.Status != types.OrderRejected {
		t.Errorf("Status = %s, want REJECTED (executedQty is 0)", order.Status)
	}
}

func TestBinanceBrokerPlaceOrderErrorsOnExchangeRejection(t *testing.T) {
	// Real response shape reproduced from an actual failure: a non-zero code
	// means the exchange rejected the order (e.g. insufficient balance, bad
	// precision), which must error rather than be parsed as a success.
	body := `{"code":-2010,"msg":"Account has insufficient balance for requested action."}`
	srv := fakeBinanceServer(t, body, http.StatusBadRequest, nil)
	b := testBinanceBroker(t, srv.URL)

	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.NewFromFloat(0.1), RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("exchange rejection should produce an error")
	}
	if !strings.Contains(err.Error(), "-2010") {
		t.Errorf("error message should include the exchange's error code, got: %v", err)
	}
}

func TestBinanceBrokerPlaceOrderRejectsNonPositiveQuantity(t *testing.T) {
	b := testBinanceBroker(t, "http://unused.invalid")
	_, err := b.PlaceOrder(t.Context(), OrderRequest{
		StrategyID: "s1", Symbol: "BTCUSDT", Side: types.SideBuy,
		Quantity: decimal.Zero, RefPrice: decimal.NewFromInt(100000),
	})
	if err == nil {
		t.Fatal("a non-positive order quantity should be rejected before the request is even sent")
	}
}
