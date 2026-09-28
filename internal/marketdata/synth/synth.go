// Package synth generates deterministic synthetic candles for unit tests and
// local demos.
//
// It deliberately introduces no randomness beyond an explicit RNG seed: the
// same parameters always produce the same data, so tests assert on algorithm
// behavior rather than on data that just happens to work.
package synth

import (
	"math"
	"math/rand"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// Builder constructs a candle sequence one candle at a time.
type Builder struct {
	symbol    string
	timeframe types.Timeframe
	start     time.Time
	candles   []types.Candle
}

// New creates a builder starting at start with the given timeframe.
func New(symbol string, tf types.Timeframe, start time.Time) *Builder {
	return &Builder{symbol: symbol, timeframe: tf, start: start}
}

// Add appends one candle. Price/volume are written as float for test
// readability and converted to decimal internally for storage.
//
// Taker buy volume is split from total volume by takerBuyRatio: 0.5 means an
// even split.
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

// AddFlat appends a candle centered on price with almost no movement.
func (b *Builder) AddFlat(price, volume float64) *Builder {
	return b.Add(price, price*1.0005, price*0.9995, price, volume, 0.5)
}

// AddBar appends a candle moving from open to close, with wicks auto-generated
// at 20% of the body.
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

// Build returns the constructed MarketData.
func (b *Builder) Build() types.MarketData {
	return types.MarketData{Symbol: b.symbol, Timeframe: b.timeframe, Candles: b.candles}
}

// Len returns the number of candles constructed so far.
func (b *Builder) Len() int { return len(b.candles) }

// Oscillate appends n candles that swing back and forth between [low, high],
// used to produce price action that repeatedly touches the same
// support/resistance level.
//
// The wick handling is deliberate: only the candle that reaches an endpoint
// (the "arrival candle") gets a wick in that endpoint's direction; the first
// candle turning back from the opposite side does not. Otherwise two adjacent
// candles would leave equal-height highs, and equal-height adjacent highs by
// definition don't form a swing point — the whole run would fail to identify
// a single key level.
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
		// The remaining candles are split evenly toward the target price, so
		// the last one lands exactly on the endpoint.
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

// Trend appends n candles moving in one direction, walking linearly from from
// to to.
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

// RandomWalk appends n pseudo-random-walk candles. A fixed seed makes the
// result fully reproducible.
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
