package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"tradeforge/internal/marketdata/okx"
)

// fakeSymbolLister 让排序/缓存逻辑的测试不用打真实 OKX，行为可以按用例精确控制。
type fakeSymbolLister struct {
	instruments []okx.Instrument
	err         error
	calls       int
}

func (f *fakeSymbolLister) ListInstruments(ctx context.Context) ([]okx.Instrument, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.instruments, nil
}

// sampleInstruments 覆盖排序需要区分的三种情况：基础货币精确匹配（ETH-USDT 等）、
// 基础货币只是前缀命中但其实是另一个币种（ETHFI-USDT），以及完全不该匹配 "ETH" 的
// 标的（BTC-USDT）。计价货币故意混了冷门（AED）和主流（USDT/USDC/BTC），验证
// preferredQuoteOrder 生效。
func sampleInstruments() []okx.Instrument {
	return []okx.Instrument{
		{Symbol: "ETHAED", Base: "ETH", Quote: "AED"},
		{Symbol: "ETHBTC", Base: "ETH", Quote: "BTC"},
		{Symbol: "ETHUSDC", Base: "ETH", Quote: "USDC"},
		{Symbol: "ETHUSDT", Base: "ETH", Quote: "USDT"},
		{Symbol: "ETHFIUSDT", Base: "ETHFI", Quote: "USDT"},
		{Symbol: "BTCUSDT", Base: "BTC", Quote: "USDT"},
	}
}

func TestSymbolCacheSearchRanksExactBaseMatchAheadOfPrefixMatch(t *testing.T) {
	cache := newSymbolCache(&fakeSymbolLister{instruments: sampleInstruments()})

	got, err := cache.search(context.Background(), "ETH", 10)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}

	// ETHFIUSDT 的基础货币是 "ETHFI"，只是前缀命中，必须排在所有基础货币精确等于
	// "ETH" 的标的后面——反过来会把不相关的币种挤到用户最先看到的位置。
	fiIndex := indexOf(got, "ETHFIUSDT")
	if fiIndex == -1 {
		t.Fatalf("结果里应包含 ETHFIUSDT，实际：%v", got)
	}
	for _, exact := range []string{"ETHUSDT", "ETHUSDC", "ETHBTC", "ETHAED"} {
		if idx := indexOf(got, exact); idx == -1 || idx > fiIndex {
			t.Errorf("%s（基础货币精确匹配）应排在 ETHFIUSDT（前缀匹配）之前，实际结果：%v", exact, got)
		}
	}

	if indexOf(got, "BTCUSDT") != -1 {
		t.Errorf("BTCUSDT 跟 \"ETH\" 无关，不应出现在结果里，实际：%v", got)
	}
}

func TestSymbolCacheSearchPrefersCommonQuoteCurrencies(t *testing.T) {
	cache := newSymbolCache(&fakeSymbolLister{instruments: sampleInstruments()})

	got, err := cache.search(context.Background(), "ETH", 10)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}

	// 同为基础货币精确匹配时，USDT 这类主流计价对应该排在 AED 这类冷门法币对前面。
	usdtIndex, aedIndex := indexOf(got, "ETHUSDT"), indexOf(got, "ETHAED")
	if usdtIndex == -1 || aedIndex == -1 || usdtIndex > aedIndex {
		t.Errorf("ETHUSDT 应排在 ETHAED 之前，实际结果：%v", got)
	}
}

func TestSymbolCacheSearchRespectsLimit(t *testing.T) {
	cache := newSymbolCache(&fakeSymbolLister{instruments: sampleInstruments()})

	got, err := cache.search(context.Background(), "ETH", 2)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(got) != 2 {
		t.Fatalf("结果数量 = %d，期望 2", len(got))
	}
}

func TestSymbolCacheSearchEmptyQueryReturnsNothing(t *testing.T) {
	cache := newSymbolCache(&fakeSymbolLister{instruments: sampleInstruments()})

	got, err := cache.search(context.Background(), "  ", 10)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(got) != 0 {
		t.Errorf("空查询应返回空列表，实际：%v", got)
	}
}

func TestSymbolCacheCachesAcrossCalls(t *testing.T) {
	lister := &fakeSymbolLister{instruments: sampleInstruments()}
	cache := newSymbolCache(lister)

	if _, err := cache.search(context.Background(), "ETH", 10); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if _, err := cache.search(context.Background(), "BTC", 10); err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if lister.calls != 1 {
		t.Errorf("ListInstruments 被调用 %d 次，期望缓存生效只调用 1 次", lister.calls)
	}
}

func TestSymbolCacheFallsBackToStaleDataOnFetchError(t *testing.T) {
	lister := &fakeSymbolLister{instruments: sampleInstruments()}
	cache := newSymbolCache(lister)
	if _, err := cache.search(context.Background(), "ETH", 10); err != nil {
		t.Fatalf("意外错误：%v", err)
	}

	lister.err = errors.New("网络抖动")
	// 强制过期，模拟 TTL 到期后下一次请求恰好碰上 OKX 请求失败。
	cache.fetchedAt = cache.fetchedAt.Add(-2 * symbolCacheTTL)

	got, err := cache.search(context.Background(), "ETH", 10)
	if err != nil {
		t.Fatalf("已有旧缓存时不应把这次的网络错误往上抛，实际：%v", err)
	}
	if len(got) == 0 {
		t.Error("应该退回旧缓存继续可用，实际返回了空结果")
	}
}

func TestSymbolCacheReturnsErrorWhenNoCacheAndFetchFails(t *testing.T) {
	lister := &fakeSymbolLister{err: errors.New("网络抖动")}
	cache := newSymbolCache(lister)

	if _, err := cache.search(context.Background(), "ETH", 10); err == nil {
		t.Error("从未成功拉取过、这次又失败，应该报错而不是静默返回空列表")
	}
}

func indexOf(list []string, target string) int {
	for i, v := range list {
		if v == target {
			return i
		}
	}
	return -1
}

func TestHandleAPISymbolSearchReturnsRankedMatches(t *testing.T) {
	s := newTestServer(t, newFakeStore())
	s.symbolCache = newSymbolCache(&fakeSymbolLister{instruments: sampleInstruments()})

	w := getPage(s, "/api/symbols?q=eth")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	var got []string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if len(got) == 0 || got[0] != "ETHUSDT" {
		t.Errorf("首位结果 = %v，期望 ETHUSDT 排第一", got)
	}
	if reflect.DeepEqual(got, []string{}) {
		t.Error("查询 \"eth\" 不应返回空结果")
	}
}

func TestHandleAPISymbolSearchEmptyQueryReturnsEmptyArrayNotNull(t *testing.T) {
	s := newTestServer(t, newFakeStore())
	s.symbolCache = newSymbolCache(&fakeSymbolLister{instruments: sampleInstruments()})

	w := getPage(s, "/api/symbols?q=")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	// 必须是 "[]" 而不是 "null"：前端直接把响应体当数组 forEach，拿到 null 会抛异常。
	if got := w.Body.String(); got != "[]\n" {
		t.Errorf("响应体 = %q，期望空数组 \"[]\"", got)
	}
}
