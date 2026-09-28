package okx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"
)

// Ticker 是某个现货标的的最新行情快照，只保留用于按流动性排序的字段——不是完整的
// OKX ticker 响应，调用方（批量扫描）只关心"这个标的活跃不活跃"，不需要买一卖一价、
// 涨跌幅这些字段。
type Ticker struct {
	Symbol string // 内部写法，如 "ETHUSDT"
	// Vol24hQuote 是过去 24 小时的累计成交额（以计价货币计，对 USDT 交易对就是
	// 大致的美元成交额），用来给标的按"有多少人在真实交易"排序。
	Vol24hQuote decimal.Decimal
}

// tickersResponse 是 /api/v5/market/tickers 的响应外壳，只解析用得到的字段。
type tickersResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data []struct {
		InstID    string `json:"instId"`
		VolCcy24h string `json:"volCcy24h"`
	} `json:"data"`
}

// ListTickers 拉取 OKX 现货全部标的的最新行情快照，公开只读接口，不需要 API key，
// 跟 FetchCandles/ListInstruments 同源。只负责"拉取"，排序/截断由调用方决定——
// Client 不该知道"批量扫描取前 N 个"这种上层业务概念。
func (c *Client) ListTickers(ctx context.Context) ([]Ticker, error) {
	reqURL := c.restBaseURL + "/api/v5/market/tickers?instType=SPOT"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 OKX 行情快照失败：%w", err)
	}
	defer resp.Body.Close()

	var body tickersResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("解析 OKX 响应失败：%w", err)
	}
	if body.Code != "0" {
		return nil, fmt.Errorf("OKX 返回错误（code=%s）：%s", body.Code, body.Msg)
	}

	tickers := make([]Ticker, 0, len(body.Data))
	for _, row := range body.Data {
		vol, err := decimal.NewFromString(row.VolCcy24h)
		if err != nil {
			// 单个标的的成交额解析失败不该让整批行情快照都拿不到——跳过它，
			// 不影响其它标的（跟项目里"一个标的的异常不能影响其它标的"是同一个原则）。
			continue
		}
		tickers = append(tickers, Ticker{
			Symbol:      strings.ReplaceAll(row.InstID, "-", ""),
			Vol24hQuote: vol,
		})
	}
	return tickers, nil
}
