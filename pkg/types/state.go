package types

// StrategyState 是策略在验证闭环中的状态。
//
// 合法流转固定为：
//
//	DRAFT → BACKTESTED → PAPER_TRADING → LIVE_ELIGIBLE → LIVE
//
// 状态机本身（含门槛校验与审计日志）实现在 internal/engine/statemachine.go。
// 这里只放常量，避免下层包为了引用状态名而依赖上层包。
type StrategyState string

const (
	// StateDraft 草稿：Agent 刚翻译出的配置，尚未回测。
	StateDraft StrategyState = "DRAFT"
	// StateBacktested 已回测：回测跑完且样本外结果达到门槛。
	StateBacktested StrategyState = "BACKTESTED"
	// StatePaperTrading 模拟盘：用实时行情跑模拟交易，不下真实单。
	StatePaperTrading StrategyState = "PAPER_TRADING"
	// StateLiveEligible 具备实盘资格：模拟盘时长与笔数均已达标，等待用户手动解锁。
	StateLiveEligible StrategyState = "LIVE_ELIGIBLE"
	// StateLive 实盘：真实下单中。
	StateLive StrategyState = "LIVE"
	// StateSuspended 已暂停：触发风控或用户手动停止。
	StateSuspended StrategyState = "SUSPENDED"
)

// AllStates 按流程顺序列出全部状态。
var AllStates = []StrategyState{
	StateDraft, StateBacktested, StatePaperTrading, StateLiveEligible, StateLive, StateSuspended,
}

// Valid 报告状态是否为已定义的取值。
func (s StrategyState) Valid() bool {
	for _, v := range AllStates {
		if v == s {
			return true
		}
	}
	return false
}
