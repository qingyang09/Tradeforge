package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"
)

// Ticker is a latest-price snapshot for a spot symbol, keeping only the
// fields used for liquidity ranking — not the full OKX ticker response.
// Callers (batch scanning) only care about "how active is this symbol", not
// best bid/ask, price change percentage, etc.
type Ticker struct {
	Symbol string // internal notation, e.g. "ETHUSDT"
	// Vol24hQuote is cumulative 24h trading volume (denominated in quote
	// currency — roughly USD volume for USDT pairs), used to rank symbols by
	// how much real trading activity they have.
	Vol24hQuote decimal.Decimal
}

// tickersResponse is the response envelope for /api/v5/market/tickers; only
// the fields we use are parsed.
type tickersResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		InstID    string `json:"instId"`
		VolCcy24h string `json:"volCcy24h"`
	} `json:"data"`
}

// ListTickers fetches the latest price snapshot for all OKX spot symbols.
// Public read-only endpoint, no API key required, same origin as
// FetchCandles/ListInstruments. Only responsible for "fetching" — sorting
// and truncation are left to the caller, since Client shouldn't know about
// higher-level concepts like "batch-scan takes the top N".
func (c *Client) ListTickers(ctx context.Context) ([]Ticker, error) {
	reqURL := c.restBaseURL + "/api/v5/market/tickers?instType=SPOT"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to request OKX ticker snapshot: %w", err)
	}
	defer resp.Body.Close()

	var body tickersResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("failed to parse OKX response: %w", err)
	}
	if body.Code != "0" {
		return nil, fmt.Errorf("OKX returned an error (code=%s): %s", body.Code, body.Msg)
	}

	tickers := make([]Ticker, 0, len(body.Data))
	for _, row := range body.Data {
		vol, err := decimal.NewFromString(row.VolCcy24h)
		if err != nil {
			// A single symbol's volume failing to parse shouldn't make the
			// whole batch of snapshots unavailable — skip it without
			// affecting other symbols (the same principle as the project's
			// "one symbol's failure shouldn't affect others").
			continue
		}
		tickers = append(tickers, Ticker{
			Symbol:      strings.ReplaceAll(row.InstID, "-", ""),
			Vol24hQuote: vol,
		})
	}
	return tickers, nil
}
