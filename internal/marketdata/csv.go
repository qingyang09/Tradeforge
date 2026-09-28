// Package marketdata handles loading and format conversion of market data.
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

// Supported CSV column names. The first row must be a header; column order is
// arbitrary, and a missing column produces an explicit error.
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

// LoadCSV reads historical candles from a file.
//
// Price/volume fields are always parsed with decimal.NewFromString, never
// strconv.ParseFloat — that step would silently introduce precision errors at
// read time that no amount of decimal usage downstream could recover from.
func LoadCSV(path string, symbol string, tf types.Timeframe) (types.MarketData, error) {
	f, err := os.Open(path)
	if err != nil {
		return types.MarketData{}, fmt.Errorf("failed to open market data file: %w", err)
	}
	defer f.Close()
	return ReadCSV(f, symbol, tf)
}

// ReadCSV reads historical candles from an io.Reader.
func ReadCSV(r io.Reader, symbol string, tf types.Timeframe) (types.MarketData, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return types.MarketData{}, fmt.Errorf("failed to read header: %w", err)
	}
	idx := make(map[string]int, len(header))
	for i, name := range header {
		idx[strings.ToLower(strings.TrimSpace(name))] = i
	}
	for _, required := range []string{colOpenTime, colOpen, colHigh, colLow, colClose, colVolume} {
		if _, ok := idx[required]; !ok {
			return types.MarketData{}, fmt.Errorf("market data file is missing required column %q (existing columns: %v)", required, header)
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
			return types.MarketData{}, fmt.Errorf("failed to parse line %d: %w", line, err)
		}

		c, err := parseRow(rec, idx, tf)
		if err != nil {
			return types.MarketData{}, fmt.Errorf("line %d: %w", line, err)
		}
		md.Candles = append(md.Candles, c)
	}

	if len(md.Candles) == 0 {
		return types.MarketData{}, fmt.Errorf("market data file contains no candle data")
	}

	// Modules assume Candles are in ascending time order, so we sort
	// proactively here rather than trust the input file.
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
		return types.Candle{}, fmt.Errorf("failed to parse open_time: %w", err)
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
			return types.Candle{}, fmt.Errorf("%s is not a valid number: %w", field.col, err)
		}
		*field.dst = v
	}

	if s := get(colTakerBuy); s != "" {
		v, err := decimal.NewFromString(s)
		if err != nil {
			return types.Candle{}, fmt.Errorf("taker_buy_volume is not a valid number: %w", err)
		}
		c.TakerBuyVolume = v
	}
	if s := get(colTrades); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return types.Candle{}, fmt.Errorf("trades is not an integer: %w", err)
		}
		c.Trades = n
	}

	if s := get(colCloseTime); s != "" {
		t, err := parseTime(s)
		if err != nil {
			return types.Candle{}, fmt.Errorf("failed to parse close_time: %w", err)
		}
		c.CloseTime = t
	} else {
		c.CloseTime = openTime.Add(tf.Duration())
	}

	if c.High.LessThan(c.Low) {
		return types.Candle{}, fmt.Errorf("high %s is below low %s, data is inconsistent", c.High, c.Low)
	}
	return c, nil
}

// parseTime accepts an RFC3339 string or a Unix millisecond/second timestamp.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("time is empty")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("not RFC3339 nor a Unix timestamp: %q", s)
	}
	// Exchange candle APIs commonly use milliseconds; 13+ digits are
	// interpreted as milliseconds.
	if n > 1e11 {
		return time.UnixMilli(n).UTC(), nil
	}
	return time.Unix(n, 0).UTC(), nil
}

// checkContinuity checks whether the time series has duplicate or
// out-of-order timestamps.
//
// Gaps are not an error (exchange maintenance windows genuinely produce
// missing candles), but duplicate timestamps would make the backtest count
// the same candle twice, which is a data problem that must be caught.
func checkContinuity(md types.MarketData) error {
	for i := 1; i < len(md.Candles); i++ {
		if !md.Candles[i].OpenTime.After(md.Candles[i-1].OpenTime) {
			return fmt.Errorf("candle %d and candle %d have the same or out-of-order timestamp (%s)",
				i, i+1, md.Candles[i].OpenTime.Format(time.RFC3339))
		}
	}
	return nil
}

// WriteCSV writes candles out as CSV, for generating test data.
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
