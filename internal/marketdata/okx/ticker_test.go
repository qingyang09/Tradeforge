package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// realTickersResponseJSON is a handful of entries picked from a real
// response captured from GET /api/v5/market/tickers?instType=SPOT; field
// values are copied verbatim, with only the fields ListTickers doesn't use
// trimmed out.
const realTickersResponseJSON = `{"code":"0","msg":"","data":[` +
	`{"instId":"BTC-USDT","last":"100150","volCcy24h":"850000000.5"},` +
	`{"instId":"ETH-USDT","last":"3500","volCcy24h":"320000000.25"},` +
	`{"instId":"DOGE-USDT","last":"0.15","volCcy24h":"45000000"}` +
	`]}`

func TestListTickersParsesRealResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v5/market/tickers") {
			t.Errorf("request path = %s, want /api/v5/market/tickers prefix", r.URL.Path)
		}
		w.Write([]byte(realTickersResponseJSON))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	got, err := c.ListTickers(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("returned %d entries, want 3", len(got))
	}

	bySymbol := map[string]Ticker{}
	for _, tk := range got {
		bySymbol[tk.Symbol] = tk
	}
	// decimal.Decimal has an internal pointer field, so == compares pointer identity, not
	// value — two independently parsed Decimals that are numerically equal aren't
	// necessarily the same pointer, so Equal must be used for value comparison.
	wantVol := decimal.RequireFromString("850000000.5")
	btc, ok := bySymbol["BTCUSDT"]
	if !ok {
		t.Fatalf("BTCUSDT missing from parsed results")
	}
	if !btc.Vol24hQuote.Equal(wantVol) {
		t.Errorf("BTC-USDT's Vol24hQuote = %s, want %s", btc.Vol24hQuote, wantVol)
	}
}

func TestListTickersRejectsErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":"50000","msg":"System error","data":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	if _, err := c.ListTickers(context.Background()); err == nil {
		t.Fatal("should error when OKX returns an error code")
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
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("a row with unparseable volume should be skipped instead of failing the whole batch; returned %d entries, want 1", len(got))
	}
	if got[0].Symbol != "BTCUSDT" {
		t.Errorf("the remaining entry should be BTCUSDT, got %q", got[0].Symbol)
	}
}
