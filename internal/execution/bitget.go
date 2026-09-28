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

	"github.com/shopspring/decimal"

	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// BitgetDefaultBaseURL is Bitget's official REST address. Demo and live
// trading share the same domain, distinguished only by the paptrading header
// — the same pattern as OKX (unlike Binance/Bybit's "testnet is a different
// domain"), and demo trading requires a specially-generated "demo API key",
// not just any regular key.
const BitgetDefaultBaseURL = "https://api.bitget.com"

// BitgetBroker places orders through the Bitget REST API v2, always in demo
// mode via paptrading: 1.
//
// Mode() always returns ModePaper — same rationale as the other brokers: demo
// trading uses virtual funds, and treating it as live would render the
// "manual unlock required before going live" gate meaningless.
//
// Bitget spot market orders have one asymmetry that differs from OKX/Bybit
// and has no override parameter to work around: a market buy's size is
// interpreted as the quote currency (USDT), while a market sell's size is
// interpreted as the base currency. OKX has tgtCcy and Bybit has marketUnit
// to force both sides to compute in the base currency; Bitget's v2 spot API
// has no equivalent parameter. So for buys, RefPrice is used here to convert
// the "base-currency quantity" into a "quote-currency amount" before sending
// — this is only an approximation aiming to get close to the target
// position; the real fill quantity is whatever baseVolume comes back from
// the post-order query. It doesn't affect whether the actual fill result
// recorded on the Order is correct, only how far the actual filled quantity
// ends up from what was originally wanted.
type BitgetBroker struct {
	baseURL    string
	apiKey     string
	apiSecret  string
	passphrase string
	client     *http.Client
	// pollInterval/pollTimeout: the order-placement endpoint only returns an
	// order ID, not fill details; a separate order-detail query is needed to
	// get the average fill price and fee (same pattern as OKX/Bybit).
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// BitgetConfig configures the Bitget channel.
type BitgetConfig struct {
	// BaseURL defaults to Bitget's official address when left empty.
	BaseURL    string
	APIKey     string
	APISecret  string
	Passphrase string
	Timeout    time.Duration
}

// NewBitgetDemoBroker creates a Bitget demo-trading order channel.
//
// Currently demo-only (every request carries paptrading: 1, and requires a
// specially-generated demo API key). Connecting a real funded account is an
// action that needs its own separate review, not a config toggle.
func NewBitgetDemoBroker(cfg BitgetConfig) (*BitgetBroker, error) {
	if cfg.APIKey == "" || cfg.APISecret == "" || cfg.Passphrase == "" {
		return nil, fmt.Errorf("缺少 Bitget 模拟盘 API 密钥（TF_BITGET_API_KEY / TF_BITGET_API_SECRET / TF_BITGET_PASSPHRASE）")
	}
	base := cfg.BaseURL
	if base == "" {
		base = BitgetDefaultBaseURL
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &BitgetBroker{
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
func (b *BitgetBroker) Name() string { return "bitget-demo" }

// Mode implements Broker. Demo trading uses virtual funds, so it is always
// treated as paper.
func (b *BitgetBroker) Mode() types.TradingMode { return types.ModePaper }

// PlaceOrder implements Broker: places a spot market order, then polls the
// order detail to get the actual fill result.
func (b *BitgetBroker) PlaceOrder(ctx context.Context, req OrderRequest) (types.Order, error) {
	if !req.Quantity.IsPositive() {
		return types.Order{}, fmt.Errorf("order quantity %s is not positive", req.Quantity)
	}

	side := "buy"
	size := req.Quantity
	if req.Side == types.SideSell {
		side = "sell"
	} else {
		// A market buy's size is a quote-currency amount, not a base-currency
		// quantity — see the type comment. With no reference price there's no
		// way to convert it, so reject outright rather than pretending we can.
		if !req.RefPrice.IsPositive() {
			return types.Order{}, fmt.Errorf("Bitget market buy orders need a positive reference price to convert the quantity into a quote-currency amount")
		}
		size = req.Quantity.Mul(req.RefPrice)
	}

	body := map[string]string{
		"symbol":    req.Symbol,
		"side":      side,
		"orderType": "market",
		"force":     "gtc",
		"size":      size.String(),
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to build order request body: %w", err)
	}

	var placeResp bitgetPlaceOrderResponse
	if err := b.doSigned(ctx, http.MethodPost, "/api/v2/spot/trade/place-order", nil, bodyBytes, &placeResp); err != nil {
		return types.Order{}, fmt.Errorf("Bitget order placement failed: %w", err)
	}
	if placeResp.Code != "00000" {
		return types.Order{}, fmt.Errorf("Bitget rejected the order (code %s): %s", placeResp.Code, placeResp.Msg)
	}
	orderID := placeResp.Data.OrderID

	detail, err := b.pollOrderDetail(ctx, orderID)
	if err != nil {
		return types.Order{}, err
	}
	return b.toOrder(req, orderID, detail)
}

// pollOrderDetail repeatedly queries the order detail until the order reaches a terminal state or the poll times out.
func (b *BitgetBroker) pollOrderDetail(ctx context.Context, orderID string) (bitgetOrderDetail, error) {
	deadline := time.Now().Add(b.pollTimeout)
	for {
		var resp bitgetGetOrderResponse
		q := url.Values{"orderId": {orderID}}
		if err := b.doSigned(ctx, http.MethodGet, "/api/v2/spot/trade/orderInfo", q, nil, &resp); err != nil {
			return bitgetOrderDetail{}, fmt.Errorf("failed to query Bitget order detail: %w", err)
		}
		if resp.Code != "00000" || len(resp.Data) == 0 {
			return bitgetOrderDetail{}, fmt.Errorf("error querying Bitget order detail (code %s): %s", resp.Code, resp.Msg)
		}
		detail := resp.Data[0]
		switch detail.Status {
		case "filled", "cancelled", "rejected":
			return detail, nil
		}
		if time.Now().After(deadline) {
			// Still not terminal by the deadline: return the status as
			// currently observed, rather than pretending it filled.
			return detail, nil
		}
		select {
		case <-ctx.Done():
			return bitgetOrderDetail{}, ctx.Err()
		case <-time.After(b.pollInterval):
		}
	}
}

func (b *BitgetBroker) toOrder(req OrderRequest, orderID string, detail bitgetOrderDetail) (types.Order, error) {
	filledQty, err := decimalOrZero(detail.BaseVolume)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to parse filled quantity: %w", err)
	}
	avg, err := decimalOrZero(detail.PriceAvg)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to parse average fill price: %w", err)
	}

	fee := decimal.Zero
	for _, f := range detail.FeeDetail {
		v, err := decimalOrZero(f.TotalFee)
		if err != nil {
			return types.Order{}, fmt.Errorf("failed to parse fee: %w", err)
		}
		fee = fee.Add(v.Abs())
	}

	status := types.OrderFilled
	if detail.Status != "filled" || !filledQty.IsPositive() {
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
		ExchangeOrderID: orderID,
		Provenance:      req.Provenance,
		CreatedAt:       created,
		FilledAt:        created,
	}, nil
}

// doSigned sends a request signed per Bitget v2 rules and decodes the response body into out.
func (b *BitgetBroker) doSigned(ctx context.Context, method, path string, query url.Values, body []byte, out any) error {
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

	timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
	sig := b.sign(timestamp, method, requestPath, bodyStr)

	httpReq.Header.Set("ACCESS-KEY", b.apiKey)
	httpReq.Header.Set("ACCESS-SIGN", sig)
	httpReq.Header.Set("ACCESS-TIMESTAMP", timestamp)
	httpReq.Header.Set("ACCESS-PASSPHRASE", b.passphrase)
	httpReq.Header.Set("Content-Type", "application/json")
	// Always carry the demo-trading flag: this version only supports demo
	// trading; connecting live is an action that needs its own separate
	// review. This assumes the caller is using a specially-generated "demo
	// API key" — a regular key sending this header gets rejected.
	httpReq.Header.Set("paptrading", "1")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("call to Bitget API failed: %w", err)
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to parse Bitget response (HTTP %d): %w", resp.StatusCode, err)
	}
	return nil
}

// sign follows Bitget v2's signing rule: base64(HMAC-SHA256(secret, timestamp+method.upper()+requestPath+body)).
func (b *BitgetBroker) sign(timestamp, method, requestPath, body string) string {
	mac := hmac.New(sha256.New, []byte(b.apiSecret))
	mac.Write([]byte(timestamp + strings.ToUpper(method) + requestPath + body))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

type bitgetPlaceOrderResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		OrderID string `json:"orderId"`
	} `json:"data"`
}

type bitgetGetOrderResponse struct {
	Code string              `json:"code"`
	Msg  string              `json:"msg"`
	Data []bitgetOrderDetail `json:"data"`
}

type bitgetOrderDetail struct {
	OrderID    string `json:"orderId"`
	Symbol     string `json:"symbol"`
	PriceAvg   string `json:"priceAvg"`
	BaseVolume string `json:"baseVolume"`
	Status     string `json:"status"`
	CTime      string `json:"cTime"`
	FeeDetail  map[string]struct {
		TotalFee string `json:"totalFee"`
	} `json:"feeDetail"`
}
