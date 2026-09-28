package newssentiment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

// flatMarketData 构造一段行情，最后一根 K 线的收盘时间等于 base，方便用相对偏移
// 构造"发布于 N 小时前"的新闻。
func flatMarketData(symbol string, news ...types.NewsItem) types.MarketData {
	c := types.Candle{
		OpenTime: base.Add(-time.Hour), CloseTime: base,
		Open: decimal.NewFromInt(100), High: decimal.NewFromInt(100),
		Low: decimal.NewFromInt(100), Close: decimal.NewFromInt(100),
		Volume: decimal.NewFromInt(1000),
	}
	return types.MarketData{Symbol: symbol, Timeframe: types.TF1h, Candles: []types.Candle{c}, News: news}
}

func item(hoursAgo float64, headline string, symbols ...string) types.NewsItem {
	return types.NewsItem{
		Source: "test", Headline: headline,
		PublishedAt: base.Add(-time.Duration(hoursAgo * float64(time.Hour))),
		Symbols:     symbols,
	}
}

// ---- KeywordSentimentProvider 打分本身的正确性。----

func TestKeywordProviderScoresKnownHeadlines(t *testing.T) {
	p := KeywordSentimentProvider{}
	cases := []struct {
		headline string
		want     float64
	}{
		{"Bitcoin ETF approval sparks massive rally", 1},
		{"Exchange hacked, users report huge losses amid lawsuit", -1},
		{"Bitcoin price steady ahead of Fed meeting", 0},
		{"Rally fades as SEC files lawsuit over alleged fraud", -1.0 / 3},
	}
	for _, tc := range cases {
		t.Run(tc.headline, func(t *testing.T) {
			got, err := p.Score(context.Background(), types.NewsItem{Headline: tc.headline})
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if diff := got - tc.want; diff > 1e-9 || diff < -1e-9 {
				t.Errorf("Score(%q) = %v，期望 %v", tc.headline, got, tc.want)
			}
		})
	}
}

// ---- newsApplies：标的相关性过滤。----

func TestNewsAppliesMatching(t *testing.T) {
	cases := []struct {
		name   string
		symbol string
		tags   []string
		want   bool
	}{
		{"无标签视为全市场新闻", "BTCUSDT", nil, true},
		{"完全匹配", "BTCUSDT", []string{"BTCUSDT"}, true},
		{"基础资产前缀匹配", "BTCUSDT", []string{"BTC"}, true},
		{"大小写不敏感", "btcusdt", []string{"BTC"}, true},
		{"不相关标的", "BTCUSDT", []string{"ETH"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newsApplies(tc.symbol, tc.tags); got != tc.want {
				t.Errorf("newsApplies(%q, %v) = %v，期望 %v", tc.symbol, tc.tags, got, tc.want)
			}
		})
	}
}

// ---- Evaluate：端到端行为。----

func TestBullishNewsProducesLong(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(1, "Bitcoin ETF approval sparks massive rally", "BTC"),
		item(2, "Analysts note strong adoption and inflow into BTC funds", "BTC"),
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionLong {
		t.Fatalf("方向 = %s，期望 LONG（原因：%s）", sig.Direction, sig.Reason)
	}
	if sig.Raw["news_count"] != 2 {
		t.Errorf("news_count = %v，期望 2", sig.Raw["news_count"])
	}
	if sig.Raw["is_heuristic"] != true {
		t.Error("默认数据源应标注 is_heuristic=true")
	}
}

func TestBearishNewsProducesShort(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(1, "Exchange hacked, funds stolen amid exploit", "BTC"),
		item(2, "Regulators file lawsuit alleging fraud and ban trading", "BTC"),
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionShort {
		t.Fatalf("方向 = %s，期望 SHORT（原因：%s）", sig.Direction, sig.Reason)
	}
}

// 中性/矛盾标题应互相抵消，不触发方向信号。
func TestNeutralHeadlinesStayNeutral(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(1, "Bitcoin price steady ahead of Fed meeting", "BTC"),
		item(2, "Market awaits macro data release", "BTC"),
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL", sig.Direction)
	}
}

// 不相关标的的新闻必须被过滤掉，即使标题情绪很极端。
func TestUnrelatedSymbolNewsIgnored(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(1, "Ethereum ETF approval sparks massive rally", "ETH"),
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL（新闻与标的无关）", sig.Direction)
	}
	if sig.Raw["news_count"] != 0 {
		t.Errorf("news_count = %v，期望 0", sig.Raw["news_count"])
	}
}

// 窗口外（发布时间早于 lookback_hours）的新闻必须被排除。
func TestNewsOutsideWindowIgnored(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(48, "Bitcoin ETF approval sparks massive rally", "BTC"), // 48 小时前，超出默认 24 小时窗口
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, map[string]any{"lookback_hours": 24})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL（新闻已超出回看窗口）", sig.Direction)
	}
}

// 未来时间戳的新闻（数据错误或时钟不同步）不应被计入，避免用到"未来信息"。
func TestFutureDatedNewsIgnored(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(-1, "Bitcoin ETF approval sparks massive rally", "BTC"), // 发布时间在 md.Time() 之后
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL（未来时间戳的新闻不应计入）", sig.Direction)
	}
}

func TestMinNewsCountEnforced(t *testing.T) {
	md := flatMarketData("BTCUSDT",
		item(1, "Bitcoin ETF approval sparks massive rally", "BTC"),
	)
	sig, err := NewDefault().Evaluate(context.Background(), md, map[string]any{"min_news_count": 2})
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL（未达到 min_news_count）", sig.Direction)
	}
}

// 单条新闻支撑的信号置信度应低于两条同向新闻支撑的信号。
func TestConfidenceLowerWithSingleHeadline(t *testing.T) {
	single := flatMarketData("BTCUSDT", item(1, "Bitcoin ETF approval sparks massive rally", "BTC"))
	double := flatMarketData("BTCUSDT",
		item(1, "Bitcoin ETF approval sparks massive rally", "BTC"),
		item(1, "Major partnership announcement drives adoption breakout", "BTC"),
	)
	sigSingle, err := NewDefault().Evaluate(context.Background(), single, nil)
	if err != nil {
		t.Fatal(err)
	}
	sigDouble, err := NewDefault().Evaluate(context.Background(), double, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sigSingle.Direction != types.DirectionLong || sigDouble.Direction != types.DirectionLong {
		t.Fatalf("期望两者都是 LONG，得到 single=%s double=%s", sigSingle.Direction, sigDouble.Direction)
	}
	if sigSingle.Confidence >= sigDouble.Confidence {
		t.Errorf("单条新闻置信度 %v 应低于两条同向新闻的置信度 %v", sigSingle.Confidence, sigDouble.Confidence)
	}
}

func TestNoNewsIsNeutral(t *testing.T) {
	md := flatMarketData("BTCUSDT")
	sig, err := NewDefault().Evaluate(context.Background(), md, nil)
	if err != nil {
		t.Fatalf("没有新闻不应报错，得到：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL", sig.Direction)
	}
	if !sig.Price.Equal(decimal.NewFromInt(100)) {
		t.Errorf("Price = %s，期望带上参考收盘价 100", sig.Price)
	}
	if sig.Timestamp.IsZero() {
		t.Error("Timestamp 不能为零值")
	}
}

func TestEmptyMarketDataIsNeutral(t *testing.T) {
	sig, err := NewDefault().Evaluate(context.Background(), types.MarketData{Symbol: "BTCUSDT"}, nil)
	if err != nil {
		t.Fatalf("空行情不应报错，得到：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL", sig.Direction)
	}
}

func TestInvalidParamsRejected(t *testing.T) {
	md := flatMarketData("BTCUSDT", item(1, "Bitcoin rally", "BTC"))
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"回看窗口越界", map[string]any{"lookback_hours": 9999}},
		{"阈值越界", map[string]any{"sentiment_threshold": 1.5}},
		{"最小新闻数非整数", map[string]any{"min_news_count": 2.5}},
		{"未知参数", map[string]any{"decay": "linear"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewDefault().Evaluate(context.Background(), md, tc.params); err == nil {
				t.Fatalf("期望拒绝 %v，实际通过了", tc.params)
			}
		})
	}
}

func TestContextCancellationRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	md := flatMarketData("BTCUSDT")
	if _, err := NewDefault().Evaluate(ctx, md, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，得到：%v", err)
	}
}

// ScoreErrorProvider 用于验证打分数据源报错时模块如实透传，而不是吞掉或伪造中性信号。
type erroringProvider struct{ err error }

func (erroringProvider) Name() string { return "erroring" }
func (p erroringProvider) Score(context.Context, types.NewsItem) (float64, error) {
	return 0, p.err
}

func TestProviderErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	m := New(erroringProvider{err: boom})
	md := flatMarketData("BTCUSDT", item(1, "Bitcoin rally", "BTC"))
	_, err := m.Evaluate(context.Background(), md, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("期望错误透传 %v，得到 %v", boom, err)
	}
}
