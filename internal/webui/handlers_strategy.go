package webui

import (
	"context"
	"errors"
	"net/http"

	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

type strategyDetailData struct {
	Strategy       types.StrategyConfig
	Transitions    []storage.Transition
	Decisions      []types.Decision
	Orders         []types.Order
	Backtest       *types.BacktestResult
	Chart          equityChart
	MonthlyReturns []monthlyReturn
	PaperStats     *strategy.PaperStats
	// GateProgress 是"距离下一步还差什么"卡片要渲染的数据——把 strategy.Gate 的
	// 门槛逐条列出当前值/要求值/是否达标，而不是等用户点了"进入模拟盘"/"解锁实盘"
	// 才在 Banner 里事后报告哪里没达标。
	GateProgress gateProgress
	// CanConfirmBacktest/CanStartPaperTrading/CanUnlockLive 只反映"当前状态允许
	// 尝试这一步"，不代表数据门槛一定达标——门槛校验在提交时发生，未达标会以
	// Banner 的形式给出具体原因，跟"解锁实盘"是同一套 UX。
	CanConfirmBacktest   bool
	CanStartPaperTrading bool
	CanUnlockLive        bool
	// Banner 是操作反馈（如解锁实盘的结果），普通 GET 请求下为空。
	Banner    string
	BannerErr bool
}

func (s *Server) handleStrategyDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !looksLikeUUID(id) {
		http.NotFound(w, r)
		return
	}
	userID, _ := currentUserID(r)
	data, err := s.loadStrategyDetail(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, "策略不存在", http.StatusNotFound)
			return
		}
		s.serverError(w, err)
		return
	}
	s.renderPage(w, r, data.Strategy.Name, "strategy_detail_content", data)
}

// loadStrategyDetail 汇总详情页需要的全部数据。
//
// 缺回测结果、从未进入过模拟盘都不是错误——分别用 nil 字段表示"还没有"，
// 只有策略本身不存在（或不属于当前用户，两者返回同一个 ErrNotFound）时才把错误
// 往上传。
func (s *Server) loadStrategyDetail(ctx context.Context, userID, id string) (strategyDetailData, error) {
	sc, err := s.store.GetStrategy(ctx, userID, id)
	if err != nil {
		return strategyDetailData{}, err
	}

	transitions, err := s.store.ListTransitions(ctx, userID, id)
	if err != nil {
		return strategyDetailData{}, err
	}
	decisions, err := s.store.ListDecisions(ctx, userID, id, 50)
	if err != nil {
		return strategyDetailData{}, err
	}
	orders, err := s.store.ListOrders(ctx, userID, id, 50)
	if err != nil {
		return strategyDetailData{}, err
	}

	data := strategyDetailData{
		Strategy:             sc,
		Transitions:          transitions,
		Decisions:            decisions,
		Orders:               orders,
		CanConfirmBacktest:   sc.State == types.StateDraft,
		CanStartPaperTrading: sc.State == types.StateBacktested,
		CanUnlockLive:        sc.State == types.StateLiveEligible,
	}

	switch bt, err := s.store.LatestBacktestResult(ctx, userID, id); {
	case err == nil:
		data.Backtest = &bt
		data.Chart = buildEquityChart(bt)
		data.MonthlyReturns = buildMonthlyReturns(bt.EquityCurve)
	case errors.Is(err, storage.ErrNotFound):
		// 尚未回测过，模板自己会展示"尚无回测结果"。
	default:
		return strategyDetailData{}, err
	}

	switch ps, err := s.store.PaperStats(ctx, userID, id); {
	case err == nil:
		data.PaperStats = &ps
	case errors.Is(err, storage.ErrNotFound):
		// 从未进入过模拟盘，模板直接不渲染这一节。
	default:
		return strategyDetailData{}, err
	}

	data.GateProgress = buildGateProgress(s.gate, sc.State, data.Backtest, data.PaperStats)

	return data, nil
}

// gateProgressItem 是"距离下一步还差什么"表格里的一行。
type gateProgressItem struct {
	Label    string
	Current  string
	Required string
	Pass     bool
}

// gateProgress 是策略详情页"距离下一步还差什么"卡片要渲染的数据。
type gateProgress struct {
	// TargetLabel 是这一步要推进到的目标的说明文案。留空表示当前状态没有
	// "下一步"这回事（LIVE 只能人工暂停，SUSPENDED 必须先回模拟盘），
	// 模板据此决定要不要渲染整张卡片。
	TargetLabel string
	// Items 非空时才是"有具体门槛数据可以逐条比对"的情况；为空时看 Note。
	Items   []gateProgressItem
	AllPass bool
	// Note 用于两种情况：这一步压根没有数据门槛（比如 BACKTESTED→PAPER_TRADING）；
	// 或者门槛存在但还没有数据可评估（比如还没运行过回测）。
	Note string
}

// buildGateProgress 把 strategy.Gate 的门槛翻译成界面能直接渲染的"当前值 vs 要求值"
// 列表，跟 strategy.CheckTransition 实际用来放行/拒绝的是同一份 EvaluateBacktestGate/
// EvaluatePaperGate 计算，不会出现界面说"全部达标"、真去推进时却被拒绝的不一致。
func buildGateProgress(gate strategy.Gate, state types.StrategyState, bt *types.BacktestResult, ps *strategy.PaperStats) gateProgress {
	// 这里的文案刻意不逐字重复下方具体操作表单的标题（"进入模拟盘"/"解锁实盘"/
	// "确认回测结果"），只客观描述"要满足什么条件"——避免以后哪个测试想用
	// strings.Contains 判断某个操作表单在不在页面上时，被这张卡片的说明文字
	// 意外撞上关键词，误判成"表单出现了"。
	switch state {
	case types.StateDraft:
		if bt == nil {
			return gateProgress{TargetLabel: "样本外回测达标", Note: "还没有回测结果，先在上面运行一次回测。"}
		}
		return criteriaToProgress("样本外回测达标", strategy.EvaluateBacktestGate(bt, gate))

	case types.StatePaperTrading:
		if ps == nil {
			return gateProgress{TargetLabel: "模拟盘运行达标", Note: "还没有模拟盘运行统计——开始跑模拟交易后系统会记录运行时长和成交笔数。"}
		}
		return criteriaToProgress("模拟盘运行达标", strategy.EvaluatePaperGate(ps, gate))

	case types.StateBacktested:
		return gateProgress{TargetLabel: "推进到模拟交易阶段", Note: "这一步没有额外的数据门槛，用下方表单即可继续。"}

	case types.StateLiveEligible:
		return gateProgress{TargetLabel: "开始真实下单", Note: "这一步没有数据门槛——必须由你在下方手动确认，系统不会自动推进。"}

	default:
		// LIVE：只能人工暂停，没有"下一步"。SUSPENDED：必须先退回模拟盘重新验证，
		// 跟 PAPER_TRADING 是同一套门槛，不重复展示。
		return gateProgress{}
	}
}

func criteriaToProgress(targetLabel string, criteria []strategy.GateCriterion) gateProgress {
	items := make([]gateProgressItem, len(criteria))
	allPass := true
	for i, c := range criteria {
		items[i] = gateProgressItem{Label: c.Label, Current: c.Current, Required: c.Required, Pass: c.Pass}
		if !c.Pass {
			allPass = false
		}
	}
	return gateProgress{TargetLabel: targetLabel, Items: items, AllPass: allPass}
}
