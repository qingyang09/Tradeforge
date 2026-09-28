package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// realCandlesResponseJSON is a real response captured from
// GET /api/v5/market/candles?instId=BTC-USDT&bar=1H&limit=3 (descending:
// newest first); field order and precision are copied verbatim, not a
// hand-written example.
const realCandlesResponseJSON = `{"code":"0","msg":"","data":[` +
	`["1787184000000","69337.2","69599.4","69319.9","69514.5","111.39520843","7743007.753348444","7743007.753348444","0"],` +
	`["1787180400000","69264.7","69462.5","69160.6","69337.1","333.0511483","23087731.217311397","23087731.217311397","1"],` +
	`["1787176800000","69718.5","69846.6","69000","69264.5","509.18666667","35301481.318071814","35301481.318071814","1"]]}`

// realErrorResponseJSON is a real response captured when requesting a
// nonexistent instId.
const realErrorResponseJSON = `{"code":"51001","msg":"Instrument ID, Instrument ID code, or Spread ID doesn't exist.","data":[]}`

func fakeRESTServer(t *testing.T, body string, statusCode int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v5/market/candles") {
			t.Errorf("request path = %s, want /api/v5/market/candles prefix", r.URL.Path)
		}
		w.WriteHeader(statusCode)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchCandlesParsesRealResponseAndReverses(t *testing.T) {
	srv := fakeRESTServer(t, realCandlesResponseJSON, http.StatusOK)
	c := NewClient(WithRESTBaseURL(srv.URL))

	candles, err := c.FetchCandles(context.Background(), "BTCUSDT", types.TF1h, 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(candles) != 3 {
		t.Fatalf("candle count = %d, want 3", len(candles))
	}
	// OKX's raw response is descending (newest first); FetchCandles must flip it to ascending.
	if !candles[0].OpenTime.Before(candles[1].OpenTime) || !candles[1].OpenTime.Before(candles[2].OpenTime) {
		t.Errorf("candles should be in ascending time order, got: %v, %v, %v",
			candles[0].OpenTime, candles[1].OpenTime, candles[2].OpenTime)
	}
	if !candles[2].Close.Equal(dec("69514.5")) {
		t.Errorf("last candle's close = %s, want 69514.5 (corresponds to the newest one in the raw response)", candles[2].Close)
	}
}

func TestFetchCandlesRejectsErrorResponse(t *testing.T) {
	srv := fakeRESTServer(t, realErrorResponseJSON, http.StatusOK)
	c := NewClient(WithRESTBaseURL(srv.URL))

	_, err := c.FetchCandles(context.Background(), "BTCUSDT", types.TF1h, 3)
	if err == nil {
		t.Fatal("should error when OKX returns an error code")
	}
	if !strings.Contains(err.Error(), "51001") {
		t.Errorf("error message should include OKX's error code, got: %v", err)
	}
}

func TestFetchCandlesRejectsUnknownSymbol(t *testing.T) {
	c := NewClient()
	if _, err := c.FetchCandles(context.Background(), "NOTASYMBOL", types.TF1h, 3); err == nil {
		t.Fatal("a symbol with an unrecognized quote currency should be rejected before making a request")
	}
}

func TestFetchCandlesClampsLimitToMax(t *testing.T) {
	var gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	if _, err := c.FetchCandles(context.Background(), "BTCUSDT", types.TF1h, 5000); err != nil {
		t.Fatal(err)
	}
	if gotLimit != "300" {
		t.Errorf("limit param = %s, want clamped to 300 (OKX's per-request cap)", gotLimit)
	}
}

func TestFetchCandlesBeforeSendsOKXAfterParam(t *testing.T) {
	var gotAfter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAfter = r.URL.Query().Get("after")
		w.Write([]byte(realCandlesResponseJSON))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	before := time.UnixMilli(1787176800000)
	candles, err := c.FetchCandlesBefore(context.Background(), "BTCUSDT", types.TF1h, 3, before)
	if err != nil {
		t.Fatal(err)
	}
	if gotAfter != "1787176800000" {
		t.Errorf("OKX after param = %s, want equal to the passed-in before millisecond timestamp 1787176800000", gotAfter)
	}
	if len(candles) != 3 {
		t.Fatalf("candle count = %d, want 3 (flipped to ascending just like plain FetchCandles)", len(candles))
	}
}

func TestFetchCandlesBeforeRejectsZeroTime(t *testing.T) {
	c := NewClient()
	if _, err := c.FetchCandlesBefore(context.Background(), "BTCUSDT", types.TF1h, 3, time.Time{}); err == nil {
		t.Fatal("should error when before is the zero value, instead of silently sending a plain request with no after param")
	}
}
