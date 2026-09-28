package types

import (
	"errors"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// ErrInsufficientData 表示可用 K 线数量不足以完成计算。
// 模块遇到该情况应返回中性信号而不是报错，除非参数本身非法。
var ErrInsufficientData = errors.New("行情数据不足")

// Timeframe 是 K 线周期，使用交易所惯例的字符串表示（"1m"、"5m"、"1h"、"4h"、"1d"）。
type Timeframe string

// 已支持的 K 线周期。
const (
	TF1m  Timeframe = "1m"
	TF5m  Timeframe = "5m"
	TF15m Timeframe = "15m"
	TF1h  Timeframe = "1h"
	TF4h  Timeframe = "4h"
	TF1d  Timeframe = "1d"
)

// SupportedTimeframes 列出平台当前允许的周期，Agent 翻译层用它做取值校验。
var SupportedTimeframes = []Timeframe{TF1m, TF5m, TF15m, TF1h, TF4h, TF1d}

// Duration 返回周期对应的时间长度。未知周期返回 0。
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

// Valid 报告周期是否受支持。
func (t Timeframe) Valid() bool { return t.Duration() > 0 }

// Candle 是一根 K 线。所有价量字段使用 decimal。
type Candle struct {
	OpenTime  time.Time       `json:"open_time"`
	CloseTime time.Time       `json:"close_time"`
	Open      decimal.Decimal `json:"open"`
	High      decimal.Decimal `json:"high"`
	Low       decimal.Decimal `json:"low"`
	Close     decimal.Decimal `json:"close"`
	Volume    decimal.Decimal `json:"volume"`
	// TakerBuyVolume 是主动买入成交量（taker 吃卖单的部分）。
	// CVD 模块用 Volume - TakerBuyVolume 推导主动卖出量。
	// 数据源不提供时为零值，模块需自行判断可用性。
	TakerBuyVolume decimal.Decimal `json:"taker_buy_volume"`
	// Trades 是该 K 线内的成交笔数，数据源不提供时为 0。
	Trades int64 `json:"trades"`
}

// Range 返回最高价与最低价之差。
func (c Candle) Range() decimal.Decimal { return c.High.Sub(c.Low) }

// TakerSellVolume 返回主动卖出成交量，即总量减去主动买入量。
// 若数据异常导致结果为负，返回零。
func (c Candle) TakerSellVolume() decimal.Decimal {
	v := c.Volume.Sub(c.TakerBuyVolume)
	if v.IsNegative() {
		return decimal.Zero
	}
	return v
}

// MarketData 是传给模块的行情切片。
//
// Candles 按时间升序排列，最后一根是最新的（可能尚未收盘）。
// 模块不得修改传入的切片。
type MarketData struct {
	Symbol    string    `json:"symbol"`
	Timeframe Timeframe `json:"timeframe"`
	Candles   []Candle  `json:"candles"`
	// News 是可选的新闻条目，供后续新闻情绪模块使用。
	News []NewsItem `json:"news,omitempty"`
}

// Last 返回最后一根 K 线；无数据时第二个返回值为 false。
func (m MarketData) Last() (Candle, bool) {
	if len(m.Candles) == 0 {
		return Candle{}, false
	}
	return m.Candles[len(m.Candles)-1], true
}

// Tail 返回最后 n 根 K 线。n 大于可用数量时返回全部，n <= 0 时返回空切片。
func (m MarketData) Tail(n int) []Candle {
	if n <= 0 {
		return nil
	}
	if n >= len(m.Candles) {
		return m.Candles
	}
	return m.Candles[len(m.Candles)-n:]
}

// Time 返回最后一根 K 线的收盘时间，无数据时返回零值。
func (m MarketData) Time() time.Time {
	c, ok := m.Last()
	if !ok {
		return time.Time{}
	}
	return c.CloseTime
}

// AlignAsOf 返回 candles 里所有收盘时间不晚于 cutoff 的前缀（candles 必须已按时间
// 升序排列）。这是多周期重放时防止"还没收盘的慢周期数据被提前看到"的核心工具：
// 比如触发周期是 15 分钟、某模块用 1 小时判断，重放到某根 15 分钟 K 线时，只能把
// 这个时刻真实已经收盘的 1 小时 K 线喂给它，用 AlignAsOf(全部1小时K线, 当前15分钟K线.CloseTime)
// 就能拿到这个安全的前缀。
func AlignAsOf(candles []Candle, cutoff time.Time) []Candle {
	// candles 保证按 CloseTime 升序排列，二分找到第一根收盘时间晚于 cutoff 的位置。
	n := sort.Search(len(candles), func(i int) bool {
		return candles[i].CloseTime.After(cutoff)
	})
	return candles[:n]
}

// NewsItem 是一条新闻，供后续阶段的新闻情绪模块消费。
type NewsItem struct {
	Source      string    `json:"source"`
	Headline    string    `json:"headline"`
	PublishedAt time.Time `json:"published_at"`
	Symbols     []string  `json:"symbols,omitempty"`
}
