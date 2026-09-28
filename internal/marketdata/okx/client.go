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

// DefaultRESTBaseURL 是 OKX 官方 REST 地址。
const DefaultRESTBaseURL = "https://www.okx.com"

// DefaultWSURL 是 OKX 承载 K 线频道的 WebSocket 地址（"business" 业务频道，
// 不是公共行情频道——OKX 把 candle 频道放在 business 这条线上）。
const DefaultWSURL = "wss://ws.okx.com:8443/ws/v5/business"

// maxCandlesPerRequest 是 OKX /market/candles 单次请求的上限：请求更多也只会
// 静默截断成这个数字，不会报错，已用真实请求验证过（请求 301 根实际拿回 300 根）。
const maxCandlesPerRequest = 300

// Client 是 OKX 行情客户端：REST 回填历史 K 线、WebSocket 订阅实时 K 线。
// 只读行情，不涉及下单，不需要 API key。
type Client struct {
	restBaseURL string
	wsURL       string
	httpClient  *http.Client
}

// Option 定制 Client 的构造，主要给测试用来注入假地址。
type Option func(*Client)

// WithRESTBaseURL 覆盖 REST 地址，测试时指向 httptest.NewServer。
func WithRESTBaseURL(base string) Option { return func(c *Client) { c.restBaseURL = base } }

// WithWSURL 覆盖 WebSocket 地址，测试时指向本地假 WS 服务器。
func WithWSURL(url string) Option { return func(c *Client) { c.wsURL = url } }

// WithHTTPClient 覆盖底层 HTTP 客户端。
func WithHTTPClient(hc *http.Client) Option { return func(c *Client) { c.httpClient = hc } }

// NewClient 按选项构造客户端，未覆盖的字段使用 OKX 官方默认地址。
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

// okxResponse 是 OKX REST 接口统一的响应外壳。code 非 "0" 表示出错，data 里是错误详情。
type okxResponse struct {
	Code string     `json:"code"`
	Msg  string     `json:"msg"`
	Data [][]string `json:"data"`
}

// FetchCandles 拉取最近 limit 根历史 K 线，按时间升序返回（OKX 原始响应是倒序，
// 这里负责翻转，调用方拿到的顺序跟项目里 Candle 切片"最后一根最新"的约定一致）。
//
// limit 超过 maxCandlesPerRequest 时会被裁剪到上限，而不是报错——调用方要更长的历史
// 应该多次分页请求，这个方法本身只负责"一次请求能拿到的最大量"。
func (c *Client) FetchCandles(ctx context.Context, symbol string, tf types.Timeframe, limit int) ([]types.Candle, error) {
	return c.fetchCandles(ctx, symbol, tf, limit, 0)
}

// FetchCandlesBefore 拉取严格早于 before 的历史 K 线，同样按时间升序返回、同样受
// maxCandlesPerRequest 限制——画板图表左拖到已加载数据的最左端时用这个接口继续
// 往回翻页，而不是重新拉一份从"现在"往回数的固定窗口（那样拿到的还是同一段数据）。
func (c *Client) FetchCandlesBefore(ctx context.Context, symbol string, tf types.Timeframe, limit int, before time.Time) ([]types.Candle, error) {
	if before.IsZero() {
		return nil, fmt.Errorf("before 不能为零值")
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
		// OKX 的 "after" 参数语义是"返回早于这个 ts 的记录"——命名跟我们这边
		// "before"（更早的历史）刚好相反，是 OKX 自己的措辞，照抄官方文档用即可。
		q.Set("after", fmt.Sprintf("%d", beforeMs))
	}
	reqURL := c.restBaseURL + "/api/v5/market/candles?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 OKX 历史 K 线失败：%w", err)
	}
	defer resp.Body.Close()

	var body okxResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("解析 OKX 响应失败：%w", err)
	}
	if body.Code != "0" {
		return nil, fmt.Errorf("OKX 返回错误（code=%s）：%s", body.Code, body.Msg)
	}

	candles := make([]types.Candle, 0, len(body.Data))
	for _, row := range body.Data {
		candle, _, err := parseCandleRow(row, tf)
		if err != nil {
			return nil, fmt.Errorf("解析 %s 的历史 K 线失败：%w", symbol, err)
		}
		candles = append(candles, candle)
	}
	// OKX 按时间倒序返回（最新的在前），翻转成项目约定的升序。
	for i, j := 0, len(candles)-1; i < j; i, j = i+1, j-1 {
		candles[i], candles[j] = candles[j], candles[i]
	}
	return candles, nil
}

// instrumentsResponse 是 /api/v5/public/instruments 的响应外壳，只解析用得到的字段。
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

// Instrument 是 ListInstruments 返回的一条标的信息：项目内部写法（Symbol）+ 拆开的
// 基础/计价货币。拆开是为了让调用方（比如标的搜索建议排序）能按"基础货币是否精确
// 匹配"判断相关度，而不是在一个已经拼接好的字符串上做子串匹配去猜——子串匹配会把
// 一个完全不相关的基础货币（比如 "ETHFI"）错当成对 "ETH" 的前缀命中。
type Instrument struct {
	Symbol string // 项目内部写法，如 "ETHUSDT"
	Base   string // 基础货币，如 "ETH"
	Quote  string // 计价货币，如 "USDT"
}

// ListInstruments 拉取 OKX 现货可交易标的全集，只保留 state 为 "live" 的——
// 已下架/暂停交易的标的不该出现在标的搜索建议里。公开只读接口，不需要 API key，
// 跟 FetchCandles 同源。
func (c *Client) ListInstruments(ctx context.Context) ([]Instrument, error) {
	reqURL := c.restBaseURL + "/api/v5/public/instruments?instType=SPOT"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 OKX 现货标的列表失败：%w", err)
	}
	defer resp.Body.Close()

	var body instrumentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("解析 OKX 响应失败：%w", err)
	}
	if body.Code != "0" {
		return nil, fmt.Errorf("OKX 返回错误（code=%s）：%s", body.Code, body.Msg)
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
