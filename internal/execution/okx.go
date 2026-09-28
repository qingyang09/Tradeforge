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

// OKXDefaultBaseURL 是 OKX 官方 REST 地址。模拟盘和实盘走的是同一个域名，
// 靠请求头 x-simulated-trading 区分——跟币安"测试网是另一个域名"的模式不同。
const OKXDefaultBaseURL = okx.DefaultRESTBaseURL

// OKXBroker 通过 OKX REST API 下单，走 x-simulated-trading: 1 的模拟盘模式。
//
// Mode() 恒返回 ModePaper——模拟盘用的是虚拟资金，把它当实盘看待会让
// "实盘前必须人工解锁"这道闸门形同虚设（跟 BinanceBroker 的理由一致）。
type OKXBroker struct {
	baseURL    string
	apiKey     string
	apiSecret  string
	passphrase string
	client     *http.Client
	// pollInterval/pollTimeout 控制下单后轮询成交结果的节奏：OKX 下单接口本身
	// 只返回订单号，不像币安 FULL 响应那样一次性带回成交明细，市价单要另外
	// 查询订单详情才能拿到成交均价和手续费。
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// OKXConfig 是 OKX 通道的配置。
type OKXConfig struct {
	// BaseURL 留空则使用 OKX 官方地址。
	BaseURL    string
	APIKey     string
	APISecret  string
	Passphrase string
	Timeout    time.Duration
}

// NewOKXDemoBroker 创建 OKX 模拟盘下单通道。
//
// 当前只支持模拟盘（每个请求都带 x-simulated-trading: 1）。接真实资金账户是
// 一个需要单独评审的动作，不是配置项。
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

// Name 实现 Broker。
func (b *OKXBroker) Name() string { return "okx-demo" }

// Mode 实现 Broker。模拟盘用的是虚拟资金，一律按模拟盘对待。
func (b *OKXBroker) Mode() types.TradingMode { return types.ModePaper }

// PlaceOrder 实现 Broker：下市价单，然后轮询订单详情拿到实际成交结果。
func (b *OKXBroker) PlaceOrder(ctx context.Context, req OrderRequest) (types.Order, error) {
	if !req.Quantity.IsPositive() {
		return types.Order{}, fmt.Errorf("下单数量 %s 非正", req.Quantity)
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
		// 不指定 tgtCcy 的话，OKX 现货市价单里买单的 sz 默认按计价货币（USDT）
		// 解读、卖单默认按基础货币解读——两个方向语义不一致。强制两边都按基础
		// 货币解读，才能让 sz 的含义跟 OrderRequest.Quantity（基础货币数量）
		// 以及 BinanceBroker 的行为保持一致。
		"tgtCcy": "base_ccy",
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return types.Order{}, fmt.Errorf("构造下单请求体失败：%w", err)
	}

	var placeResp okxPlaceOrderResponse
	if err := b.doSigned(ctx, http.MethodPost, "/api/v5/trade/order", nil, bodyBytes, &placeResp); err != nil {
		return types.Order{}, fmt.Errorf("OKX 下单失败：%w", err)
	}
	if placeResp.Code != "0" || len(placeResp.Data) == 0 {
		return types.Order{}, fmt.Errorf("OKX 拒绝了订单（code %s）：%s", placeResp.Code, placeResp.Msg)
	}
	entry := placeResp.Data[0]
	if entry.SCode != "0" {
		return types.Order{}, fmt.Errorf("OKX 拒绝了订单（sCode %s）：%s", entry.SCode, entry.SMsg)
	}

	detail, err := b.pollOrderDetail(ctx, instID, entry.OrdID)
	if err != nil {
		return types.Order{}, err
	}
	return b.toOrder(req, entry.OrdID, detail)
}

// pollOrderDetail 反复查询订单详情，直到订单进入终态（filled/canceled）或超时。
// OKX 市价单通常在几十到几百毫秒内成交，轮询是因为下单接口本身不返回成交明细。
func (b *OKXBroker) pollOrderDetail(ctx context.Context, instID, ordID string) (okxOrderDetail, error) {
	deadline := time.Now().Add(b.pollTimeout)
	for {
		var resp okxGetOrderResponse
		q := url.Values{"instId": {instID}, "ordId": {ordID}}
		if err := b.doSigned(ctx, http.MethodGet, "/api/v5/trade/order", q, nil, &resp); err != nil {
			return okxOrderDetail{}, fmt.Errorf("查询 OKX 订单详情失败：%w", err)
		}
		if resp.Code != "0" || len(resp.Data) == 0 {
			return okxOrderDetail{}, fmt.Errorf("查询 OKX 订单详情出错（code %s）：%s", resp.Code, resp.Msg)
		}
		detail := resp.Data[0]
		if detail.State == "filled" || detail.State == "canceled" || detail.State == "mmp_canceled" {
			return detail, nil
		}
		if time.Now().After(deadline) {
			// 超时仍未到终态：如实按"当前查到的状态"返回，不假装它已经成交。
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
		return types.Order{}, fmt.Errorf("解析成交数量失败：%w", err)
	}
	avg, err := decimalOrZero(detail.AvgPx)
	if err != nil {
		return types.Order{}, fmt.Errorf("解析成交均价失败：%w", err)
	}
	feeRaw, err := decimalOrZero(detail.Fee)
	if err != nil {
		return types.Order{}, fmt.Errorf("解析手续费失败：%w", err)
	}
	// OKX 把手续费记成负数（表示从账户扣除），项目内部约定手续费是非负的
	// 支出金额，这里取绝对值统一语义。
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

// doSigned 发送一个带 OKX v5 签名的请求并把响应体解码进 out。
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
		return fmt.Errorf("构造请求失败：%w", err)
	}

	timestamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sig := b.sign(timestamp, method, requestPath, bodyStr)

	httpReq.Header.Set("OK-ACCESS-KEY", b.apiKey)
	httpReq.Header.Set("OK-ACCESS-SIGN", sig)
	httpReq.Header.Set("OK-ACCESS-TIMESTAMP", timestamp)
	httpReq.Header.Set("OK-ACCESS-PASSPHRASE", b.passphrase)
	httpReq.Header.Set("Content-Type", "application/json")
	// 恒带模拟盘标记：当前版本只支持模拟盘，接实盘是需要单独评审的动作。
	httpReq.Header.Set("x-simulated-trading", "1")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("调用 OKX 接口失败：%w", err)
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("解析 OKX 响应失败（HTTP %d）：%w", resp.StatusCode, err)
	}
	return nil
}

// sign 按 OKX v5 的签名规则：base64(HMAC-SHA256(secret, timestamp+method+requestPath+body))。
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
