// Package marketdata 负责行情数据的加载与格式转换。
package marketdata

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// 支持的 CSV 列名。首行必须是表头；列顺序任意，缺列会明确报错。
const (
	colOpenTime  = "open_time"
	colOpen      = "open"
	colHigh      = "high"
	colLow       = "low"
	colClose     = "close"
	colVolume    = "volume"
	colTakerBuy  = "taker_buy_volume"
	colCloseTime = "close_time"
	colTrades    = "trades"
)

// LoadCSV 从文件读取历史 K 线。
//
// 价量字段一律用 decimal.NewFromString 解析，绝不经过 strconv.ParseFloat——
// 那一步会在读入阶段就悄悄引入精度误差，后面再怎么用 decimal 都补不回来。
func LoadCSV(path string, symbol string, tf types.Timeframe) (types.MarketData, error) {
	f, err := os.Open(path)
	if err != nil {
		return types.MarketData{}, fmt.Errorf("打开行情文件失败：%w", err)
	}
	defer f.Close()
	return ReadCSV(f, symbol, tf)
}

// ReadCSV 从 io.Reader 读取历史 K 线。
func ReadCSV(r io.Reader, symbol string, tf types.Timeframe) (types.MarketData, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return types.MarketData{}, fmt.Errorf("读取表头失败：%w", err)
	}
	idx := make(map[string]int, len(header))
	for i, name := range header {
		idx[strings.ToLower(strings.TrimSpace(name))] = i
	}
	for _, required := range []string{colOpenTime, colOpen, colHigh, colLow, colClose, colVolume} {
		if _, ok := idx[required]; !ok {
			return types.MarketData{}, fmt.Errorf("行情文件缺少必需的列 %q（现有列：%v）", required, header)
		}
	}

	md := types.MarketData{Symbol: symbol, Timeframe: tf}
	line := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		line++
		if err != nil {
			return types.MarketData{}, fmt.Errorf("第 %d 行解析失败：%w", line, err)
		}

		c, err := parseRow(rec, idx, tf)
		if err != nil {
			return types.MarketData{}, fmt.Errorf("第 %d 行：%w", line, err)
		}
		md.Candles = append(md.Candles, c)
	}

	if len(md.Candles) == 0 {
		return types.MarketData{}, fmt.Errorf("行情文件中没有任何 K 线数据")
	}

	// 模块假定 Candles 按时间升序，这里主动排序而不是相信输入文件。
	sort.Slice(md.Candles, func(i, j int) bool {
		return md.Candles[i].OpenTime.Before(md.Candles[j].OpenTime)
	})
	if err := checkContinuity(md); err != nil {
		return types.MarketData{}, err
	}
	return md, nil
}

func parseRow(rec []string, idx map[string]int, tf types.Timeframe) (types.Candle, error) {
	get := func(col string) string {
		i, ok := idx[col]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	openTime, err := parseTime(get(colOpenTime))
	if err != nil {
		return types.Candle{}, fmt.Errorf("open_time 解析失败：%w", err)
	}

	c := types.Candle{OpenTime: openTime}
	for _, field := range []struct {
		col string
		dst *decimal.Decimal
	}{
		{colOpen, &c.Open}, {colHigh, &c.High}, {colLow, &c.Low},
		{colClose, &c.Close}, {colVolume, &c.Volume},
	} {
		v, err := decimal.NewFromString(get(field.col))
		if err != nil {
			return types.Candle{}, fmt.Errorf("%s 不是合法数值：%w", field.col, err)
		}
		*field.dst = v
	}

	if s := get(colTakerBuy); s != "" {
		v, err := decimal.NewFromString(s)
		if err != nil {
			return types.Candle{}, fmt.Errorf("taker_buy_volume 不是合法数值：%w", err)
		}
		c.TakerBuyVolume = v
	}
	if s := get(colTrades); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return types.Candle{}, fmt.Errorf("trades 不是整数：%w", err)
		}
		c.Trades = n
	}

	if s := get(colCloseTime); s != "" {
		t, err := parseTime(s)
		if err != nil {
			return types.Candle{}, fmt.Errorf("close_time 解析失败：%w", err)
		}
		c.CloseTime = t
	} else {
		c.CloseTime = openTime.Add(tf.Duration())
	}

	if c.High.LessThan(c.Low) {
		return types.Candle{}, fmt.Errorf("最高价 %s 低于最低价 %s，数据异常", c.High, c.Low)
	}
	return c, nil
}

// parseTime 接受 RFC3339 字符串或 Unix 毫秒/秒时间戳。
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("时间为空")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("既不是 RFC3339 也不是 Unix 时间戳：%q", s)
	}
	// 交易所的 K 线接口普遍用毫秒；13 位以上按毫秒解释。
	if n > 1e11 {
		return time.UnixMilli(n).UTC(), nil
	}
	return time.Unix(n, 0).UTC(), nil
}

// checkContinuity 检查时间序列是否有重复或倒序。
//
// 缺口只警告不报错（交易所维护期确实会缺 K 线），但重复时间戳会让
// 回测把同一根 K 线算两次，属于必须拦下的数据问题。
func checkContinuity(md types.MarketData) error {
	for i := 1; i < len(md.Candles); i++ {
		if !md.Candles[i].OpenTime.After(md.Candles[i-1].OpenTime) {
			return fmt.Errorf("第 %d 根与第 %d 根 K 线时间相同或倒序（%s）",
				i, i+1, md.Candles[i].OpenTime.Format(time.RFC3339))
		}
	}
	return nil
}

// WriteCSV 把 K 线写成 CSV，供生成测试数据使用。
func WriteCSV(w io.Writer, md types.MarketData) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	if err := cw.Write([]string{
		colOpenTime, colCloseTime, colOpen, colHigh, colLow, colClose, colVolume, colTakerBuy,
	}); err != nil {
		return err
	}
	for _, c := range md.Candles {
		if err := cw.Write([]string{
			c.OpenTime.UTC().Format(time.RFC3339),
			c.CloseTime.UTC().Format(time.RFC3339),
			c.Open.String(), c.High.String(), c.Low.String(),
			c.Close.String(), c.Volume.String(), c.TakerBuyVolume.String(),
		}); err != nil {
			return err
		}
	}
	return cw.Error()
}
