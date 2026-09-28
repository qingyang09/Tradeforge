package cvdorderflow

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// Delta 是一根 K 线上的主动买卖净差（taker buy - taker sell）。
type Delta struct {
	// Net 为正表示主动买入占优，为负表示主动卖出占优。
	Net decimal.Decimal
	// Total 是该根 K 线的总成交量，用于把 Net 归一化成 [-1, 1] 的失衡度。
	Total decimal.Decimal
}

// FlowProvider 是订单流数据的来源抽象。
//
// 这是后续接入 Coinglass 等真实订单流数据源的扩展点：
// 只要实现这个接口并在构造模块时注入，模块算法本身完全不用改。
type FlowProvider interface {
	// Name 返回数据源标识，会写进 Signal.Raw，让人一眼看出信号是基于哪种数据算的。
	Name() string
	// Deltas 返回与 md.Candles 一一对应、等长的净差序列。
	// 长度不匹配会被模块视为数据源故障。
	Deltas(ctx context.Context, md types.MarketData) ([]Delta, error)
}

// CandleFlowProvider 从 K 线自带的主动买入量推导净差。
//
// Binance 等交易所的 K 线接口直接提供 takerBuyBaseVolume，这是当前默认的数据来源，
// 精度低于逐笔订单流但无需额外数据源。
type CandleFlowProvider struct{}

// Name 实现 FlowProvider。
func (CandleFlowProvider) Name() string { return "candle_taker_volume" }

// Deltas 实现 FlowProvider。
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
	// 有成交量却没有主动买入量，说明数据源根本没填这个字段。
	// 此时净差恒等于 -Volume，会造出一串纯属虚构的看空失衡，必须报错而不是照算。
	if missing > 0 && missing == countWithVolume(md.Candles) {
		return nil, fmt.Errorf("行情数据缺少主动买入量字段（%d 根 K 线），无法计算 CVD", missing)
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

// SyntheticFlowProvider 在缺少真实主动买卖量时，用 K 线形态推导一个占位净差。
//
// 净差 = 成交量 × (收 - 开) / (高 - 低)，即按 K 线实体方向和强度分配成交量。
// 这只是让链路能在没有订单流数据时跑起来的占位实现，绝不能当作真实订单流用于实盘决策，
// 因此它会在信号里显式标注数据源，下游可据此拒绝放行。
type SyntheticFlowProvider struct{}

// Name 实现 FlowProvider。
func (SyntheticFlowProvider) Name() string { return "synthetic_from_candles" }

// Deltas 实现 FlowProvider。
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

// IsSynthetic 报告某个数据源是否为占位实现。执行层可据此拒绝让实盘策略使用。
func IsSynthetic(p FlowProvider) bool {
	_, ok := p.(SyntheticFlowProvider)
	return ok
}
