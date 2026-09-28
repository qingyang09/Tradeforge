// Command module-demo 用构造好的假行情跑一遍每个信号模块，打印完整的 Signal 结构。
//
// 用途是人工确认模块逻辑是否合理——单元测试断言的是既定行为，
// 这个命令让人直接看到"什么样的行情产出什么样的信号"。
//
// 用法：go run ./cmd/module-demo
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"time"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/internal/modules"
	"tradeforge/internal/modules/cvdorderflow"
	"tradeforge/internal/modules/macdrsi"
	"tradeforge/internal/modules/newssentiment"
	"tradeforge/internal/modules/supportresistance"
	"tradeforge/internal/modules/volumebreakout"
	"tradeforge/pkg/types"
)

var start = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

type scenario struct {
	name   string
	module string
	params map[string]any
	data   types.MarketData
}

func main() {
	reg := modules.NewDefaultRegistry()

	fmt.Println("已注册模块：")
	for _, m := range reg.All() {
		fmt.Printf("  - %-20s %s\n", m.Name(), m.Description())
		for _, p := range m.RequiredParams() {
			fmt.Printf("      %-22s %-7s 默认 %-8v 范围 %s\n",
				p.Name, p.Type, p.Default, p.AllowedDesc())
		}
	}

	for _, sc := range allScenarios() {
		fmt.Printf("\n%s\n场景：%s\n模块：%s  参数：%v\n%s\n",
			strings.Repeat("=", 78), sc.name, sc.module, sc.params, strings.Repeat("-", 78))

		m, err := reg.Get(sc.module)
		if err != nil {
			fmt.Fprintf(os.Stderr, "取模块失败：%v\n", err)
			os.Exit(1)
		}
		sig, err := m.Evaluate(context.Background(), sc.data, sc.params)
		if err != nil {
			fmt.Printf("Evaluate 返回错误：%v\n", err)
			continue
		}
		b, _ := json.MarshalIndent(sig, "", "  ")
		fmt.Println(string(b))
	}
}

func allScenarios() []scenario {
	return []scenario{
		{
			name:   "震荡区间 100~110 后向上突破阻力",
			module: supportresistance.ModuleName,
			params: map[string]any{"pivot_strength": 1, "min_touches": 2},
			data: synth.New("BTCUSDT", types.TF1h, start).
				Oscillate(64, 100, 110, 1000).
				AddBar(110, 113, 1500, 0.65).
				Build(),
		},
		{
			name:   "单调上涨趋势，没有被反复触及的关键位",
			module: supportresistance.ModuleName,
			params: map[string]any{"min_touches": 3},
			data: synth.New("BTCUSDT", types.TF1h, start).
				Trend(80, 100, 200, 1000, 0.6).
				Build(),
		},
		{
			name:   "平量区间后放出 3 倍量的阳线",
			module: volumebreakout.ModuleName,
			params: map[string]any{"window": 20, "multiplier": 2.0},
			data: flatVolume(20, 100, 1000).
				AddBar(100, 105, 3000, 0.7).
				Build(),
		},
		{
			name:   "放量但只有 1.5 倍，未达阈值",
			module: volumebreakout.ModuleName,
			params: map[string]any{"window": 20, "multiplier": 2.0},
			data: flatVolume(20, 100, 1000).
				AddBar(100, 120, 1500, 0.9).
				Build(),
		},
		{
			name:   "持续主动买入占优（CVD 多头失衡）",
			module: cvdorderflow.ModuleName,
			params: map[string]any{"window": 50, "detect": cvdorderflow.DetectImbalance},
			data:   repeatBar(60, 100, 100.1, 1000, 0.8),
		},
		{
			name:   "价格上行但主动买盘净流出（CVD 顶背离）",
			module: cvdorderflow.ModuleName,
			params: map[string]any{"window": 50, "detect": cvdorderflow.DetectBoth},
			data: synth.New("BTCUSDT", types.TF1h, start).
				Trend(60, 100, 120, 1000, 0.35).
				Build(),
		},
		{
			name:   "急跌后反弹足以把 RSI 打到超卖区再拉回，RSI 反转模式捕获多头信号",
			module: macdrsi.ModuleName,
			params: map[string]any{"mode": macdrsi.ModeRSIReversal},
			data:   truncateAtFirstSignal(driftWalkReversal(30, -2.0, 2.0, 0.8), macdrsi.ModuleName, map[string]any{"mode": macdrsi.ModeRSIReversal}),
		},
		{
			name:   "下跌途中 MACD 死叉（macd_cross 模式，不管 RSI 已经跌到多深都照样触发）",
			module: macdrsi.ModuleName,
			params: map[string]any{"mode": macdrsi.ModeMACDCross},
			data:   truncateAtFirstSignal(driftWalkReversal(80, -1.2, 1.2, 1.0), macdrsi.ModuleName, map[string]any{"mode": macdrsi.ModeMACDCross}),
		},
		{
			name:   "同一根死叉 K 线改用 confluence 模式：RSI 已跌穿超卖区，判定动能透支，过滤掉这次空头信号",
			module: macdrsi.ModuleName,
			params: map[string]any{"mode": macdrsi.ModeConfluence},
			data:   truncateAtFirstSignal(driftWalkReversal(80, -1.2, 1.2, 1.0), macdrsi.ModuleName, map[string]any{"mode": macdrsi.ModeMACDCross}),
		},
		{
			name:   "24 小时内两条正面新闻（ETF 获批 + 资金流入），加权情绪得分越过阈值",
			module: newssentiment.ModuleName,
			params: map[string]any{},
			data: newsMarketData("BTCUSDT",
				newsItem(1, "Bitcoin ETF approval sparks massive rally", "BTC"),
				newsItem(6, "Analysts note strong adoption and inflow into BTC funds", "BTC"),
			),
		},
		{
			name:   "交易所被黑客攻击叠加监管诉讼，加权情绪得分触发空头",
			module: newssentiment.ModuleName,
			params: map[string]any{},
			data: newsMarketData("BTCUSDT",
				newsItem(2, "Exchange hacked, funds stolen amid exploit", "BTC"),
				newsItem(4, "Regulators file lawsuit alleging fraud and ban trading", "BTC"),
			),
		},
		{
			name:   "标题情绪很极端但标的是 ETH，跟 BTCUSDT 策略无关，过滤掉不计入",
			module: newssentiment.ModuleName,
			params: map[string]any{},
			data: newsMarketData("BTCUSDT",
				newsItem(1, "Ethereum ETF approval sparks massive rally", "ETH"),
			),
		},
	}
}

// truncateAtFirstSignal 逐根扩大窗口重放，把行情截到模块第一次给出非中性信号的那一根。
//
// 直接对完整序列调用一次 Evaluate 只能看到最后一根的状态——如果穿越发生在中途，
// 最后一根往往早已回到中性。演示的意义在于让人一眼看到"触发时刻"，所以要截到那一刻。
func truncateAtFirstSignal(full types.MarketData, moduleName string, params map[string]any) types.MarketData {
	reg := modules.NewDefaultRegistry()
	m, err := reg.Get(moduleName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "truncateAtFirstSignal: %v\n", err)
		os.Exit(1)
	}
	for k := 1; k <= len(full.Candles); k++ {
		md := types.MarketData{Symbol: full.Symbol, Timeframe: full.Timeframe, Candles: full.Candles[:k]}
		sig, err := m.Evaluate(context.Background(), md, params)
		if err != nil {
			continue // 数据不足阶段的参数校验期错误不影响后续更长窗口的判定
		}
		if sig.Direction != types.DirectionNeutral {
			return md
		}
	}
	return full // 没找到就退回完整序列，至少能看到最终状态
}

// driftWalkReversal 用带漂移的伪随机游走构造一段反转行情：先按 drift1 走 n 根，
// 再按 drift2 走 n 根（drift1 为负、drift2 为正即先跌后涨）。
//
// 纯线性趋势（synth.Builder.Trend）会让 MACD 线在几十根之内收敛到与信号线几乎
// 重合的常数——此后的差值都是浮点噪声量级，不代表真实的动量转向。加入噪声让
// 指标行为贴近真实行情，才能演示出有意义的金叉/死叉/RSI 反转。种子固定，结果可复现。
func driftWalkReversal(n int, drift1, drift2, noise float64) types.MarketData {
	b := synth.New("BTCUSDT", types.TF1h, start)
	takerA, takerB := 0.35, 0.65
	price := 150.0
	rng := rand.New(rand.NewSource(42))
	walk := func(steps int, drift, takerBuyRatio float64) {
		for i := 0; i < steps; i++ {
			next := price + drift + (rng.Float64()-0.5)*2*noise
			b.AddBar(price, next, 1000, takerBuyRatio)
			price = next
		}
	}
	walk(n, drift1, takerA)
	walk(n, drift2, takerB)
	return b.Build()
}

func flatVolume(n int, price, volume float64) *synth.Builder {
	b := synth.New("BTCUSDT", types.TF1h, start)
	for i := 0; i < n; i++ {
		b.AddBar(price, price, volume, 0.5)
	}
	return b
}

func repeatBar(n int, open, close, volume, takerRatio float64) types.MarketData {
	b := synth.New("BTCUSDT", types.TF1h, start)
	for i := 0; i < n; i++ {
		b.AddBar(open, close, volume, takerRatio)
	}
	return b.Build()
}

// newsMarketData 构造一根平静的 K 线（news_sentiment 只关心 News 字段，不关心价格走势）
// 外加给定的新闻列表，最后一根 K 线的收盘时间就是新闻时间戳的参照基准。
func newsMarketData(symbol string, news ...types.NewsItem) types.MarketData {
	md := synth.New(symbol, types.TF1h, start).AddFlat(100, 1000).Build()
	md.News = news
	return md
}

// newsItem 构造一条发布于"hoursAgo 小时前"（相对最后一根 K 线收盘时间）的新闻。
func newsItem(hoursAgo float64, headline string, symbols ...string) types.NewsItem {
	closeTime := start.Add(types.TF1h.Duration())
	return types.NewsItem{
		Source: "demo", Headline: headline,
		PublishedAt: closeTime.Add(-time.Duration(hoursAgo * float64(time.Hour))),
		Symbols:     symbols,
	}
}
