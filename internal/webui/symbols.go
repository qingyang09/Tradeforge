package webui

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"tradeforge/internal/marketdata/okx"
)

// symbolCacheTTL decides how often the symbol list cache refreshes. OKX's
// spot symbol list barely changes within a day, so there's no need to hit
// the real OKX endpoint on every single search request.
const symbolCacheTTL = 1 * time.Hour

// symbolLister is the minimal interface symbolCache depends on; the real
// implementation is okx.Client.ListInstruments. Tests can inject a fake
// implementation instead of hitting real OKX.
type symbolLister interface {
	ListInstruments(ctx context.Context) ([]okx.Instrument, error)
}

// preferredQuoteOrder puts common quote currencies first within the same
// relevance tier -- kept in the same priority order as
// internal/marketdata/okx/symbol.go's knownQuoteCurrencies. When a user
// searches "ETH," what they most want to see is a mainstream quote pair
// like ETHUSDT/ETHUSDC, not a long-tail fiat pair further down the list
// (ETHAED, ETHBRL, ...) -- plain alphabetical order would let these obscure
// pairs crowd into the top N results, pushing out the ones people actually use.
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

// symbolCache is an in-process cache of the symbol list: lazy-loaded with
// periodic expiry, needing no external storage -- same as sessionStore and
// the Agent config, it just re-fetches once after a restart.
type symbolCache struct {
	lister symbolLister

	mu          sync.Mutex
	instruments []okx.Instrument
	fetchedAt   time.Time
}

func newSymbolCache(lister symbolLister) *symbolCache {
	return &symbolCache{lister: lister}
}

// searchMatch records one candidate match and its sort basis; used only inside search.
type searchMatch struct {
	symbol    string
	tier      int // 0 = exact base-currency match, 1 = base-currency prefix match, 2 = substring match anywhere in the symbol
	quoteRank int
}

// search returns symbols matching query, up to limit of them. Sorted by
// relevance tier: an exact base-currency match to query ranks first, then
// a base currency starting with query, then anything containing query as
// a substring anywhere; within the same tier, sorted again by quote
// currency commonness (preferredQuoteOrder). An empty query returns an
// empty list -- no need to spit out a whole page of symbols before the
// user has typed anything.
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
		// If the fetch fails but an old cache still exists, it's better to
		// return stale data than to let the search box error out outright --
		// the symbol list changes slowly, and a single network blip
		// shouldn't make autocomplete disappear entirely.
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
