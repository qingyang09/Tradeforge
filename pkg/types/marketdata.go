package types

import (
	"errors"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// ErrInsufficientData means there aren't enough available candles to complete
// the computation. A module hitting this should return a neutral signal rather
// than an error, unless the parameters themselves are invalid.
var ErrInsufficientData = errors.New("insufficient market data")

// Timeframe is a candle period, using the exchange convention string form
// ("1m", "5m", "1h", "4h", "1d").
type Timeframe string

// Supported candle periods.
const (
	TF1m  Timeframe = "1m"
	TF5m  Timeframe = "5m"
	TF15m Timeframe = "15m"
	TF1h  Timeframe = "1h"
	TF4h  Timeframe = "4h"
	TF1d  Timeframe = "1d"
)

// SupportedTimeframes lists the periods the platform currently allows; the
// Agent translation layer uses it for value validation.
var SupportedTimeframes = []Timeframe{TF1m, TF5m, TF15m, TF1h, TF4h, TF1d}

// Duration returns the time length corresponding to the period. An unknown period returns 0.
func (t Timeframe) Duration() time.Duration {
	switch t {
	case TF1m:
		return time.Minute
	case TF5m:
		return 5 * time.Minute
	case TF15m:
		return 15 * time.Minute
	case TF1h:
		return time.Hour
	case TF4h:
		return 4 * time.Hour
	case TF1d:
		return 24 * time.Hour
	default:
		return 0
	}
}

// Valid reports whether the period is supported.
func (t Timeframe) Valid() bool { return t.Duration() > 0 }

// Candle is a single candlestick. All price/volume fields use decimal.
type Candle struct {
	OpenTime  time.Time       `json:"open_time"`
	CloseTime time.Time       `json:"close_time"`
	Open      decimal.Decimal `json:"open"`
	High      decimal.Decimal `json:"high"`
	Low       decimal.Decimal `json:"low"`
	Close     decimal.Decimal `json:"close"`
	Volume    decimal.Decimal `json:"volume"`
	// TakerBuyVolume is the taker buy volume (the portion of volume where takers hit
	// the ask). The CVD module derives taker sell volume as Volume - TakerBuyVolume.
	// It's zero when the data source doesn't provide it; modules must judge availability
	// themselves.
	TakerBuyVolume decimal.Decimal `json:"taker_buy_volume"`
	// Trades is the number of trades within this candle; 0 when the data source doesn't provide it.
	Trades int64 `json:"trades"`
}

// Range returns the difference between the high and low prices.
func (c Candle) Range() decimal.Decimal { return c.High.Sub(c.Low) }

// TakerSellVolume returns the taker sell volume, i.e. total volume minus taker buy volume.
// Returns zero if bad data would otherwise make the result negative.
func (c Candle) TakerSellVolume() decimal.Decimal {
	v := c.Volume.Sub(c.TakerBuyVolume)
	if v.IsNegative() {
		return decimal.Zero
	}
	return v
}

// MarketData is the candle slice passed to a module.
//
// Candles are sorted ascending by time; the last one is the most recent (and
// may not have closed yet). Modules must not modify the slice they receive.
type MarketData struct {
	Symbol    string    `json:"symbol"`
	Timeframe Timeframe `json:"timeframe"`
	Candles   []Candle  `json:"candles"`
	// News is an optional set of news items, for use by the news-sentiment module.
	News []NewsItem `json:"news,omitempty"`
}

// Last returns the most recent candle; the second return value is false when there's no data.
func (m MarketData) Last() (Candle, bool) {
	if len(m.Candles) == 0 {
		return Candle{}, false
	}
	return m.Candles[len(m.Candles)-1], true
}

// Tail returns the last n candles. Returns all of them if n exceeds the available
// count, and an empty slice if n <= 0.
func (m MarketData) Tail(n int) []Candle {
	if n <= 0 {
		return nil
	}
	if n >= len(m.Candles) {
		return m.Candles
	}
	return m.Candles[len(m.Candles)-n:]
}

// Time returns the close time of the last candle, or the zero value when there's no data.
func (m MarketData) Time() time.Time {
	c, ok := m.Last()
	if !ok {
		return time.Time{}
	}
	return c.CloseTime
}

// AlignAsOf returns the prefix of candles whose close time is no later than cutoff
// (candles must already be sorted ascending by time). This is the core tool for
// preventing a "not-yet-closed slower-timeframe candle from being seen early" during
// multi-timeframe replay: e.g. if the trigger timeframe is 15 minutes and a module
// uses 1-hour candles, when replaying up to a given 15-minute candle you can only
// feed it the 1-hour candles that have genuinely closed as of that moment —
// AlignAsOf(all 1h candles, current 15m candle's CloseTime) gives you exactly that
// safe prefix.
func AlignAsOf(candles []Candle, cutoff time.Time) []Candle {
	// candles is guaranteed sorted ascending by CloseTime; binary search for the first
	// candle whose close time is after cutoff.
	n := sort.Search(len(candles), func(i int) bool {
		return candles[i].CloseTime.After(cutoff)
	})
	return candles[:n]
}

// NewsItem is a single news item, consumed by the news-sentiment module in a later stage.
type NewsItem struct {
	Source      string    `json:"source"`
	Headline    string    `json:"headline"`
	PublishedAt time.Time `json:"published_at"`
	Symbols     []string  `json:"symbols,omitempty"`
}
