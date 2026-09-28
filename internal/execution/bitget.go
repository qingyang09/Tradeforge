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

// BitgetDefaultBaseURL 是 Bitget 官方 REST 地址。模拟盘和实盘走的是同一个域名，
// 靠请求头 paptrading 区分——跟 OKX 的模式一样（跟币安/Bybit"测试网是另一个域名"
// 不同），而且模拟盘要求用专门生成的"模拟盘 API Key"，不是随便一把普通 key。
const BitgetDefaultBaseURL = "https://api.bitget.com"

// BitgetBroker 通过 Bitget REST API v2 下单，走 paptrading: 1 的模拟盘模式。
//
// Mode() 恒返回 ModePaper——理由跟其它几个 broker 一致：模拟盘用的是虚拟资金，
// 把它当实盘看待会让"实盘前必须人工解锁"这道闸门形同虚设。
//
// Bitget 现货市价单有一处跟 OKX/Bybit 不一样、且没有覆盖参数可以绕开的不对称：
// 市价买单的 size 按计价货币（USDT）解读，市价卖单的 size 按基础货币解读——
// OKX 有 tgtCcy、Bybit 有 marketUnit 可以强制两边都按基础货币算，Bitget v2 现货
// 接口没有等价参数。所以买单这里用 RefPrice 把"基础货币数量"换算成"计价货币金额"
// 发出去，这只是一个尽量贴近目标仓位的近似值——真实成交数量以下单后查询到的
// baseVolume 为准，不影响记录到 Order 里的实际成交结果是否正确，只影响"实际成交
// 数量跟本来想要的数量差多少"。
type BitgetBroker struct {
	baseURL    string
	apiKey     string
	apiSecret  string
	passphrase string
	client     *http.Client
	// pollInterval/pollTimeout：下单接口只返回订单号，不返回成交明细，要另外
	// 查询订单详情才能拿到成交均价和手续费（跟 OKX/Bybit 是同一个模式）。
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// BitgetConfig 是 Bitget 通道的配置。
type BitgetConfig struct {
	// BaseURL 留空则使用 Bitget 官方地址。
	BaseURL    string
	APIKey     string
	APISecret  string
	Passphrase string
	Timeout    time.Duration
}

// NewBitgetDemoBroker 创建 Bitget 模拟盘下单通道。
//
// 当前只支持模拟盘（每个请求都带 paptrading: 1，且要求用专门生成的模拟盘 API Key）。
// 接真实资金账户是一个需要单独评审的动作，不是配置项。
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

// Name 实现 Broker。
func (b *BitgetBroker) Name() string { return "bitget-demo" }

// Mode 实现 Broker。模拟盘用的是虚拟资金，一律按模拟盘对待。
func (b *BitgetBroker) Mode() types.TradingMode { return types.ModePaper }

// PlaceOrder 实现 Broker：下现货市价单，然后轮询订单详情拿到实际成交结果。
func (b *BitgetBroker) PlaceOrder(ctx context.Context, req OrderRequest) (types.Order, error) {
	if !req.Quantity.IsPositive() {
		return types.Order{}, fmt.Errorf("下单数量 %s 非正", req.Quantity)
	}

	side := "buy"
	size := req.Quantity
	if req.Side == types.SideSell {
		side = "sell"
	} else {
		// 市价买单的 size 是计价货币金额，不是基础货币数量——见类型注释。
		// 没有参考价就没法换算，直接拒绝，不能假装能算出来。
		if !req.RefPrice.IsPositive() {
			return types.Order{}, fmt.Errorf("Bitget 市价买单需要一个正的参考价才能把数量换算成计价货币金额")
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
		return types.Order{}, fmt.Errorf("构造下单请求体失败：%w", err)
	}

	var placeResp bitgetPlaceOrderResponse
	if err := b.doSigned(ctx, http.MethodPost, "/api/v2/spot/trade/place-order", nil, bodyBytes, &placeResp); err != nil {
		return types.Order{}, fmt.Errorf("Bitget 下单失败：%w", err)
	}
	if placeResp.Code != "00000" {
		return types.Order{}, fmt.Errorf("Bitget 拒绝了订单（code %s）：%s", placeResp.Code, placeResp.Msg)
	}
	orderID := placeResp.Data.OrderID

	detail, err := b.pollOrderDetail(ctx, orderID)
	if err != nil {
		return types.Order{}, err
	}
	return b.toOrder(req, orderID, detail)
}

// pollOrderDetail 反复查询订单详情，直到订单进入终态或超时。
func (b *BitgetBroker) pollOrderDetail(ctx context.Context, orderID string) (bitgetOrderDetail, error) {
	deadline := time.Now().Add(b.pollTimeout)
	for {
		var resp bitgetGetOrderResponse
		q := url.Values{"orderId": {orderID}}
		if err := b.doSigned(ctx, http.MethodGet, "/api/v2/spot/trade/orderInfo", q, nil, &resp); err != nil {
			return bitgetOrderDetail{}, fmt.Errorf("查询 Bitget 订单详情失败：%w", err)
		}
		if resp.Code != "00000" || len(resp.Data) == 0 {
			return bitgetOrderDetail{}, fmt.Errorf("查询 Bitget 订单详情出错（code %s）：%s", resp.Code, resp.Msg)
		}
		detail := resp.Data[0]
		switch detail.Status {
		case "filled", "cancelled", "rejected":
			return detail, nil
		}
		if time.Now().After(deadline) {
			// 超时仍未到终态：如实按"当前查到的状态"返回，不假装它已经成交。
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
		return types.Order{}, fmt.Errorf("解析成交数量失败：%w", err)
	}
	avg, err := decimalOrZero(detail.PriceAvg)
	if err != nil {
		return types.Order{}, fmt.Errorf("解析成交均价失败：%w", err)
	}

	fee := decimal.Zero
	for _, f := range detail.FeeDetail {
		v, err := decimalOrZero(f.TotalFee)
		if err != nil {
			return types.Order{}, fmt.Errorf("解析手续费失败：%w", err)
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

// doSigned 发送一个带 Bitget v2 签名的请求并把响应体解码进 out。
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
		return fmt.Errorf("构造请求失败：%w", err)
	}

	timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
	sig := b.sign(timestamp, method, requestPath, bodyStr)

	httpReq.Header.Set("ACCESS-KEY", b.apiKey)
	httpReq.Header.Set("ACCESS-SIGN", sig)
	httpReq.Header.Set("ACCESS-TIMESTAMP", timestamp)
	httpReq.Header.Set("ACCESS-PASSPHRASE", b.passphrase)
	httpReq.Header.Set("Content-Type", "application/json")
	// 恒带模拟盘标记：当前版本只支持模拟盘，接实盘是需要单独评审的动作。
	// 前提是调用方用的是专门生成的"模拟盘 API Key"，普通 key 带这个头会被拒绝。
	httpReq.Header.Set("paptrading", "1")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("调用 Bitget 接口失败：%w", err)
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("解析 Bitget 响应失败（HTTP %d）：%w", resp.StatusCode, err)
	}
	return nil
}

// sign 按 Bitget v2 的签名规则：base64(HMAC-SHA256(secret, timestamp+method.upper()+requestPath+body))。
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
