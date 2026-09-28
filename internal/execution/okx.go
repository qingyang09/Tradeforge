package execution

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tradeforge/internal/marketdata/okx"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// OKXDefaultBaseURL is OKX's official REST address. Demo and live trading
// share the same domain, distinguished only by the x-simulated-trading
// header — unlike Binance's pattern of "testnet is a different domain".
const OKXDefaultBaseURL = okx.DefaultRESTBaseURL

// OKXBroker places orders through the OKX REST API, always in demo mode via
// x-simulated-trading: 1.
//
// Mode() always returns ModePaper — demo trading uses virtual funds, and
// treating it as live would render the "manual unlock required before going
// live" gate meaningless (same rationale as BinanceBroker).
type OKXBroker struct {
	baseURL    string
	apiKey     string
	apiSecret  string
	passphrase string
	client     *http.Client
	// pollInterval/pollTimeout control the cadence of polling for the fill
	// result after placing an order: OKX's order-placement endpoint only
	// returns an order ID, unlike Binance's FULL response which returns fill
	// details in one shot — a market order needs a separate order-detail
	// query to get the average fill price and fee.
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// OKXConfig configures the OKX channel.
type OKXConfig struct {
	// BaseURL defaults to OKX's official address when left empty.
	BaseURL    string
	APIKey     string
	APISecret  string
	Passphrase string
	Timeout    time.Duration
}

// NewOKXDemoBroker creates an OKX demo-trading order channel.
//
// Currently demo-only (every request carries x-simulated-trading: 1).
// Connecting a real funded account is an action that needs its own separate
// review, not a config toggle.
func NewOKXDemoBroker(cfg OKXConfig) (*OKXBroker, error) {
	if cfg.APIKey == "" || cfg.APISecret == "" || cfg.Passphrase == "" {
		return nil, fmt.Errorf("缺少 OKX 模拟盘 API 密钥（TF_OKX_API_KEY / TF_OKX_API_SECRET / TF_OKX_PASSPHRASE）")
	}
	base := cfg.BaseURL
	if base == "" {
		base = OKXDefaultBaseURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &OKXBroker{
		baseURL:      strings.TrimRight(base, "/"),
		apiKey:       cfg.APIKey,
		apiSecret:    cfg.APISecret,
		passphrase:   cfg.Passphrase,
		client:       &http.Client{Timeout: timeout},
		pollInterval: 300 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}, nil
}

// Name implements Broker.
func (b *OKXBroker) Name() string { return "okx-demo" }

// Mode implements Broker. Demo trading uses virtual funds, so it is always
// treated as paper.
func (b *OKXBroker) Mode() types.TradingMode { return types.ModePaper }

// PlaceOrder implements Broker: places a market order, then polls the order
// detail to get the actual fill result.
func (b *OKXBroker) PlaceOrder(ctx context.Context, req OrderRequest) (types.Order, error) {
	if !req.Quantity.IsPositive() {
		return types.Order{}, fmt.Errorf("order quantity %s is not positive", req.Quantity)
	}
	instID, err := okx.ToInstID(req.Symbol)
	if err != nil {
		return types.Order{}, err
	}

	side := "buy"
	if req.Side == types.SideSell {
		side = "sell"
	}
	body := map[string]string{
		"instId":  instID,
		"tdMode":  "cash",
		"side":    side,
		"ordType": "market",
		"sz":      req.Quantity.String(),
		// Without tgtCcy, OKX spot market orders interpret sz as the quote
		// currency (USDT) by default for buys and as the base currency by
		// default for sells — inconsistent semantics across the two sides.
		// Forcing both sides to interpret it as the base currency keeps sz's
		// meaning aligned with OrderRequest.Quantity (a base-currency amount)
		// and with BinanceBroker's behavior.
		"tgtCcy": "base_ccy",
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to build order request body: %w", err)
	}

	var placeResp okxPlaceOrderResponse
	if err := b.doSigned(ctx, http.MethodPost, "/api/v5/trade/order", nil, bodyBytes, &placeResp); err != nil {
		return types.Order{}, fmt.Errorf("OKX order placement failed: %w", err)
	}
	if placeResp.Code != "0" || len(placeResp.Data) == 0 {
		return types.Order{}, fmt.Errorf("OKX rejected the order (code %s): %s", placeResp.Code, placeResp.Msg)
	}
	entry := placeResp.Data[0]
	if entry.SCode != "0" {
		return types.Order{}, fmt.Errorf("OKX rejected the order (sCode %s): %s", entry.SCode, entry.SMsg)
	}

	detail, err := b.pollOrderDetail(ctx, instID, entry.OrdID)
	if err != nil {
		return types.Order{}, err
	}
	return b.toOrder(req, entry.OrdID, detail)
}

// pollOrderDetail repeatedly queries the order detail until the order reaches
// a terminal state (filled/canceled) or the poll times out. OKX market orders
// typically fill within tens to hundreds of milliseconds; polling is needed
// because the order-placement endpoint itself doesn't return fill details.
func (b *OKXBroker) pollOrderDetail(ctx context.Context, instID, ordID string) (okxOrderDetail, error) {
	deadline := time.Now().Add(b.pollTimeout)
	for {
		var resp okxGetOrderResponse
		q := url.Values{"instId": {instID}, "ordId": {ordID}}
		if err := b.doSigned(ctx, http.MethodGet, "/api/v5/trade/order", q, nil, &resp); err != nil {
			return okxOrderDetail{}, fmt.Errorf("failed to query OKX order detail: %w", err)
		}
		if resp.Code != "0" || len(resp.Data) == 0 {
			return okxOrderDetail{}, fmt.Errorf("error querying OKX order detail (code %s): %s", resp.Code, resp.Msg)
		}
		detail := resp.Data[0]
		if detail.State == "filled" || detail.State == "canceled" || detail.State == "mmp_canceled" {
			return detail, nil
		}
		if time.Now().After(deadline) {
			// Still not terminal by the deadline: return the status as
			// currently observed, rather than pretending it filled.
			return detail, nil
		}
		select {
		case <-ctx.Done():
			return okxOrderDetail{}, ctx.Err()
		case <-time.After(b.pollInterval):
		}
	}
}

func (b *OKXBroker) toOrder(req OrderRequest, ordID string, detail okxOrderDetail) (types.Order, error) {
	filledQty, err := decimalOrZero(detail.AccFillSz)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to parse filled quantity: %w", err)
	}
	avg, err := decimalOrZero(detail.AvgPx)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to parse average fill price: %w", err)
	}
	feeRaw, err := decimalOrZero(detail.Fee)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to parse fee: %w", err)
	}
	// OKX records the fee as a negative number (an account debit); this
	// project's internal convention is that fees are a non-negative expense
	// amount, so take the absolute value to keep the semantics uniform.
	fee := feeRaw.Abs()

	status := types.OrderFilled
	if detail.State != "filled" || !filledQty.IsPositive() {
		status = types.OrderRejected
	}

	created := time.Now().UTC()
	if detail.CTime != "" {
		if ms, convErr := decimalOrZero(detail.CTime); convErr == nil && ms.IsPositive() {
			created = time.UnixMilli(ms.IntPart()).UTC()
		}
	}

	return types.Order{
		ID:              idgen.NewUUID(),
		StrategyID:      req.StrategyID,
		Symbol:          req.Symbol,
		Side:            req.Side,
		Type:            types.OrderMarket,
		Mode:            types.ModePaper,
		Quantity:        filledQty,
		FilledPrice:     avg,
		Fee:             fee,
		Status:          status,
		ExchangeOrderID: ordID,
		Provenance:      req.Provenance,
		CreatedAt:       created,
		FilledAt:        created,
	}, nil
}

// doSigned sends a request signed per OKX v5 rules and decodes the response body into out.
func (b *OKXBroker) doSigned(ctx context.Context, method, path string, query url.Values, body []byte, out any) error {
	requestPath := path
	if len(query) > 0 {
		requestPath += "?" + query.Encode()
	}
	endpoint := b.baseURL + requestPath

	var bodyReader io.Reader
	bodyStr := ""
	if len(body) > 0 {
		bodyStr = string(body)
		bodyReader = strings.NewReader(bodyStr)
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}

	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sig := b.sign(timestamp, method, requestPath, bodyStr)

	httpReq.Header.Set("OK-ACCESS-KEY", b.apiKey)
	httpReq.Header.Set("OK-ACCESS-SIGN", sig)
	httpReq.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
	httpReq.Header.Set("OK-ACCESS-PASSPHRASE", b.passphrase)
	httpReq.Header.Set("Content-Type", "application/json")
	// Always carry the demo-trading flag: this version only supports demo
	// trading; connecting live is an action that needs its own separate review.
	httpReq.Header.Set("x-simulated-trading", "1")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("call to OKX API failed: %w", err)
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to parse OKX response (HTTP %d): %w", resp.StatusCode, err)
	}
	return nil
}

// sign follows OKX v5's signing rule: base64(HMAC-SHA256(secret, timestamp+method+requestPath+body)).
func (b *OKXBroker) sign(timestamp, method, requestPath, body string) string {
	mac := hmac.New(sha256.New, []byte(b.apiSecret))
	mac.Write([]byte(timestamp + method + requestPath + body))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

type okxPlaceOrderResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		OrdID string `json:"ordId"`
		SCode string `json:"sCode"`
		SMsg  string `json:"sMsg"`
	} `json:"data"`
}

type okxGetOrderResponse struct {
	Code string           `json:"code"`
	Msg  string           `json:"msg"`
	Data []okxOrderDetail `json:"data"`
}

type okxOrderDetail struct {
	InstID    string `json:"instId"`
	OrdID     string `json:"ordId"`
	AvgPx     string `json:"avgPx"`
	AccFillSz string `json:"accFillSz"`
	Fee       string `json:"fee"`
	State     string `json:"state"`
	CTime     string `json:"cTime"`
}
