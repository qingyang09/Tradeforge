package execution

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// BybitTestnetBaseURL is Bybit's spot testnet address.
const BybitTestnetBaseURL = "https://api-testnet.bybit.com"

// BybitBroker places orders through the Bybit REST API v5.
//
// Currently testnet-only. Mode() always returns ModePaper — same rationale as
// BinanceBroker/OKXBroker: the testnet trades with simulated funds, and
// treating it as live would render the "manual unlock required before going
// live" gate meaningless.
//
// Bybit's symbol format ("BTCUSDT") matches this project's internal
// convention, so no symbol conversion is needed the way OKX requires.
type BybitBroker struct {
	baseURL    string
	apiKey     string
	apiSecret  string
	recvWindow string
	client     *http.Client
	// pollInterval/pollTimeout: Bybit's order-placement endpoint only returns
	// an order ID, not fill details (like OKX, unlike Binance which returns
	// FULL fill info in one response); a separate order-detail query is
	// needed to get the average fill price and fee.
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// BybitConfig configures the Bybit channel.
type BybitConfig struct {
	// BaseURL defaults to the testnet when left empty.
	BaseURL   string
	APIKey    string
	APISecret string
	Timeout   time.Duration
}

// NewBybitTestnetBroker creates a Bybit testnet order channel.
func NewBybitTestnetBroker(cfg BybitConfig) (*BybitBroker, error) {
	if cfg.APIKey == "" || cfg.APISecret == "" {
		return nil, fmt.Errorf("缺少 Bybit 测试网 API 密钥（TF_BYBIT_API_KEY / TF_BYBIT_API_SECRET）")
	}
	base := cfg.BaseURL
	if base == "" {
		base = BybitTestnetBaseURL
	}
	if !strings.Contains(base, "testnet") {
		// Explicitly reject anything pointing at production — the same safety rule as BinanceBroker.
		return nil, fmt.Errorf("BaseURL %q 不是测试网地址；当前版本只允许对接测试网", base)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &BybitBroker{
		baseURL:      strings.TrimRight(base, "/"),
		apiKey:       cfg.APIKey,
		apiSecret:    cfg.APISecret,
		recvWindow:   "5000",
		client:       &http.Client{Timeout: timeout},
		pollInterval: 300 * time.Millisecond,
		pollTimeout:  5 * time.Second,
	}, nil
}

// Name implements Broker.
func (b *BybitBroker) Name() string { return "bybit-testnet" }

// Mode implements Broker. The testnet trades with simulated funds, so it is
// always treated as paper.
func (b *BybitBroker) Mode() types.TradingMode { return types.ModePaper }

// PlaceOrder implements Broker: places a spot market order, then polls the
// order detail to get the actual fill result.
func (b *BybitBroker) PlaceOrder(ctx context.Context, req OrderRequest) (types.Order, error) {
	if !req.Quantity.IsPositive() {
		return types.Order{}, fmt.Errorf("order quantity %s is not positive", req.Quantity)
	}

	side := "Buy"
	if req.Side == types.SideSell {
		side = "Sell"
	}
	body := map[string]string{
		"category":  "spot",
		"symbol":    req.Symbol,
		"side":      side,
		"orderType": "Market",
		"qty":       req.Quantity.String(),
		// Without marketUnit, Bybit spot market orders interpret qty as the
		// quote currency by default for buys and as the base currency by
		// default for sells — the same class of problem as OKX's default
		// behavior when tgtCcy is unset. Forcing both sides to interpret it
		// as the base currency keeps qty's meaning consistent with
		// OrderRequest.Quantity (a base-currency amount), independent of side.
		"marketUnit": "baseCoin",
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to build order request body: %w", err)
	}

	var placeResp bybitPlaceOrderResponse
	if err := b.doSigned(ctx, http.MethodPost, "/v5/order/create", nil, bodyBytes, &placeResp); err != nil {
		return types.Order{}, fmt.Errorf("Bybit order placement failed: %w", err)
	}
	if placeResp.RetCode != 0 {
		return types.Order{}, fmt.Errorf("Bybit rejected the order (retCode %d): %s", placeResp.RetCode, placeResp.RetMsg)
	}
	orderID := placeResp.Result.OrderID

	detail, err := b.pollOrderDetail(ctx, req.Symbol, orderID)
	if err != nil {
		return types.Order{}, err
	}
	return b.toOrder(req, orderID, detail)
}

// pollOrderDetail repeatedly queries the order detail until the order reaches a terminal state or the poll times out.
func (b *BybitBroker) pollOrderDetail(ctx context.Context, symbol, orderID string) (bybitOrderDetail, error) {
	deadline := time.Now().Add(b.pollTimeout)
	for {
		var resp bybitGetOrderResponse
		q := url.Values{"category": {"spot"}, "symbol": {symbol}, "orderId": {orderID}}
		if err := b.doSigned(ctx, http.MethodGet, "/v5/order/realtime", q, nil, &resp); err != nil {
			return bybitOrderDetail{}, fmt.Errorf("failed to query Bybit order detail: %w", err)
		}
		if resp.RetCode != 0 {
			return bybitOrderDetail{}, fmt.Errorf("error querying Bybit order detail (retCode %d): %s", resp.RetCode, resp.RetMsg)
		}
		if len(resp.Result.List) == 0 {
			return bybitOrderDetail{}, fmt.Errorf("failed to query Bybit order detail: order not present in the response")
		}
		detail := resp.Result.List[0]
		switch detail.OrderStatus {
		case "Filled", "Cancelled", "Rejected", "PartiallyFilledCanceled":
			return detail, nil
		}
		if time.Now().After(deadline) {
			// Still not terminal by the deadline: return the status as
			// currently observed, rather than pretending it filled.
			return detail, nil
		}
		select {
		case <-ctx.Done():
			return bybitOrderDetail{}, ctx.Err()
		case <-time.After(b.pollInterval):
		}
	}
}

func (b *BybitBroker) toOrder(req OrderRequest, orderID string, detail bybitOrderDetail) (types.Order, error) {
	filledQty, err := decimalOrZero(detail.CumExecQty)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to parse filled quantity: %w", err)
	}
	avg, err := decimalOrZero(detail.AvgPrice)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to parse average fill price: %w", err)
	}
	feeRaw, err := decimalOrZero(detail.CumExecFee)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to parse fee: %w", err)
	}
	fee := feeRaw.Abs()

	status := types.OrderFilled
	if detail.OrderStatus != "Filled" || !filledQty.IsPositive() {
		status = types.OrderRejected
	}

	created := time.Now().UTC()
	if detail.CreatedTime != "" {
		if ms, convErr := decimalOrZero(detail.CreatedTime); convErr == nil && ms.IsPositive() {
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

// doSigned sends a request signed per Bybit v5 rules and decodes the response body into out.
func (b *BybitBroker) doSigned(ctx context.Context, method, path string, query url.Values, body []byte, out any) error {
	requestPath := path
	queryStr := ""
	if len(query) > 0 {
		queryStr = query.Encode()
		requestPath += "?" + queryStr
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
	// Bybit v5's signature payload: GET uses the query string, POST uses the
	// request body — pick one, never both.
	payload := queryStr
	if method == http.MethodPost {
		payload = bodyStr
	}
	sig := b.sign(timestamp, payload)

	httpReq.Header.Set("X-BAPI-API-KEY", b.apiKey)
	httpReq.Header.Set("X-BAPI-SIGN", sig)
	httpReq.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	httpReq.Header.Set("X-BAPI-RECV-WINDOW", b.recvWindow)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("call to Bybit API failed: %w", err)
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to parse Bybit response (HTTP %d): %w", resp.StatusCode, err)
	}
	return nil
}

// sign follows Bybit v5's signing rule: hex(HMAC-SHA256(secret, timestamp+apiKey+recvWindow+payload)).
func (b *BybitBroker) sign(timestamp, payload string) string {
	mac := hmac.New(sha256.New, []byte(b.apiSecret))
	mac.Write([]byte(timestamp + b.apiKey + b.recvWindow + payload))
	return hex.EncodeToString(mac.Sum(nil))
}

type bybitPlaceOrderResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		OrderID string `json:"orderId"`
	} `json:"result"`
}

type bybitGetOrderResponse struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  struct {
		List []bybitOrderDetail `json:"list"`
	} `json:"result"`
}

type bybitOrderDetail struct {
	OrderID     string `json:"orderId"`
	Symbol      string `json:"symbol"`
	AvgPrice    string `json:"avgPrice"`
	CumExecQty  string `json:"cumExecQty"`
	CumExecFee  string `json:"cumExecFee"`
	OrderStatus string `json:"orderStatus"`
	CreatedTime string `json:"createdTime"`
}
