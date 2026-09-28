package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// realInstrumentsResponseJSON is a handful of entries picked from a real
// response captured from GET /api/v5/public/instruments?instType=SPOT
// (BTC-USDT, a few ETH quote pairs, ETHFI-USDT whose base currency happens
// to start with "ETH" but is actually a different coin, and one non-live
// entry); field values are copied verbatim, with only the fields
// ListInstruments doesn't use trimmed out.
const realInstrumentsResponseJSON = `{"code":"0","msg":"","data":[` +
	`{"instId":"BTC-USDT","baseCcy":"BTC","quoteCcy":"USDT","state":"live"},` +
	`{"instId":"ETH-USDT","baseCcy":"ETH","quoteCcy":"USDT","state":"live"},` +
	`{"instId":"ETH-USDC","baseCcy":"ETH","quoteCcy":"USDC","state":"live"},` +
	`{"instId":"ETH-BTC","baseCcy":"ETH","quoteCcy":"BTC","state":"live"},` +
	`{"instId":"ETHW-USDT","baseCcy":"ETHW","quoteCcy":"USDT","state":"live"},` +
	`{"instId":"ETHFI-USDT","baseCcy":"ETHFI","quoteCcy":"USDT","state":"live"},` +
	`{"instId":"XTESTA-USDT","baseCcy":"XTESTA","quoteCcy":"USDT","state":"rebase"}` +
	`]}`

func TestListInstrumentsParsesRealResponseAndFiltersNonLive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v5/public/instruments") {
			t.Errorf("request path = %s, want /api/v5/public/instruments prefix", r.URL.Path)
		}
		w.Write([]byte(realInstrumentsResponseJSON))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	got, err := c.ListInstruments(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// XTESTA-USDT, with state="rebase", must be filtered out.
	if len(got) != 6 {
		t.Fatalf("returned %d entries, want 6 (the real response has 7 entries, 1 non-live one should be filtered)", len(got))
	}

	byInstID := map[string]Instrument{}
	for _, inst := range got {
		byInstID[inst.Symbol] = inst
	}

	want := Instrument{Symbol: "ETHUSDT", Base: "ETH", Quote: "USDT"}
	if got := byInstID["ETHUSDT"]; got != want {
		t.Errorf("ETH-USDT parse result = %+v, want %+v", got, want)
	}

	// ETHFI-USDT's base currency is "ETHFI", not "ETH" with something appended —
	// this is the key case for why symbol-search ranking needs to distinguish
	// "exact base-currency match" from "happens to contain the substring".
	if got := byInstID["ETHFIUSDT"].Base; got != "ETHFI" {
		t.Errorf("ETHFI-USDT's Base = %q, want %q (must not be misjudged as a prefix match for ETH)", got, "ETHFI")
	}

	for _, inst := range got {
		if inst.Symbol == "XTESTAUSDT" {
			t.Errorf("non-live symbol %s should not appear in the results", inst.Symbol)
		}
	}
}

func TestListInstrumentsRejectsErrorResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":"50000","msg":"System error","data":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	if _, err := c.ListInstruments(context.Background()); err == nil {
		t.Fatal("should error when OKX returns an error code")
	}
}
