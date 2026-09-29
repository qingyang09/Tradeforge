package cvdorderflow

import (
	"context"

	"github.com/shopspring/decimal"

	"tradeforge/internal/i18n"
	"tradeforge/pkg/types"
)

// Delta is the taker buy/sell net difference (taker buy - taker sell) on one candle.
type Delta struct {
	// Net positive means taker buying dominates; negative means taker selling dominates.
	Net decimal.Decimal
	// Total is this candle's total volume, used to normalize Net into an
	// imbalance ratio in [-1, 1].
	Total decimal.Decimal
}

// DataSourceError indicates a FlowProvider could not produce order-flow data
// at all (as opposed to a normal "insufficient data" case, which the module
// reports as a neutral Signal instead). Its Reason is a translatable
// types.Message rather than a pre-rendered string, since flow.go runs
// headlessly and has no idea what language whoever eventually sees this error
// prefers -- see types.Message's doc comment.
type DataSourceError struct {
	reason types.Message
}

// Error implements the error interface, rendering in English for logs and Go
// error-handling code. Callers that want a bilingual render should use Reason
// directly instead (see internal/execution.BrokerConfigError for the same
// convention).
func (e *DataSourceError) Error() string {
	return i18n.Render(i18n.LangEN, e.reason)
}

// Reason returns this error's translatable message.
func (e *DataSourceError) Reason() types.Message { return e.reason }

// FlowProvider abstracts the source of order-flow data.
//
// This is the extension point for later plugging in a real order-flow data
// source such as Coinglass: implement this interface and inject it when
// constructing the module, and the module's algorithm itself needs no changes.
type FlowProvider interface {
	// Name returns the data source's identifier; it's written into Signal.Raw
	// so it's immediately clear which kind of data a signal was computed from.
	Name() string
	// Deltas returns a net-difference series aligned one-to-one with
	// md.Candles. A length mismatch is treated by the module as a data source
	// failure.
	Deltas(ctx context.Context, md types.MarketData) ([]Delta, error)
}

// CandleFlowProvider derives the net difference from the candle's own taker buy volume.
//
// Exchanges like Binance provide takerBuyBaseVolume directly in their candle
// endpoint; this is the current default data source — lower precision than
// tick-by-tick order flow, but needs no extra data source.
type CandleFlowProvider struct{}

// Name implements FlowProvider.
func (CandleFlowProvider) Name() string { return "candle_taker_volume" }

// Deltas implements FlowProvider.
func (CandleFlowProvider) Deltas(_ context.Context, md types.MarketData) ([]Delta, error) {
	out := make([]Delta, len(md.Candles))
	missing := 0
	for i, c := range md.Candles {
		if c.Volume.IsPositive() && c.TakerBuyVolume.IsZero() {
			missing++
		}
		out[i] = Delta{
			Net:   c.TakerBuyVolume.Sub(c.TakerSellVolume()),
			Total: c.Volume,
		}
	}
	// Having volume but no taker buy volume means the data source simply never
	// populated that field. In that case the net difference is always
	// -Volume, which would fabricate a string of purely fictitious bearish
	// imbalances — this must error rather than be computed as if valid.
	if missing > 0 && missing == countWithVolume(md.Candles) {
		return nil, &DataSourceError{reason: types.Msg("modules.cvd_orderflow.error.missing_taker_volume", "count", missing)}
	}
	return out, nil
}

func countWithVolume(candles []types.Candle) int {
	n := 0
	for _, c := range candles {
		if c.Volume.IsPositive() {
			n++
		}
	}
	return n
}

// SyntheticFlowProvider derives a placeholder net difference from candle shape
// when real taker buy/sell volume isn't available.
//
// Net = volume × (close - open) / (high - low), i.e. volume is allocated by
// the candle body's direction and strength. This is only a placeholder
// implementation to keep the pipeline running when order-flow data isn't
// available, and must never be used as real order flow for live decisions —
// it therefore explicitly tags its data source in the signal, so downstream
// code can refuse to let it through.
type SyntheticFlowProvider struct{}

// Name implements FlowProvider.
func (SyntheticFlowProvider) Name() string { return "synthetic_from_candles" }

// Deltas implements FlowProvider.
func (SyntheticFlowProvider) Deltas(_ context.Context, md types.MarketData) ([]Delta, error) {
	out := make([]Delta, len(md.Candles))
	for i, c := range md.Candles {
		rng := c.Range()
		if !rng.IsPositive() || !c.Volume.IsPositive() {
			out[i] = Delta{Net: decimal.Zero, Total: c.Volume}
			continue
		}
		bias := c.Close.Sub(c.Open).Div(rng) // [-1, 1]
		out[i] = Delta{Net: c.Volume.Mul(bias), Total: c.Volume}
	}
	return out, nil
}

// IsSynthetic reports whether a data source is a placeholder implementation.
// The execution layer can use this to refuse to let a live strategy use it.
func IsSynthetic(p FlowProvider) bool {
	_, ok := p.(SyntheticFlowProvider)
	return ok
}
