package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tradeforge/pkg/types"
)

// DefaultRESTBaseURL is OKX's official REST endpoint.
const DefaultRESTBaseURL = "https://www.okx.com"

// DefaultWSURL is OKX's WebSocket endpoint carrying the candle channel (the
// "business" channel, not the public market channel — OKX puts the candle
// channel on the business line).
const DefaultWSURL = "wss://ws.okx.com:8443/ws/v5/business"

// maxCandlesPerRequest is the cap for a single OKX /market/candles request:
// asking for more is silently truncated to this number rather than erroring —
// verified against a real request (asking for 301 candles actually returned
// 300).
const maxCandlesPerRequest = 300

// Client is the OKX market-data client: REST backfills historical candles,
// WebSocket subscribes to live candles. Read-only market data, no order
// placement, no API key required.
type Client struct {
	restBaseURL string
	wsURL       string
	httpClient  *http.Client
}

// Option customizes Client construction, mainly used by tests to inject fake
// addresses.
type Option func(*Client)

// WithRESTBaseURL overrides the REST endpoint, pointed at httptest.NewServer
// in tests.
func WithRESTBaseURL(base string) Option { return func(c *Client) { c.restBaseURL = base } }

// WithWSURL overrides the WebSocket endpoint, pointed at a local fake WS
// server in tests.
func WithWSURL(url string) Option { return func(c *Client) { c.wsURL = url } }

// WithHTTPClient overrides the underlying HTTP client.
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.httpClient = hc } }

// NewClient builds a client from options; fields not overridden use OKX's
// official default endpoints.
func NewClient(opts ...Option) *Client {
	c := &Client{
		restBaseURL: DefaultRESTBaseURL,
		wsURL:       DefaultWSURL,
		httpClient:  http.DefaultClient,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// okxResponse is the common response envelope for OKX REST endpoints. A code
// other than "0" indicates an error, with details in data.
type okxResponse struct {
	Code string     `json:"code"`
	Msg  string     `json:"msg"`
	Data [][]string `json:"data"`
}

// FetchCandles fetches the most recent limit historical candles, returned in
// ascending time order (OKX's raw response is descending; this method flips
// it so callers get the order the project's Candle slices assume — "last
// element is most recent").
//
// A limit above maxCandlesPerRequest is clamped to the cap rather than
// erroring — callers that need more history should page through multiple
// requests; this method is only responsible for "the most a single request
// can return".
func (c *Client) FetchCandles(ctx context.Context, symbol string, tf types.Timeframe, limit int) ([]types.Candle, error) {
	return c.fetchCandles(ctx, symbol, tf, limit, 0)
}

// FetchCandlesBefore fetches historical candles strictly earlier than before,
// also returned in ascending time order and also subject to
// maxCandlesPerRequest — used to keep paging backward when a chart is dragged
// left past the edge of already-loaded data, instead of re-fetching the same
// fixed window counted back from "now" (which would just return the same
// data again).
func (c *Client) FetchCandlesBefore(ctx context.Context, symbol string, tf types.Timeframe, limit int, before time.Time) ([]types.Candle, error) {
	if before.IsZero() {
		return nil, fmt.Errorf("before must not be the zero value")
	}
	return c.fetchCandles(ctx, symbol, tf, limit, before.UnixMilli())
}

func (c *Client) fetchCandles(ctx context.Context, symbol string, tf types.Timeframe, limit int, beforeMs int64) ([]types.Candle, error) {
	instID, err := ToInstID(symbol)
	if err != nil {
		return nil, err
	}
	bar, err := toBar(tf)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = maxCandlesPerRequest
	}
	if limit > maxCandlesPerRequest {
		limit = maxCandlesPerRequest
	}

	q := url.Values{
		"instId": {instID},
		"bar":    {bar},
		"limit":  {fmt.Sprintf("%d", limit)},
	}
	if beforeMs > 0 {
		// OKX's "after" parameter means "return records earlier than this
		// ts" — the naming is the opposite of our "before" (earlier
		// history); that's OKX's own wording, just follow the official docs.
		q.Set("after", fmt.Sprintf("%d", beforeMs))
	}
	reqURL := c.restBaseURL + "/api/v5/market/candles?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request OKX historical candles: %w", err)
	}
	defer resp.Body.Close()

	var body okxResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("failed to parse OKX response: %w", err)
	}
	if body.Code != "0" {
		return nil, fmt.Errorf("OKX returned an error (code=%s): %s", body.Code, body.Msg)
	}

	candles := make([]types.Candle, 0, len(body.Data))
	for _, row := range body.Data {
		candle, _, err := parseCandleRow(row, tf)
		if err != nil {
			return nil, fmt.Errorf("failed to parse historical candles for %s: %w", symbol, err)
		}
		candles = append(candles, candle)
	}
	// OKX returns candles in descending time order (newest first); flip to
	// the project's ascending convention.
	for i, j := 0, len(candles)-1; i < j; i, j = i+1, j-1 {
		candles[i], candles[j] = candles[j], candles[i]
	}
	return candles, nil
}

// instrumentsResponse is the response envelope for
// /api/v5/public/instruments; only the fields we use are parsed.
type instrumentsResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		InstID   string `json:"instId"`
		BaseCcy  string `json:"baseCcy"`
		QuoteCcy string `json:"quoteCcy"`
		State    string `json:"state"`
	} `json:"data"`
}

// Instrument is one instrument entry returned by ListInstruments: the
// project's internal notation (Symbol) plus the base/quote currency split
// out separately. The split lets callers (e.g. ranking symbol-search
// suggestions) judge relevance by whether the base currency matches exactly,
// instead of guessing via substring matching on an already-concatenated
// string — substring matching would wrongly treat a completely unrelated
// base currency (e.g. "ETHFI") as a prefix match for "ETH".
type Instrument struct {
	Symbol string // internal notation, e.g. "ETHUSDT"
	Base   string // base currency, e.g. "ETH"
	Quote  string // quote currency, e.g. "USDT"
}

// ListInstruments fetches the full set of OKX spot tradable instruments,
// keeping only those with state "live" — delisted/suspended instruments
// shouldn't show up in symbol-search suggestions. Public read-only endpoint,
// no API key required, same origin as FetchCandles.
func (c *Client) ListInstruments(ctx context.Context) ([]Instrument, error) {
	reqURL := c.restBaseURL + "/api/v5/public/instruments?instType=SPOT"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request OKX spot instrument list: %w", err)
	}
	defer resp.Body.Close()

	var body instrumentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("failed to parse OKX response: %w", err)
	}
	if body.Code != "0" {
		return nil, fmt.Errorf("OKX returned an error (code=%s): %s", body.Code, body.Msg)
	}

	instruments := make([]Instrument, 0, len(body.Data))
	for _, inst := range body.Data {
		if inst.State != "live" {
			continue
		}
		instruments = append(instruments, Instrument{
			Symbol: strings.ReplaceAll(inst.InstID, "-", ""),
			Base:   inst.BaseCcy,
			Quote:  inst.QuoteCcy,
		})
	}
	return instruments, nil
}
