package strategy

import (
	"errors"
	"fmt"
	"time"

	"tradeforge/pkg/types"
)

// 状态机是平台的安全闸门，不是流程装饰。
//
// 合法流转固定为：
//
//	DRAFT → BACKTESTED → PAPER_TRADING → LIVE_ELIGIBLE → LIVE
//
// 三条不可绕过的规则：
//  1. 每一步都必须逐级推进，不允许跳级（尤其是 DRAFT 直达 LIVE）
//  2. 除最后一步外，每步推进都要有数据支撑（回测结果 / 模拟盘统计）
//  3. LIVE_ELIGIBLE → LIVE 必须是用户的显式手动操作，系统永不自动推进

// allowedTransitions 是唯一的流转白名单。不在表里的一律拒绝。
var allowedTransitions = map[types.StrategyState][]types.StrategyState{
	types.StateDraft:        {types.StateBacktested},
	types.StateBacktested:   {types.StatePaperTrading, types.StateDraft},
	types.StatePaperTrading: {types.StateLiveEligible, types.StateSuspended},
	types.StateLiveEligible: {types.StateLive, types.StateSuspended},
	types.StateLive:         {types.StateSuspended},
	// 暂停后只能回到模拟盘重新验证，不能直接恢复实盘——
	// 触发过风控的策略必须重新证明自己。
	types.StateSuspended: {types.StatePaperTrading},
}

// Actor 标识发起流转的主体。
type Actor string

const (
	// ActorSystem 表示由系统基于数据自动推进（回测完成、模拟盘达标等）。
	ActorSystem Actor = "system"
	// ActorUser 表示由用户手动操作。
	ActorUser Actor = "user"
)

// Gate 是推进到某状态所需的门槛配置。
type Gate struct {
	// MinOutOfSampleSharpe 是进入 PAPER_TRADING 所需的最低样本外夏普比率。
	MinOutOfSampleSharpe float64
	// MaxDrawdownLimit 是允许的最大回撤上限（0.5 表示 50%）。
	MaxDrawdownLimit float64
	// MinOutOfSampleTrades 是样本外区间所需的最少交易笔数。
	// 一两笔交易的样本外指标没有统计意义，等于没验证过。
	MinOutOfSampleTrades int
	// MaxFeeDragRatio 是样本外区间手续费占毛利润的比例上限（0.5 表示 50%）。
	// 0 表示不启用这条检查。夏普/收益本身已经是扣完手续费之后的数字，
	// 这条是额外的、独立的信号：换手率过高、靠很薄的价差反复进出的策略，
	// 净指标可能刚好卡过门槛，但对手续费/滑点的变动极度敏感，值得单独标出来。
	MaxFeeDragRatio float64
	// MinPaperDuration 是模拟盘的最短运行时长。
	MinPaperDuration time.Duration
	// MinPaperTrades 是模拟盘的最少交易笔数。
	MinPaperTrades int
}

// DefaultGate 是平台默认门槛。
//
// 数值取得保守是有意的：门槛太松等于没有门槛，
// 而用户随时可以在配置里调高，却不该轻易调低。
func DefaultGate() Gate {
	return Gate{
		MinOutOfSampleSharpe: 0,
		MaxDrawdownLimit:     0.5,
		MinOutOfSampleTrades: 5,
		MaxFeeDragRatio:      0.5,
		MinPaperDuration:     7 * 24 * time.Hour,
		MinPaperTrades:       10,
	}
}

// PaperStats 是模拟盘的运行统计，用于判断能否进入 LIVE_ELIGIBLE。
type PaperStats struct {
	StartedAt  time.Time
	Now        time.Time
	TradeCount int
}

// Duration 返回模拟盘已运行的时长。
func (p PaperStats) Duration() time.Duration {
	if p.StartedAt.IsZero() || p.Now.IsZero() {
		return 0
	}
	return p.Now.Sub(p.StartedAt)
}

// TransitionRequest 是一次状态推进请求。
type TransitionRequest struct {
	From types.StrategyState
	To   types.StrategyState
	// Actor 是发起者。LIVE_ELIGIBLE → LIVE 只接受 ActorUser。
	Actor Actor
	// ActorID 是具体的操作者标识，写入审计日志。
	ActorID string
	// Reason 是推进理由，必填。
	Reason string
	// Backtest 是支撑 DRAFT → BACKTESTED 的回测结果。
	Backtest *types.BacktestResult
	// Paper 是支撑 PAPER_TRADING → LIVE_ELIGIBLE 的模拟盘统计。
	Paper *PaperStats
}

// TransitionError 表示一次流转被拒绝。
type TransitionError struct {
	From, To types.StrategyState
	Reason   string
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("拒绝从 %s 推进到 %s：%s", e.From, e.To, e.Reason)
}

// ErrManualOnly 表示该流转必须由用户手动发起。
var ErrManualOnly = errors.New("该流转必须由用户显式手动操作，系统不得自动推进")

// Evidence 是本次推进所依据的数据快照，写入审计日志。
type Evidence map[string]any

// CheckTransition 校验一次流转是否被允许，并返回写入审计的依据快照。
//
// 它是纯函数：不碰数据库、不改状态。这样状态机的全部规则都能被单元测试
// 直接覆盖，不必搭起一整套基础设施。
func CheckTransition(req TransitionRequest, gate Gate) (Evidence, error) {
	if !req.From.Valid() {
		return nil, &TransitionError{req.From, req.To, fmt.Sprintf("源状态 %q 不合法", req.From)}
	}
	if !req.To.Valid() {
		return nil, &TransitionError{req.From, req.To, fmt.Sprintf("目标状态 %q 不合法", req.To)}
	}
	if req.From == req.To {
		return nil, &TransitionError{req.From, req.To, "源状态与目标状态相同"}
	}
	if req.Reason == "" {
		return nil, &TransitionError{req.From, req.To, "必须说明推进理由，审计日志不接受空理由"}
	}

	if !isAllowed(req.From, req.To) {
		return nil, &TransitionError{req.From, req.To,
			fmt.Sprintf("不是合法的流转路径；%s 只能推进到 %v", req.From, allowedTransitions[req.From])}
	}

	switch {
	case req.From == types.StateDraft && req.To == types.StateBacktested:
		return checkBacktestGate(req, gate)

	case req.To == types.StateLive:
		// 平台最重要的一道闸门：实盘只能由人点头。
		if req.Actor != ActorUser {
			return nil, fmt.Errorf("从 %s 推进到 %s：%w", req.From, req.To, ErrManualOnly)
		}
		if req.ActorID == "" {
			return nil, &TransitionError{req.From, req.To, "手动解锁实盘必须记录操作者身份"}
		}
		return Evidence{
			"unlocked_by": req.ActorID,
			"unlocked_at": time.Now().UTC().Format(time.RFC3339),
		}, nil

	case req.From == types.StatePaperTrading && req.To == types.StateLiveEligible:
		return checkPaperGate(req, gate)

	default:
		// 其余流转（进入模拟盘、暂停、退回草稿）没有数据门槛。
		return Evidence{"actor": string(req.Actor), "actor_id": req.ActorID}, nil
	}
}

func isAllowed(from, to types.StrategyState) bool {
	for _, s := range allowedTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// GateCriterion 是门槛里的一条具体检查项：当前值、要求值、是否达标。
//
// EvaluateBacktestGate/EvaluatePaperGate 和 checkBacktestGate/checkPaperGate
// 共用同一份计算——界面上"距离下一步还差什么"要展示的每一行，跟真正决定能不能
// 推进状态的判断必须是同一处代码算出来的，不能各写一份、慢慢就对不上了。
type GateCriterion struct {
	// Label 是检查项名称，如"样本外夏普比率"。
	Label string
	// Current 是当前值的展示文案。
	Current string
	// Required 是门槛的展示文案，如 "> 0.000"。
	Required string
	Pass     bool
	// Reason 只在 Pass=false 时有意义，是拒绝流转时 TransitionError 的具体理由。
	Reason string
}

// EvaluateBacktestGate 逐条算出回测门槛的检查结果。
//
// 只看样本外指标：样本内的漂亮数字可能只是参数在那段数据上过拟合的结果，
// 拿它当门槛等于没有门槛。
func EvaluateBacktestGate(bt *types.BacktestResult, gate Gate) []GateCriterion {
	oos := bt.OutOfSample
	criteria := []GateCriterion{
		{
			Label:    "样本外交易笔数",
			Current:  fmt.Sprintf("%d 笔", oos.TradeCount),
			Required: fmt.Sprintf(">= %d 笔", gate.MinOutOfSampleTrades),
			Pass:     oos.TradeCount >= gate.MinOutOfSampleTrades,
			Reason: fmt.Sprintf(
				"样本外只有 %d 笔交易，低于要求的 %d 笔；样本太少时的指标没有统计意义，等同于未验证",
				oos.TradeCount, gate.MinOutOfSampleTrades),
		},
		{
			Label:    "样本外夏普比率",
			Current:  fmt.Sprintf("%.3f", oos.SharpeRatio),
			Required: fmt.Sprintf("> %.3f", gate.MinOutOfSampleSharpe),
			Pass:     oos.SharpeRatio > gate.MinOutOfSampleSharpe,
			Reason:   fmt.Sprintf("样本外夏普 %.3f 未超过门槛 %.3f", oos.SharpeRatio, gate.MinOutOfSampleSharpe),
		},
	}
	if gate.MaxDrawdownLimit > 0 {
		criteria = append(criteria, GateCriterion{
			Label:    "样本外最大回撤",
			Current:  fmt.Sprintf("%.2f%%", oos.MaxDrawdown*100),
			Required: fmt.Sprintf("<= %.2f%%", gate.MaxDrawdownLimit*100),
			Pass:     oos.MaxDrawdown <= gate.MaxDrawdownLimit,
			Reason: fmt.Sprintf("样本外最大回撤 %.2f%% 超过上限 %.2f%%",
				oos.MaxDrawdown*100, gate.MaxDrawdownLimit*100),
		})
	}
	if gate.MaxFeeDragRatio > 0 {
		if feeDrag, ok := oos.FeeDragRatio(); ok {
			criteria = append(criteria, GateCriterion{
				Label:    "手续费占毛利润比例",
				Current:  fmt.Sprintf("%.1f%%", feeDrag*100),
				Required: fmt.Sprintf("<= %.1f%%", gate.MaxFeeDragRatio*100),
				Pass:     feeDrag <= gate.MaxFeeDragRatio,
				Reason: fmt.Sprintf(
					"样本外毛利润里有 %.1f%% 被手续费吃掉，超过上限 %.1f%%；换手率过高，对手续费/滑点太敏感",
					feeDrag*100, gate.MaxFeeDragRatio*100),
			})
		} else {
			// 毛利润非正、算不出比例时不拦截（跟下面的判断逻辑一致），但仍然展示
			// 这一项，界面上不能悄悄漏掉一条门槛。
			criteria = append(criteria, GateCriterion{
				Label:    "手续费占毛利润比例",
				Current:  "—（还没有正的毛利润，暂时算不出比例）",
				Required: fmt.Sprintf("<= %.1f%%", gate.MaxFeeDragRatio*100),
				Pass:     true,
			})
		}
	}
	return criteria
}

// checkBacktestGate 校验回测结果是否达标。
func checkBacktestGate(req TransitionRequest, gate Gate) (Evidence, error) {
	if req.Backtest == nil {
		return nil, &TransitionError{req.From, req.To, "缺少回测结果，无法判断是否达标"}
	}
	oos := req.Backtest.OutOfSample

	ev := Evidence{
		"backtest_id":          req.Backtest.ID,
		"out_of_sample_sharpe": oos.SharpeRatio,
		"out_of_sample_trades": oos.TradeCount,
		"out_of_sample_maxdd":  oos.MaxDrawdown,
		"out_of_sample_return": oos.TotalReturn,
		"engine_version":       req.Backtest.EngineVersion,
		"gate_min_sharpe":      gate.MinOutOfSampleSharpe,
		"gate_min_trades":      gate.MinOutOfSampleTrades,
		"gate_max_drawdown":    gate.MaxDrawdownLimit,
		"gate_max_fee_drag":    gate.MaxFeeDragRatio,
	}
	if feeDrag, ok := oos.FeeDragRatio(); ok {
		ev["out_of_sample_fee_drag"] = feeDrag
	}

	for _, c := range EvaluateBacktestGate(req.Backtest, gate) {
		if !c.Pass {
			return nil, &TransitionError{req.From, req.To, c.Reason}
		}
	}

	return ev, nil
}

// EvaluatePaperGate 逐条算出模拟盘门槛的检查结果。
//
// 时长和笔数都要满足：只跑够时长但一笔没成交，说明策略在真实行情里根本不触发；
// 只跑够笔数但时间太短，说明只见过一种市况。
func EvaluatePaperGate(paper *PaperStats, gate Gate) []GateCriterion {
	elapsed := paper.Duration()
	// 展示用的时长四舍五入到秒——elapsed 本身是"现在减去起始时间"算出来的，
	// 带着一串没意义的纳秒尾数（比如 72h0m17.1800002s），秒级精度对人已经够看了；
	// 判定通过与否仍然用未取整的 elapsed，不受这行展示格式影响。
	elapsedDisplay := elapsed.Round(time.Second)
	return []GateCriterion{
		{
			Label:    "模拟盘运行时长",
			Current:  elapsedDisplay.String(),
			Required: fmt.Sprintf(">= %s", gate.MinPaperDuration),
			Pass:     elapsed >= gate.MinPaperDuration,
			Reason:   fmt.Sprintf("模拟盘只运行了 %s，未达到要求的 %s", elapsedDisplay, gate.MinPaperDuration),
		},
		{
			Label:    "模拟盘成交笔数",
			Current:  fmt.Sprintf("%d 笔", paper.TradeCount),
			Required: fmt.Sprintf(">= %d 笔", gate.MinPaperTrades),
			Pass:     paper.TradeCount >= gate.MinPaperTrades,
			Reason: fmt.Sprintf("模拟盘只成交了 %d 笔，未达到要求的 %d 笔",
				paper.TradeCount, gate.MinPaperTrades),
		},
	}
}

// checkPaperGate 校验模拟盘是否跑够时长与笔数。
func checkPaperGate(req TransitionRequest, gate Gate) (Evidence, error) {
	if req.Paper == nil {
		return nil, &TransitionError{req.From, req.To, "缺少模拟盘统计，无法判断是否达标"}
	}
	elapsed := req.Paper.Duration()

	ev := Evidence{
		"paper_started_at":  req.Paper.StartedAt.UTC().Format(time.RFC3339),
		"paper_duration":    elapsed.String(),
		"paper_trade_count": req.Paper.TradeCount,
		"gate_min_duration": gate.MinPaperDuration.String(),
		"gate_min_trades":   gate.MinPaperTrades,
	}

	for _, c := range EvaluatePaperGate(req.Paper, gate) {
		if !c.Pass {
			return nil, &TransitionError{req.From, req.To, c.Reason}
		}
	}

	return ev, nil
}

// CanTradeLive 报告某状态下是否允许下真实单。
//
// 执行层在每次下单前都应当调用它。把这个判断集中在一处，
// 是为了避免"某个分支忘了检查状态"这类错误直接变成真金白银的损失。
func CanTradeLive(state types.StrategyState) bool {
	return state == types.StateLive
}

// CanTradePaper 报告某状态下是否允许跑模拟交易。
func CanTradePaper(state types.StrategyState) bool {
	return state == types.StatePaperTrading || state == types.StateLiveEligible
}

// NextStates 返回某状态的全部合法后继，供界面展示可用操作。
func NextStates(state types.StrategyState) []types.StrategyState {
	out := make([]types.StrategyState, len(allowedTransitions[state]))
	copy(out, allowedTransitions[state])
	return out
}
