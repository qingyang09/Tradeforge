package strategy

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

func goodBacktest() *types.BacktestResult {
	return &types.BacktestResult{
		ID:            "bt-1",
		EngineVersion: "signal-replay/1.0.0",
		OutOfSample: types.PerformanceMetrics{
			SharpeRatio: 1.2,
			TradeCount:  20,
			MaxDrawdown: 0.15,
			TotalReturn: 0.3,
		},
	}
}

func goodPaper() *PaperStats {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	return &PaperStats{
		StartedAt:  start,
		Now:        start.Add(10 * 24 * time.Hour),
		TradeCount: 25,
	}
}

// ---------- 非法跳转必须全部被拒绝 ----------

// 这是状态机存在的根本理由：任何跳级都不允许，尤其是 DRAFT 直达 LIVE。
func TestAllIllegalTransitionsAreRejected(t *testing.T) {
	all := []types.StrategyState{
		types.StateDraft, types.StateBacktested, types.StatePaperTrading,
		types.StateLiveEligible, types.StateLive, types.StateSuspended,
	}

	for _, from := range all {
		for _, to := range all {
			if from == to || isAllowed(from, to) {
				continue
			}
			t.Run(string(from)+"→"+string(to), func(t *testing.T) {
				_, err := CheckTransition(TransitionRequest{
					From: from, To: to,
					Actor: ActorUser, ActorID: "user:1", Reason: "尝试非法跳转",
					Backtest: goodBacktest(), Paper: goodPaper(),
				}, DefaultGate())
				if err == nil {
					t.Fatalf("非法流转 %s → %s 竟然被允许了", from, to)
				}
			})
		}
	}
}

// 单独把最危险的一条拎出来，确保它永远有专门的测试守着。
func TestDraftCannotJumpToLive(t *testing.T) {
	_, err := CheckTransition(TransitionRequest{
		From: types.StateDraft, To: types.StateLive,
		Actor: ActorUser, ActorID: "user:1", Reason: "我很确定这个策略没问题",
		Backtest: goodBacktest(),
	}, DefaultGate())

	if err == nil {
		t.Fatal("DRAFT 绝不能直接跳到 LIVE")
	}
	var te *TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("期望 *TransitionError，得到 %T", err)
	}
}

func TestSuspendedCannotResumeDirectlyToLive(t *testing.T) {
	_, err := CheckTransition(TransitionRequest{
		From: types.StateSuspended, To: types.StateLive,
		Actor: ActorUser, ActorID: "user:1", Reason: "风控问题已排查",
	}, DefaultGate())
	if err == nil {
		t.Fatal("暂停后必须重新走模拟盘验证，不能直接恢复实盘")
	}
}

// ---------- 合法路径 ----------

func TestHappyPathEndToEnd(t *testing.T) {
	gate := DefaultGate()
	steps := []struct {
		from, to types.StrategyState
		actor    Actor
		req      func(*TransitionRequest)
	}{
		{types.StateDraft, types.StateBacktested, ActorSystem,
			func(r *TransitionRequest) { r.Backtest = goodBacktest() }},
		{types.StateBacktested, types.StatePaperTrading, ActorSystem, nil},
		{types.StatePaperTrading, types.StateLiveEligible, ActorSystem,
			func(r *TransitionRequest) { r.Paper = goodPaper() }},
		{types.StateLiveEligible, types.StateLive, ActorUser, nil},
	}

	for _, s := range steps {
		t.Run(string(s.from)+"→"+string(s.to), func(t *testing.T) {
			req := TransitionRequest{
				From: s.from, To: s.to, Actor: s.actor,
				ActorID: "user:1", Reason: "正常推进",
			}
			if s.req != nil {
				s.req(&req)
			}
			ev, err := CheckTransition(req, gate)
			if err != nil {
				t.Fatalf("合法流转被拒绝：%v", err)
			}
			if len(ev) == 0 {
				t.Error("每次流转都要留下依据快照，供事后审计")
			}
		})
	}
}

// ---------- 实盘必须人工解锁 ----------

func TestLiveRequiresManualUserAction(t *testing.T) {
	_, err := CheckTransition(TransitionRequest{
		From: types.StateLiveEligible, To: types.StateLive,
		Actor: ActorSystem, ActorID: "system:scheduler",
		Reason: "模拟盘表现达标，自动上线",
	}, DefaultGate())

	if !errors.Is(err, ErrManualOnly) {
		t.Fatalf("系统不得自动推进到实盘，期望 ErrManualOnly，得到：%v", err)
	}
}

func TestLiveUnlockRecordsWhoDidIt(t *testing.T) {
	_, err := CheckTransition(TransitionRequest{
		From: types.StateLiveEligible, To: types.StateLive,
		Actor: ActorUser, ActorID: "", Reason: "解锁实盘",
	}, DefaultGate())
	if err == nil {
		t.Fatal("手动解锁实盘必须记录操作者身份")
	}

	ev, err := CheckTransition(TransitionRequest{
		From: types.StateLiveEligible, To: types.StateLive,
		Actor: ActorUser, ActorID: "user:alice", Reason: "确认上线",
	}, DefaultGate())
	if err != nil {
		t.Fatal(err)
	}
	if ev["unlocked_by"] != "user:alice" {
		t.Errorf("审计依据里没有记录操作者，实际：%v", ev)
	}
}

// ---------- 门槛逐项拆解（供界面展示"距离下一步还差什么"用） ----------

// EvaluateBacktestGate/EvaluatePaperGate 是 checkBacktestGate/checkPaperGate
// 真正用来做判断的同一份计算，这里直接验证它们逐项给出的 Pass 值，
// 跟 CheckTransition 整体通过/拒绝的结论必须一致——否则界面会显示"全部达标"
// 但提交时却被拒绝，或反过来。
func TestEvaluateBacktestGateMatchesCheckTransition(t *testing.T) {
	bt := goodBacktest()
	criteria := EvaluateBacktestGate(bt, DefaultGate())
	if len(criteria) == 0 {
		t.Fatal("达标的回测应该至少有几条检查项")
	}
	allPass := true
	for _, c := range criteria {
		if !c.Pass {
			allPass = false
		}
	}
	_, err := CheckTransition(TransitionRequest{
		From: types.StateDraft, To: types.StateBacktested,
		Actor: ActorSystem, Reason: "回测完成", Backtest: bt,
	}, DefaultGate())
	if allPass != (err == nil) {
		t.Errorf("EvaluateBacktestGate 逐项都通过=%v，但 CheckTransition 的结论是 err=%v，两者应该一致", allPass, err)
	}
}

func TestEvaluatePaperGateMatchesCheckTransition(t *testing.T) {
	p := goodPaper()
	criteria := EvaluatePaperGate(p, DefaultGate())
	if len(criteria) == 0 {
		t.Fatal("达标的模拟盘应该至少有几条检查项")
	}
	allPass := true
	for _, c := range criteria {
		if !c.Pass {
			allPass = false
		}
	}
	_, err := CheckTransition(TransitionRequest{
		From: types.StatePaperTrading, To: types.StateLiveEligible,
		Actor: ActorSystem, Reason: "模拟盘评估", Paper: p,
	}, DefaultGate())
	if allPass != (err == nil) {
		t.Errorf("EvaluatePaperGate 逐项都通过=%v，但 CheckTransition 的结论是 err=%v，两者应该一致", allPass, err)
	}
}

func TestEvaluateBacktestGateFlagsFailingCriterionOnly(t *testing.T) {
	bt := goodBacktest()
	bt.OutOfSample.SharpeRatio = -0.5 // 只让夏普不达标，其它照旧
	criteria := EvaluateBacktestGate(bt, DefaultGate())

	var sharpe *GateCriterion
	for i := range criteria {
		if criteria[i].Label == "样本外夏普比率" {
			sharpe = &criteria[i]
		} else if !criteria[i].Pass {
			t.Errorf("只有夏普不达标，但 %q 也被标记成未达标", criteria[i].Label)
		}
	}
	if sharpe == nil {
		t.Fatal("缺少夏普比率这一项检查")
	}
	if sharpe.Pass {
		t.Error("夏普为负，这一项应该标记为未达标")
	}
}

// ---------- 回测门槛 ----------

func TestBacktestGate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*types.BacktestResult)
		wantErr string
	}{
		{"样本外夏普为负", func(b *types.BacktestResult) {
			b.OutOfSample.SharpeRatio = -0.5
		}, "夏普"},
		{"样本外夏普刚好等于门槛（不算通过）", func(b *types.BacktestResult) {
			b.OutOfSample.SharpeRatio = 0
		}, "夏普"},
		{"样本外交易太少", func(b *types.BacktestResult) {
			b.OutOfSample.TradeCount = 2
		}, "笔交易"},
		{"样本外回撤过大", func(b *types.BacktestResult) {
			b.OutOfSample.MaxDrawdown = 0.8
		}, "回撤"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bt := goodBacktest()
			tc.mutate(bt)
			_, err := CheckTransition(TransitionRequest{
				From: types.StateDraft, To: types.StateBacktested,
				Actor: ActorSystem, Reason: "回测完成", Backtest: bt,
			}, DefaultGate())
			if err == nil {
				t.Fatal("未达标的回测不应放行")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("错误信息未说明原因 %q，实际：%v", tc.wantErr, err)
			}
		})
	}
}

// 手续费占毛利润的比例是独立于夏普/回撤之外的另一道检查——一个换手率很高、
// 靠薄价差反复进出的策略，净指标可能刚好卡过门槛，但对手续费/滑点的变动极度敏感。
func TestBacktestGateRejectsHighFeeDrag(t *testing.T) {
	bt := goodBacktest()
	// 本金 10000 涨到 11000（净利润 1000），但手续费花了 1500：
	// 毛利润 = 1000 + 1500 = 2500，占比 = 1500/2500 = 60%，超过默认门槛 50%。
	bt.OutOfSample.TotalReturn = 0.1
	bt.OutOfSample.FinalEquity = decimal.NewFromInt(11000)
	bt.OutOfSample.TotalFees = decimal.NewFromInt(1500)

	_, err := CheckTransition(TransitionRequest{
		From: types.StateDraft, To: types.StateBacktested,
		Actor: ActorSystem, Reason: "回测完成", Backtest: bt,
	}, DefaultGate())
	if err == nil {
		t.Fatal("手续费吃掉六成毛利润，不应该放行")
	}
	if !strings.Contains(err.Error(), "手续费") {
		t.Errorf("错误信息未说明是手续费占比超标，实际：%v", err)
	}
}

func TestBacktestGateAllowsLowFeeDrag(t *testing.T) {
	bt := goodBacktest()
	// 同样净利润 1000，但手续费只花了 800：毛利润 1800，占比约 44%，低于门槛。
	bt.OutOfSample.TotalReturn = 0.1
	bt.OutOfSample.FinalEquity = decimal.NewFromInt(11000)
	bt.OutOfSample.TotalFees = decimal.NewFromInt(800)

	_, err := CheckTransition(TransitionRequest{
		From: types.StateDraft, To: types.StateBacktested,
		Actor: ActorSystem, Reason: "回测完成", Backtest: bt,
	}, DefaultGate())
	if err != nil {
		t.Fatalf("手续费占比未超门槛，不应该被拒绝：%v", err)
	}
}

func TestBacktestGateSkipsFeeDragCheckWhenDisabled(t *testing.T) {
	bt := goodBacktest()
	bt.OutOfSample.TotalReturn = 0.1
	bt.OutOfSample.FinalEquity = decimal.NewFromInt(11000)
	bt.OutOfSample.TotalFees = decimal.NewFromInt(1500) // 60%，若启用检查会被拒绝

	gate := DefaultGate()
	gate.MaxFeeDragRatio = 0 // 显式关闭

	_, err := CheckTransition(TransitionRequest{
		From: types.StateDraft, To: types.StateBacktested,
		Actor: ActorSystem, Reason: "回测完成", Backtest: bt,
	}, gate)
	if err != nil {
		t.Fatalf("MaxFeeDragRatio=0 表示不启用这条检查，不应该被拒绝：%v", err)
	}
}

// 漂亮的样本内指标不能替代样本外验证——这正是过拟合最常见的伪装。
func TestGoodInSampleCannotSubstituteForOutOfSample(t *testing.T) {
	bt := goodBacktest()
	bt.InSample = types.PerformanceMetrics{SharpeRatio: 5.0, TradeCount: 200, TotalReturn: 3.0}
	bt.OutOfSample = types.PerformanceMetrics{SharpeRatio: -1.0, TradeCount: 20}

	_, err := CheckTransition(TransitionRequest{
		From: types.StateDraft, To: types.StateBacktested,
		Actor: ActorSystem, Reason: "回测完成", Backtest: bt,
	}, DefaultGate())
	if err == nil {
		t.Fatal("样本内再漂亮，样本外不达标也必须拒绝")
	}
}

func TestBacktestGateRequiresBacktestResult(t *testing.T) {
	_, err := CheckTransition(TransitionRequest{
		From: types.StateDraft, To: types.StateBacktested,
		Actor: ActorSystem, Reason: "跳过回测",
	}, DefaultGate())
	if err == nil {
		t.Fatal("没有回测结果不能推进到 BACKTESTED")
	}
}

// ---------- 模拟盘门槛 ----------

func TestPaperGate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*PaperStats)
		wantErr string
	}{
		{"时长不足", func(p *PaperStats) {
			p.Now = p.StartedAt.Add(2 * 24 * time.Hour)
		}, "只运行了"},
		{"笔数不足", func(p *PaperStats) {
			p.TradeCount = 3
		}, "只成交了"},
		{"跑够时间但一笔没成交", func(p *PaperStats) {
			p.TradeCount = 0
		}, "只成交了"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := goodPaper()
			tc.mutate(p)
			_, err := CheckTransition(TransitionRequest{
				From: types.StatePaperTrading, To: types.StateLiveEligible,
				Actor: ActorSystem, Reason: "模拟盘评估", Paper: p,
			}, DefaultGate())
			if err == nil {
				t.Fatal("未达标的模拟盘不应放行")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("错误信息未说明原因 %q，实际：%v", tc.wantErr, err)
			}
		})
	}
}

func TestPaperGateRequiresStats(t *testing.T) {
	_, err := CheckTransition(TransitionRequest{
		From: types.StatePaperTrading, To: types.StateLiveEligible,
		Actor: ActorSystem, Reason: "直接放行",
	}, DefaultGate())
	if err == nil {
		t.Fatal("没有模拟盘统计不能推进到 LIVE_ELIGIBLE")
	}
}

// ---------- 审计要求 ----------

func TestEveryTransitionRequiresReason(t *testing.T) {
	_, err := CheckTransition(TransitionRequest{
		From: types.StateBacktested, To: types.StatePaperTrading,
		Actor: ActorUser, ActorID: "user:1", Reason: "",
	}, DefaultGate())
	if err == nil {
		t.Fatal("空理由的流转必须被拒绝，审计日志不能有空白")
	}
}

func TestEvidenceCapturesGateValues(t *testing.T) {
	ev, err := CheckTransition(TransitionRequest{
		From: types.StateDraft, To: types.StateBacktested,
		Actor: ActorSystem, Reason: "回测通过", Backtest: goodBacktest(),
	}, DefaultGate())
	if err != nil {
		t.Fatal(err)
	}
	// 依据里既要有实际值，也要有当时的门槛值——
	// 否则日后门槛调整了，就无法判断当初为什么放行。
	for _, key := range []string{
		"out_of_sample_sharpe", "out_of_sample_trades",
		"gate_min_sharpe", "gate_min_trades", "engine_version",
	} {
		if _, ok := ev[key]; !ok {
			t.Errorf("审计依据缺少字段 %q", key)
		}
	}
}

// ---------- 下单许可 ----------

func TestOnlyLiveStateCanPlaceRealOrders(t *testing.T) {
	for _, s := range types.AllStates {
		want := s == types.StateLive
		if got := CanTradeLive(s); got != want {
			t.Errorf("CanTradeLive(%s) = %v，期望 %v", s, got, want)
		}
	}
}

func TestPaperTradingStates(t *testing.T) {
	if !CanTradePaper(types.StatePaperTrading) {
		t.Error("PAPER_TRADING 应当允许模拟交易")
	}
	if !CanTradePaper(types.StateLiveEligible) {
		t.Error("LIVE_ELIGIBLE 在用户解锁前应继续跑模拟盘")
	}
	if CanTradePaper(types.StateDraft) {
		t.Error("DRAFT 不应产生任何交易")
	}
}

func TestNextStatesIsACopy(t *testing.T) {
	got := NextStates(types.StateDraft)
	if len(got) == 0 {
		t.Fatal("DRAFT 应当有合法后继")
	}
	got[0] = "篡改"
	if NextStates(types.StateDraft)[0] == "篡改" {
		t.Error("NextStates 返回的必须是副本，否则调用方能改坏白名单")
	}
}
