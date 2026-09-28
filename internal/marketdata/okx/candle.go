// Package okx 实现 OKX 交易所的行情接入：REST 回填历史 K 线、WebSocket 订阅实时 K 线。
//
// 选 OKX 而不是 CLAUDE.md 里默认提到的 Binance，是因为从这个开发环境实测：
// 访问 Binance 返回 451（地区封锁），OKX 可以正常访问——细节见项目根目录的规划记录。
//
// OKX 的 K 线接口不提供主动买卖量拆分，所以这里产出的 Candle.TakerBuyVolume 恒为零值。
// 用这个数据源的策略如果配了 cvd_orderflow 模块，默认的 CandleFlowProvider 会因为
// "有成交量却没有主动买入量"而拒绝计算（这是那个模块自己的既有防护，见
// internal/modules/cvdorderflow/flow.go），必须显式换成 SyntheticFlowProvider。
package okx

import (
	"fmt"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// parseCandleRow 解析 OKX 返回的一行 K 线数据。
//
// REST（/api/v5/market/candles）和 WebSocket（candle 频道推送）用的是同一套数组编码：
// [ts, open, high, low, close, vol, volCcy, volCcyQuote, confirm]，这里只写一份解析，
// 两条数据路径共用。confirm 为 "1" 表示这根 K 线已经收盘定型，"0" 表示还在变化中。
func parseCandleRow(row []string, tf types.Timeframe) (candle types.Candle, confirmed bool, err error) {
	if len(row) < 9 {
		return types.Candle{}, false, fmt.Errorf("OKX K 线数据字段数 = %d，期望至少 9 个", len(row))
	}

	tsMillis, err := strconv.ParseInt(row[0], 10, 64)
	if err != nil {
		return types.Candle{}, false, fmt.Errorf("解析开盘时间 %q 失败：%w", row[0], err)
	}
	openTime := time.UnixMilli(tsMillis).UTC()

	open, err := decimal.NewFromString(row[1])
	if err != nil {
		return types.Candle{}, false, fmt.Errorf("解析开盘价 %q 失败：%w", row[1], err)
	}
	high, err := decimal.NewFromString(row[2])
	if err != nil {
		return types.Candle{}, false, fmt.Errorf("解析最高价 %q 失败：%w", row[2], err)
	}
	low, err := decimal.NewFromString(row[3])
	if err != nil {
		return types.Candle{}, false, fmt.Errorf("解析最低价 %q 失败：%w", row[3], err)
	}
	closePrice, err := decimal.NewFromString(row[4])
	if err != nil {
		return types.Candle{}, false, fmt.Errorf("解析收盘价 %q 失败：%w", row[4], err)
	}
	volume, err := decimal.NewFromString(row[5])
	if err != nil {
		return types.Candle{}, false, fmt.Errorf("解析成交量 %q 失败：%w", row[5], err)
	}

	c := types.Candle{
		OpenTime:  openTime,
		CloseTime: openTime.Add(tf.Duration()),
		Open:      open,
		High:      high,
		Low:       low,
		Close:     closePrice,
		Volume:    volume,
		// TakerBuyVolume、Trades 保持零值：OKX 的 K 线接口不提供这两项。
	}
	return c, row[8] == "1", nil
}
