// Package synth 生成确定性的合成 K 线，供单元测试与本地演示使用。
//
// 它刻意不引入随机数种子以外的任何不确定性：同样的参数永远生成同样的数据，
// 这样测试断言的是算法行为，而不是碰巧的数据。
package synth

import (
	"math"
	"math/rand"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// Builder 逐根构造 K 线序列。
type Builder struct {
	symbol    string
	timeframe types.Timeframe
	start     time.Time
	candles   []types.Candle
}

// New 创建一个从 start 开始、指定周期的构造器。
func New(symbol string, tf types.Timeframe, start time.Time) *Builder {
	return &Builder{symbol: symbol, timeframe: tf, start: start}
}

// Add 追加一根 K 线，价量用 float 书写以便测试可读，内部转成 decimal 存储。
//
// 主动买入量按 takerBuyRatio 从总量中切分：0.5 表示买卖各半。
func (b *Builder) Add(open, high, low, close, volume, takerBuyRatio float64) *Builder {
	idx := len(b.candles)
	openTime := b.start.Add(time.Duration(idx) * b.timeframe.Duration())
	vol := decimal.NewFromFloat(volume)
	b.candles = append(b.candles, types.Candle{
		OpenTime:       openTime,
		CloseTime:      openTime.Add(b.timeframe.Duration()),
		Open:           decimal.NewFromFloat(open),
		High:           decimal.NewFromFloat(high),
		Low:            decimal.NewFromFloat(low),
		Close:          decimal.NewFromFloat(close),
		Volume:         vol,
		TakerBuyVolume: vol.Mul(decimal.NewFromFloat(takerBuyRatio)),
		Trades:         int64(volume),
	})
	return b
}

// AddFlat 追加一根以 price 为中心、几乎没有波动的 K 线。
func (b *Builder) AddFlat(price, volume float64) *Builder {
	return b.Add(price, price*1.0005, price*0.9995, price, volume, 0.5)
}

// AddBar 追加一根从 open 走到 close 的 K 线，影线按实体的 20% 自动生成。
func (b *Builder) AddBar(open, close, volume, takerBuyRatio float64) *Builder {
	body := math.Abs(close - open)
	wick := body * 0.2
	if body == 0 {
		wick = open * 0.0005
	}
	high := math.Max(open, close) + wick
	low := math.Min(open, close) - wick
	return b.Add(open, high, low, close, volume, takerBuyRatio)
}

// Build 返回构造好的 MarketData。
func (b *Builder) Build() types.MarketData {
	return types.MarketData{Symbol: b.symbol, Timeframe: b.timeframe, Candles: b.candles}
}

// Len 返回当前已构造的 K 线数量。
func (b *Builder) Len() int { return len(b.candles) }

// Oscillate 追加 n 根在 [low, high] 之间来回震荡的 K 线，
// 用于制造反复触及同一支撑/阻力位的行情。
//
// 影线的处理是刻意的：只有走到端点的那一根（"到达根"）才带出端点方向的影线，
// 从对侧折返的第一根不带。否则两根相邻 K 线会留下等高的高点，
// 而等高的相邻高点按定义不构成摆动点，整段行情会一个关键位都识别不出来。
func (b *Builder) Oscillate(n int, low, high, volume float64) *Builder {
	const stepsPerLeg = 4
	wick := (high - low) * 0.02

	price := low
	goingUp := true
	for i := 0; i < n; i++ {
		s := i % stepsPerLeg
		target := high
		if !goingUp {
			target = low
		}
		// 剩余根数均分到目标价，最后一根正好落在端点上。
		closePrice := price + (target-price)/float64(stepsPerLeg-s)
		arrival := s == stepsPerLeg-1

		var upperWick, lowerWick float64
		if arrival && goingUp {
			upperWick = wick
		}
		if arrival && !goingUp {
			lowerWick = wick
		}

		ratio := 0.45
		if goingUp {
			ratio = 0.55
		}
		b.Add(
			price,
			math.Max(price, closePrice)+upperWick,
			math.Min(price, closePrice)-lowerWick,
			closePrice, volume, ratio,
		)

		price = closePrice
		if arrival {
			goingUp = !goingUp
		}
	}
	return b
}

// Trend 追加 n 根单向推进的 K 线，从 from 线性走到 to。
func (b *Builder) Trend(n int, from, to, volume, takerBuyRatio float64) *Builder {
	if n <= 0 {
		return b
	}
	step := (to - from) / float64(n)
	price := from
	for i := 0; i < n; i++ {
		b.AddBar(price, price+step, volume, takerBuyRatio)
		price += step
	}
	return b
}

// RandomWalk 追加 n 根伪随机游走的 K 线。seed 固定则结果完全可复现。
func (b *Builder) RandomWalk(n int, start, volatility, volume float64, seed int64) *Builder {
	rng := rand.New(rand.NewSource(seed))
	price := start
	for i := 0; i < n; i++ {
		next := price * (1 + (rng.Float64()-0.5)*2*volatility)
		ratio := 0.4 + rng.Float64()*0.2
		b.AddBar(price, next, volume*(0.8+rng.Float64()*0.4), ratio)
		price = next
	}
	return b
}
