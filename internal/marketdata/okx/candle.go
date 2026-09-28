// Package okx implements market-data ingestion for the OKX exchange: REST
// backfills historical candles, WebSocket subscribes to live candles.
//
// OKX was chosen over the Binance default mentioned in CLAUDE.md because, as
// tested from this dev environment: Binance returns 451 (geo-blocked) while
// OKX is reachable normally — see the project's planning notes at the repo
// root for details.
//
// OKX's candle API doesn't provide a taker buy/sell volume split, so the
// Candle.TakerBuyVolume this package produces is always zero. If a strategy
// using this data source has the cvd_orderflow module configured, the default
// CandleFlowProvider will refuse to compute ("has volume but no taker buy
// volume" — this is that module's own existing safeguard, see
// internal/modules/cvdorderflow/flow.go), so it must be explicitly swapped
// for SyntheticFlowProvider.
package okx

import (
	"fmt"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// parseCandleRow parses one row of candle data returned by OKX.
//
// REST (/api/v5/market/candles) and WebSocket (candle channel pushes) use the
// same array encoding: [ts, open, high, low, close, vol, volCcy, volCcyQuote,
// confirm], so there's a single parser shared by both data paths. confirm
// "1" means the candle has closed and is final; "0" means it's still in
// progress.
func parseCandleRow(row []string, tf types.Timeframe) (candle types.Candle, confirmed bool, err error) {
	if len(row) < 9 {
		return types.Candle{}, false, fmt.Errorf("OKX candle data has %d fields, expected at least 9", len(row))
	}

	tsMillis, err := strconv.ParseInt(row[0], 10, 64)
	if err != nil {
		return types.Candle{}, false, fmt.Errorf("failed to parse open time %q: %w", row[0], err)
	}
	openTime := time.UnixMilli(tsMillis).UTC()

	open, err := decimal.NewFromString(row[1])
	if err != nil {
		return types.Candle{}, false, fmt.Errorf("failed to parse open price %q: %w", row[1], err)
	}
	high, err := decimal.NewFromString(row[2])
	if err != nil {
		return types.Candle{}, false, fmt.Errorf("failed to parse high price %q: %w", row[2], err)
	}
	low, err := decimal.NewFromString(row[3])
	if err != nil {
		return types.Candle{}, false, fmt.Errorf("failed to parse low price %q: %w", row[3], err)
	}
	closePrice, err := decimal.NewFromString(row[4])
	if err != nil {
		return types.Candle{}, false, fmt.Errorf("failed to parse close price %q: %w", row[4], err)
	}
	volume, err := decimal.NewFromString(row[5])
	if err != nil {
		return types.Candle{}, false, fmt.Errorf("failed to parse volume %q: %w", row[5], err)
	}

	c := types.Candle{
		OpenTime:  openTime,
		CloseTime: openTime.Add(tf.Duration()),
		Open:      open,
		High:      high,
		Low:       low,
		Close:     closePrice,
		Volume:    volume,
		// TakerBuyVolume and Trades stay zero: OKX's candle API doesn't
		// provide either field.
	}
	return c, row[8] == "1", nil
}
