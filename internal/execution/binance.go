package execution

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// BinanceTestnetBaseURL is Binance's spot testnet address.
//
// The testnet is deliberately hard-coded as the default: the MVP stage must
// never allow "flip an env var and it hits live trading". Connecting a real
// funded account is an action that needs its own separate review, not a
// config toggle.
const BinanceTestnetBaseURL = "https://testnet.binance.vision"

// BinanceBroker places orders through the Binance REST API.
//
// Currently testnet-only. Mode() always returns ModePaper — because the
// testnet trades with simulated funds, treating it as live would render the
// "manual unlock required before going live" gate meaningless.
type BinanceBroker struct {
	baseURL   string
	apiKey    string
	apiSecret string
	client    *http.Client
	// recvWindow is Binance's required request validity window, in milliseconds.
	recvWindow int64
}

// BinanceConfig configures the Binance channel.
type BinanceConfig struct {
	// BaseURL defaults to the testnet when left empty.
	BaseURL   string
	APIKey    string
	APISecret string
	Timeout   time.Duration
}

// NewBinanceTestnetBroker creates a Binance testnet order channel.
func NewBinanceTestnetBroker(cfg BinanceConfig) (*BinanceBroker, error) {
	if cfg.APIKey == "" || cfg.APISecret == "" {
		return nil, &BrokerConfigError{types.Msg("execution.broker.binance.missing_keys")}
	}
	base := cfg.BaseURL
	if base == "" {
		base = BinanceTestnetBaseURL
	}
	if !strings.Contains(base, "testnet") {
		// Explicitly reject anything pointing at production. Actually going
		// live should be a deliberate code change plus a review round, not
		// something a config edit can trigger.
		return nil, &BrokerConfigError{types.Msg("execution.broker.not_testnet", "base_url", base)}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &BinanceBroker{
		baseURL:    strings.TrimRight(base, "/"),
		apiKey:     cfg.APIKey,
		apiSecret:  cfg.APISecret,
		client:     &http.Client{Timeout: timeout},
		recvWindow: 5000,
	}, nil
}

// Name implements Broker.
func (b *BinanceBroker) Name() string { return "binance-testnet" }

// Mode implements Broker. The testnet trades with simulated funds, so it is
// always treated as paper.
func (b *BinanceBroker) Mode() types.TradingMode { return types.ModePaper }

// PlaceOrder implements Broker: places a market order.
func (b *BinanceBroker) PlaceOrder(ctx context.Context, req OrderRequest) (types.Order, error) {
	if !req.Quantity.IsPositive() {
		return types.Order{}, fmt.Errorf("order quantity %s is not positive", req.Quantity)
	}

	params := url.Values{}
	params.Set("symbol", req.Symbol)
	params.Set("side", string(req.Side))
	params.Set("type", "MARKET")
	params.Set("quantity", req.Quantity.String())
	params.Set("newOrderRespType", "FULL")
	params.Set("recvWindow", strconv.FormatInt(b.recvWindow, 10))
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("signature", b.sign(params.Encode()))

	endpoint := b.baseURL + "/api/v3/order?" + params.Encode()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to build request: %w", err)
	}
	httpReq.Header.Set("X-MBX-APIKEY", b.apiKey)

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return types.Order{}, fmt.Errorf("call to Binance API failed: %w", err)
	}
	defer resp.Body.Close()

	var raw binanceOrderResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return types.Order{}, fmt.Errorf("failed to parse Binance response (HTTP %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || raw.Code != 0 {
		return types.Order{}, fmt.Errorf("Binance rejected the order (HTTP %d, code %d): %s",
			resp.StatusCode, raw.Code, raw.Msg)
	}

	return b.toOrder(req, raw)
}

func (b *BinanceBroker) toOrder(req OrderRequest, raw binanceOrderResponse) (types.Order, error) {
	filledQty, err := decimalOrZero(raw.ExecutedQty)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to parse filled quantity: %w", err)
	}
	quote, err := decimalOrZero(raw.CummulativeQuoteQty)
	if err != nil {
		return types.Order{}, fmt.Errorf("failed to parse filled amount: %w", err)
	}

	// A market order has no limit price, so the average fill price has to be
	// computed as filled amount divided by filled quantity.
	avg := decimal.Zero
	if filledQty.IsPositive() {
		avg = quote.Div(filledQty)
	}

	fee := decimal.Zero
	for _, f := range raw.Fills {
		v, err := decimalOrZero(f.Commission)
		if err != nil {
			return types.Order{}, fmt.Errorf("failed to parse fee: %w", err)
		}
		fee = fee.Add(v)
	}

	status := types.OrderFilled
	if !filledQty.IsPositive() {
		status = types.OrderRejected
	}

	created := time.Now().UTC()
	if raw.TransactTime > 0 {
		created = time.UnixMilli(raw.TransactTime).UTC()
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
		ExchangeOrderID: strconv.FormatInt(raw.OrderID, 10),
		Provenance:      req.Provenance,
		CreatedAt:       created,
		FilledAt:        created,
	}, nil
}

// sign signs the query string with HMAC-SHA256, as required by Binance's auth scheme.
func (b *BinanceBroker) sign(query string) string {
	mac := hmac.New(sha256.New, []byte(b.apiSecret))
	mac.Write([]byte(query))
	return hex.EncodeToString(mac.Sum(nil))
}

type binanceOrderResponse struct {
	Code                int    `json:"code"`
	Msg                 string `json:"msg"`
	OrderID             int64  `json:"orderId"`
	TransactTime        int64  `json:"transactTime"`
	ExecutedQty         string `json:"executedQty"`
	CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
	Status              string `json:"status"`
	Fills               []struct {
		Price      string `json:"price"`
		Qty        string `json:"qty"`
		Commission string `json:"commission"`
	} `json:"fills"`
}

func decimalOrZero(s string) (decimal.Decimal, error) {
	if strings.TrimSpace(s) == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(s)
}
