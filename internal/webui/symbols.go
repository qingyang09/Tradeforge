package webui

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"tradeforge/internal/marketdata/okx"
)

// symbolCacheTTL 决定标的列表缓存多久刷新一次。OKX 现货标的列表一天内几乎不变，
// 没必要每次搜索请求都打一次真实 OKX 接口。
const symbolCacheTTL = 1 * time.Hour

// symbolLister 是 symbolCache 依赖的最小接口，真实实现是 okx.Client.ListInstruments；
// 测试可以注入假实现，不用打真实 OKX。
type symbolLister interface {
	ListInstruments(ctx context.Context) ([]okx.Instrument, error)
}

// preferredQuoteOrder 决定同一相关度档次内，常见计价货币排在前面——跟
// internal/marketdata/okx/symbol.go 的 knownQuoteCurrencies 保持同一优先序。用户搜
// "ETH" 时最想看到的是 ETHUSDT/ETHUSDC 这类主流计价对，不是排在后面的长尾法币对
// （ETHAED、ETHBRL……），纯字母序会把这些冷门对排到前 N 个结果里，挤掉真正常用的。
var preferredQuoteOrder = []string{"USDT", "USDC", "BTC", "ETH", "USD"}

func quoteRank(quote string) int {
	quote = strings.ToUpper(quote)
	for i, q := range preferredQuoteOrder {
		if quote == q {
			return i
		}
	}
	return len(preferredQuoteOrder)
}

// symbolCache 是进程内的标的列表缓存：懒加载 + 定期过期，不需要外部存储——
// 跟 sessionStore、agent 配置一样，重启后重新拉一次即可。
type symbolCache struct {
	lister symbolLister

	mu          sync.Mutex
	instruments []okx.Instrument
	fetchedAt   time.Time
}

func newSymbolCache(lister symbolLister) *symbolCache {
	return &symbolCache{lister: lister}
}

// searchMatch 记录一次候选命中及其排序依据，只在 search 内部使用。
type searchMatch struct {
	symbol    string
	tier      int // 0 = 基础货币精确匹配，1 = 基础货币前缀匹配，2 = 标的字符串里包含
	quoteRank int
}

// search 返回匹配 query 的标的，最多 limit 个。排序按相关度分档：基础货币精确等于
// query 的排最前，其次是基础货币以 query 开头的，最后是随便哪里包含 query 子串的；
// 同档内按计价货币的常见程度（preferredQuoteOrder）再排一次。query 为空返回空
// 列表——用户还没打字时不需要吐一整页标的。
func (c *symbolCache) search(ctx context.Context, query string, limit int) ([]string, error) {
	query = strings.ToUpper(strings.TrimSpace(query))
	if query == "" || limit <= 0 {
		return nil, nil
	}
	all, err := c.all(ctx)
	if err != nil {
		return nil, err
	}

	matches := make([]searchMatch, 0, limit*2)
	for _, inst := range all {
		base := strings.ToUpper(inst.Base)
		var tier int
		switch {
		case base == query:
			tier = 0
		case strings.HasPrefix(base, query):
			tier = 1
		case strings.Contains(inst.Symbol, query):
			tier = 2
		default:
			continue
		}
		matches = append(matches, searchMatch{symbol: inst.Symbol, tier: tier, quoteRank: quoteRank(inst.Quote)})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].tier != matches[j].tier {
			return matches[i].tier < matches[j].tier
		}
		if matches[i].quoteRank != matches[j].quoteRank {
			return matches[i].quoteRank < matches[j].quoteRank
		}
		return matches[i].symbol < matches[j].symbol
	})

	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.symbol
	}
	return out, nil
}

func (c *symbolCache) all(ctx context.Context) ([]okx.Instrument, error) {
	c.mu.Lock()
	if len(c.instruments) > 0 && time.Since(c.fetchedAt) < symbolCacheTTL {
		defer c.mu.Unlock()
		return c.instruments, nil
	}
	c.mu.Unlock()

	fetched, err := c.lister.ListInstruments(ctx)
	if err != nil {
		// 拉取失败时如果还有旧缓存，宁可返回过期数据也不让搜索框直接报错——
		// 标的列表变化很慢，一次网络抖动不该让自动补全整个消失。
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(c.instruments) > 0 {
			return c.instruments, nil
		}
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.instruments = fetched
	c.fetchedAt = time.Now()
	return c.instruments, nil
}
