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

// BybitTestnetBaseURL 是 Bybit 现货测试网地址。
const BybitTestnetBaseURL = "https://api-testnet.bybit.com"

// BybitBroker 通过 Bybit REST API v5 下单。
//
// 当前只支持测试网。Mode() 恒返回 ModePaper——理由跟 BinanceBroker/OKXBroker 一致：
// 测试网用的是模拟资金，把它当实盘看待会让"实盘前必须人工解锁"这道闸门形同虚设。
//
// Bybit 的标的写法（"BTCUSDT"）跟项目内部约定一致，不需要像 OKX 那样做符号转换。
type BybitBroker struct {
	baseURL    string
	apiKey     string
	apiSecret  string
	recvWindow string
	client     *http.Client
	// pollInterval/pollTimeout：Bybit 下单接口只返回订单号，不返回成交明细
	// （跟 OKX 一样，不像币安一次响应就带回 FULL 成交信息），要另外查询订单
	// 详情才能拿到成交均价和手续费。
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// BybitConfig 是 Bybit 通道的配置。
type BybitConfig struct {
	// BaseURL 留空则使用测试网。
	BaseURL   string
	APIKey    string
	APISecret string
	Timeout   time.Duration
}

// NewBybitTestnetBroker 创建 Bybit 测试网下单通道。
func NewBybitTestnetBroker(cfg BybitConfig) (*BybitBroker, error) {
	if cfg.APIKey == "" || cfg.APISecret == "" {
		return nil, fmt.Errorf("缺少 Bybit 测试网 API 密钥（TF_BYBIT_API_KEY / TF_BYBIT_API_SECRET）")
	}
	base := cfg.BaseURL
	if base == "" {
		base = BybitTestnetBaseURL
	}
	if !strings.Contains(base, "testnet") {
		// 明确拒绝指向生产环境的地址，跟 BinanceBroker 是同一条安全约定。
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

// Name 实现 Broker。
func (b *BybitBroker) Name() string { return "bybit-testnet" }

// Mode 实现 Broker。测试网用的是模拟资金，一律按模拟盘对待。
func (b *BybitBroker) Mode() types.TradingMode { return types.ModePaper }

// PlaceOrder 实现 Broker：下现货市价单，然后轮询订单详情拿到实际成交结果。
func (b *BybitBroker) PlaceOrder(ctx context.Context, req OrderRequest) (types.Order, error) {
	if !req.Quantity.IsPositive() {
		return types.Order{}, fmt.Errorf("下单数量 %s 非正", req.Quantity)
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
		// 不指定 marketUnit 的话，Bybit 现货市价单里买单的 qty 默认按计价货币
		// 解读、卖单默认按基础货币解读——跟 OKX 不指定 tgtCcy 时的默认行为是
		// 同一类问题。强制两边都按基础货币解读，保证 qty 的含义跟
		// OrderRequest.Quantity（基础货币数量）一致，不因买卖方向变化。
		"marketUnit": "baseCoin",
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return types.Order{}, fmt.Errorf("构造下单请求体失败：%w", err)
	}

	var placeResp bybitPlaceOrderResponse
	if err := b.doSigned(ctx, http.MethodPost, "/v5/order/create", nil, bodyBytes, &placeResp); err != nil {
		return types.Order{}, fmt.Errorf("Bybit 下单失败：%w", err)
	}
	if placeResp.RetCode != 0 {
		return types.Order{}, fmt.Errorf("Bybit 拒绝了订单（retCode %d）：%s", placeResp.RetCode, placeResp.RetMsg)
	}
	orderID := placeResp.Result.OrderID

	detail, err := b.pollOrderDetail(ctx, req.Symbol, orderID)
	if err != nil {
		return types.Order{}, err
	}
	return b.toOrder(req, orderID, detail)
}

// pollOrderDetail 反复查询订单详情，直到订单进入终态或超时。
func (b *BybitBroker) pollOrderDetail(ctx context.Context, symbol, orderID string) (bybitOrderDetail, error) {
	deadline := time.Now().Add(b.pollTimeout)
	for {
		var resp bybitGetOrderResponse
		q := url.Values{"category": {"spot"}, "symbol": {symbol}, "orderId": {orderID}}
		if err := b.doSigned(ctx, http.MethodGet, "/v5/order/realtime", q, nil, &resp); err != nil {
			return bybitOrderDetail{}, fmt.Errorf("查询 Bybit 订单详情失败：%w", err)
		}
		if resp.RetCode != 0 {
			return bybitOrderDetail{}, fmt.Errorf("查询 Bybit 订单详情出错（retCode %d）：%s", resp.RetCode, resp.RetMsg)
		}
		if len(resp.Result.List) == 0 {
			return bybitOrderDetail{}, fmt.Errorf("查询 Bybit 订单详情失败：响应里没有这笔订单")
		}
		detail := resp.Result.List[0]
		switch detail.OrderStatus {
		case "Filled", "Cancelled", "Rejected", "PartiallyFilledCanceled":
			return detail, nil
		}
		if time.Now().After(deadline) {
			// 超时仍未到终态：如实按"当前查到的状态"返回，不假装它已经成交。
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
		return types.Order{}, fmt.Errorf("解析成交数量失败：%w", err)
	}
	avg, err := decimalOrZero(detail.AvgPrice)
	if err != nil {
		return types.Order{}, fmt.Errorf("解析成交均价失败：%w", err)
	}
	feeRaw, err := decimalOrZero(detail.CumExecFee)
	if err != nil {
		return types.Order{}, fmt.Errorf("解析手续费失败：%w", err)
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

// doSigned 发送一个带 Bybit v5 签名的请求并把响应体解码进 out。
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
		return fmt.Errorf("构造请求失败：%w", err)
	}

	timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
	// Bybit v5 的签名负载：GET 用查询串，POST 用请求体，二选一，不能都带上。
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
		return fmt.Errorf("调用 Bybit 接口失败：%w", err)
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("解析 Bybit 响应失败（HTTP %d）：%w", resp.StatusCode, err)
	}
	return nil
}

// sign 按 Bybit v5 的签名规则：hex(HMAC-SHA256(secret, timestamp+apiKey+recvWindow+payload))。
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
