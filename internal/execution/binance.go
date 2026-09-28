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

// BinanceTestnetBaseURL 是币安现货测试网地址。
//
// 刻意把测试网写成默认值：MVP 阶段绝不该出现"改一个环境变量就打到实盘"的可能。
// 接真实资金账户是一个需要单独评审的动作，不是配置项。
const BinanceTestnetBaseURL = "https://testnet.binance.vision"

// BinanceBroker 通过币安 REST API 下单。
//
// 当前只支持测试网。Mode() 恒返回 ModePaper——因为测试网用的是模拟资金，
// 把它当实盘看待会让"实盘前必须人工解锁"这道闸门形同虚设。
type BinanceBroker struct {
	baseURL   string
	apiKey    string
	apiSecret string
	client    *http.Client
	// recvWindow 是币安要求的请求有效期（毫秒）。
	recvWindow int64
}

// BinanceConfig 是币安通道的配置。
type BinanceConfig struct {
	// BaseURL 留空则使用测试网。
	BaseURL   string
	APIKey    string
	APISecret string
	Timeout   time.Duration
}

// NewBinanceTestnetBroker 创建币安测试网下单通道。
func NewBinanceTestnetBroker(cfg BinanceConfig) (*BinanceBroker, error) {
	if cfg.APIKey == "" || cfg.APISecret == "" {
		return nil, fmt.Errorf("缺少币安测试网 API 密钥（TF_BINANCE_API_KEY / TF_BINANCE_API_SECRET）")
	}
	base := cfg.BaseURL
	if base == "" {
		base = BinanceTestnetBaseURL
	}
	if !strings.Contains(base, "testnet") {
		// 明确拒绝指向生产环境的地址。真要接实盘，应当是一次显式的代码改动
		// 加一轮评审，而不是改个配置就生效。
		return nil, fmt.Errorf("BaseURL %q 不是测试网地址；当前版本只允许对接测试网", base)
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

// Name 实现 Broker。
func (b *BinanceBroker) Name() string { return "binance-testnet" }

// Mode 实现 Broker。测试网用的是模拟资金，一律按模拟盘对待。
func (b *BinanceBroker) Mode() types.TradingMode { return types.ModePaper }

// PlaceOrder 实现 Broker：下市价单。
func (b *BinanceBroker) PlaceOrder(ctx context.Context, req OrderRequest) (types.Order, error) {
	if !req.Quantity.IsPositive() {
		return types.Order{}, fmt.Errorf("下单数量 %s 非正", req.Quantity)
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
		return types.Order{}, fmt.Errorf("构造请求失败：%w", err)
	}
	httpReq.Header.Set("X-MBX-APIKEY", b.apiKey)

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return types.Order{}, fmt.Errorf("调用币安接口失败：%w", err)
	}
	defer resp.Body.Close()

	var raw binanceOrderResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return types.Order{}, fmt.Errorf("解析币安响应失败（HTTP %d）：%w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || raw.Code != 0 {
		return types.Order{}, fmt.Errorf("币安拒绝了订单（HTTP %d，code %d）：%s",
			resp.StatusCode, raw.Code, raw.Msg)
	}

	return b.toOrder(req, raw)
}

func (b *BinanceBroker) toOrder(req OrderRequest, raw binanceOrderResponse) (types.Order, error) {
	filledQty, err := decimalOrZero(raw.ExecutedQty)
	if err != nil {
		return types.Order{}, fmt.Errorf("解析成交数量失败：%w", err)
	}
	quote, err := decimalOrZero(raw.CummulativeQuoteQty)
	if err != nil {
		return types.Order{}, fmt.Errorf("解析成交金额失败：%w", err)
	}

	// 市价单没有委托价，成交均价要用成交额除以成交量算出来。
	avg := decimal.Zero
	if filledQty.IsPositive() {
		avg = quote.Div(filledQty)
	}

	fee := decimal.Zero
	for _, f := range raw.Fills {
		v, err := decimalOrZero(f.Commission)
		if err != nil {
			return types.Order{}, fmt.Errorf("解析手续费失败：%w", err)
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

// sign 用 HMAC-SHA256 对查询串签名，这是币安的鉴权要求。
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
