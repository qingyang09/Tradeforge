// Package types 定义跨模块共享的核心数据结构。
//
// 约定：所有涉及金额、价格、数量的字段一律使用 decimal.Decimal，
// 禁止使用 float64。置信度、权重等无量纲的统计量允许使用 float64。
package types

import (
	"time"

	"github.com/shopspring/decimal"
)

// Direction 表示信号或订单的方向。
type Direction string

const (
	// DirectionLong 看多。
	DirectionLong Direction = "LONG"
	// DirectionShort 看空。
	DirectionShort Direction = "SHORT"
	// DirectionNeutral 中性 / 无观点。模块超时或降级时也使用该值。
	DirectionNeutral Direction = "NEUTRAL"
)

// Valid 报告方向是否为已定义的取值。
func (d Direction) Valid() bool {
	switch d {
	case DirectionLong, DirectionShort, DirectionNeutral:
		return true
	default:
		return false
	}
}

// Opposite 返回相反方向；中性的相反仍是中性。
func (d Direction) Opposite() Direction {
	switch d {
	case DirectionLong:
		return DirectionShort
	case DirectionShort:
		return DirectionLong
	default:
		return DirectionNeutral
	}
}

// Signal 是所有信号模块的标准化输出。
//
// 模块之间互不耦合，唯一的契约就是这个结构：组合引擎只认 Signal，
// 不关心模块内部是怎么算出来的。
type Signal struct {
	// Module 是产出该信号的模块名，对应 SignalModule.Name()。
	Module string `json:"module"`
	// Symbol 是标的，如 "BTCUSDT"。
	Symbol string `json:"symbol"`
	// Direction 是信号方向。
	Direction Direction `json:"direction"`
	// Confidence 是置信度，取值范围 [0, 1]。中性信号的置信度应为 0。
	Confidence float64 `json:"confidence"`
	// Timestamp 是信号对应的行情时间（不是模块的计算时间）。
	Timestamp time.Time `json:"timestamp"`
	// Price 是产生信号时的参考价格，通常为最后一根 K 线的收盘价。
	Price decimal.Decimal `json:"price"`
	// Reason 是人类可读的触发原因，用于可解释性展示。
	// 只描述"发生了什么"，不得包含任何投资建议措辞。
	Reason string `json:"reason"`
	// Raw 保存模块计算过程中的中间量（如支撑位、成交量倍数），
	// 供审计与界面展示使用。
	Raw map[string]any `json:"raw,omitempty"`
	// Degraded 为 true 表示该信号是降级产物（模块超时或报错后填充的中性信号），
	// 聚合与审计时需要区别对待。
	Degraded bool `json:"degraded,omitempty"`
	// Err 记录降级原因，仅在 Degraded 为 true 时有值。
	Err string `json:"err,omitempty"`
}

// NeutralSignal 构造一个中性信号，供模块在数据不足时返回。
func NeutralSignal(module, symbol, reason string, ts time.Time) Signal {
	return Signal{
		Module:     module,
		Symbol:     symbol,
		Direction:  DirectionNeutral,
		Confidence: 0,
		Timestamp:  ts,
		Reason:     reason,
	}
}

// DegradedSignal 构造一个降级信号，供组合引擎在模块超时/报错时填充。
func DegradedSignal(module, symbol string, err error, ts time.Time) Signal {
	s := NeutralSignal(module, symbol, "模块未产出信号，已降级为中性", ts)
	s.Degraded = true
	if err != nil {
		s.Err = err.Error()
	}
	return s
}
