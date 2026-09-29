package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

func getPage(srv *Server, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	attachTestSession(srv, req)
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	return w
}

func draftStrategy(id string) types.StrategyConfig {
	return types.StrategyConfig{
		ID: id, UserID: testDefaultUserID, Name: "测试策略", Symbol: "BTCUSDT", Timeframe: types.TF1h,
		Combine: types.CombineAll, Risk: riskConfig(), State: types.StateDraft,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func TestHandleStrategyDetailDistinguishesInSampleAndOutOfSample(t *testing.T) {
	store := newFakeStore()
	store.strategies["aaaaaaaa-1111-4111-8111-111111111111"] = draftStrategy("aaaaaaaa-1111-4111-8111-111111111111")
	store.backtests["aaaaaaaa-1111-4111-8111-111111111111"] = types.BacktestResult{
		ID: "bt1", StrategyID: "aaaaaaaa-1111-4111-8111-111111111111", Symbol: "BTCUSDT",
		Overall:     types.PerformanceMetrics{TradeCount: 10, SharpeRatio: 1.1},
		InSample:    types.PerformanceMetrics{TradeCount: 7, SharpeRatio: 2.5},
		OutOfSample: types.PerformanceMetrics{TradeCount: 3, SharpeRatio: 0.4},
		Trades: []types.BacktestTrade{
			{EntryTime: time.Now().Add(-time.Hour), PnL: decimal.NewFromInt(10), Segment: "in_sample"},
			{EntryTime: time.Now(), PnL: decimal.NewFromInt(-4), Segment: "out_of_sample"},
		},
	}

	srv := newTestServer(t, store)
	w := getPage(srv, "/strategies/aaaaaaaa-1111-4111-8111-111111111111")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "样本外") || !strings.Contains(body, "唯一依据") {
		t.Errorf("应醒目标注样本外区块，实际：%s", body)
	}
	// 样本内夏普 2.50 与样本外夏普 0.40 都应各自出现，不能被合并成一份数字。
	if !strings.Contains(body, "2.50") || !strings.Contains(body, "0.40") {
		t.Errorf("样本内外的夏普应分别展示，实际：%s", body)
	}
}

func TestHandleStrategyDetailShowsZeroTradeOutOfSampleNote(t *testing.T) {
	store := newFakeStore()
	store.strategies["aaaaaaaa-1111-4111-8111-111111111111"] = draftStrategy("aaaaaaaa-1111-4111-8111-111111111111")
	store.backtests["aaaaaaaa-1111-4111-8111-111111111111"] = types.BacktestResult{
		ID: "bt1", StrategyID: "aaaaaaaa-1111-4111-8111-111111111111", Symbol: "BTCUSDT",
		OutOfSample: types.PerformanceMetrics{TradeCount: 0},
	}

	srv := newTestServer(t, store)
	w := getPage(srv, "/strategies/aaaaaaaa-1111-4111-8111-111111111111")
	body := w.Body.String()
	if !strings.Contains(body, "不代表已验证") {
		t.Errorf("样本外零交易应有事实性提示，实际：%s", body)
	}
	for _, bad := range []string{"建议", "推荐"} {
		if strings.Contains(body, bad) {
			t.Errorf("详情页文案不得包含投资建议措辞 %q，实际：%s", bad, body)
		}
	}
}

func TestHandleStrategyDetailHandlesNoBacktestYet(t *testing.T) {
	store := newFakeStore()
	store.strategies["aaaaaaaa-1111-4111-8111-111111111111"] = draftStrategy("aaaaaaaa-1111-4111-8111-111111111111")

	srv := newTestServer(t, store)
	w := getPage(srv, "/strategies/aaaaaaaa-1111-4111-8111-111111111111")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "尚无回测结果") {
		t.Errorf("没有回测结果时应有提示，实际：%s", w.Body.String())
	}
}

func TestHandleStrategyDetailOmitsPaperStatsWhenNeverEnteredPaperTrading(t *testing.T) {
	store := newFakeStore()
	store.strategies["aaaaaaaa-1111-4111-8111-111111111111"] = draftStrategy("aaaaaaaa-1111-4111-8111-111111111111")

	srv := newTestServer(t, store)
	w := getPage(srv, "/strategies/aaaaaaaa-1111-4111-8111-111111111111")
	if strings.Contains(w.Body.String(), "模拟盘运行统计") {
		t.Errorf("从未进入模拟盘时不应展示模拟盘统计区块，实际：%s", w.Body.String())
	}
}

func TestHandleStrategyDetailShowsPaperStatsWhenAvailable(t *testing.T) {
	store := newFakeStore()
	store.strategies["aaaaaaaa-1111-4111-8111-111111111111"] = draftStrategy("aaaaaaaa-1111-4111-8111-111111111111")
	store.paperStats["aaaaaaaa-1111-4111-8111-111111111111"] = strategy.PaperStats{
		StartedAt: time.Now().Add(-48 * time.Hour), Now: time.Now(), TradeCount: 12,
	}

	srv := newTestServer(t, store)
	w := getPage(srv, "/strategies/aaaaaaaa-1111-4111-8111-111111111111")
	body := w.Body.String()
	if !strings.Contains(body, "模拟盘运行统计") || !strings.Contains(body, "12") {
		t.Errorf("应展示模拟盘统计，实际：%s", body)
	}
}

func TestHandleStrategyDetailUnknownIDReturns404(t *testing.T) {
	srv := newTestServer(t, newFakeStore())
	// 合法 UUID 形状但库里没有这一条：走 Store.GetStrategy 返回 ErrNotFound 的路径。
	w := getPage(srv, "/strategies/aaaaaaaa-1111-4111-8111-111111111111")
	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", w.Code)
	}
}

// 真实 Postgres 里 strategies.id 是 UUID 列：格式不对的字符串会让驱动直接报
// "invalid input syntax for type uuid"，这个错误不是 ErrNotFound，如果不提前拦截，
// 会被 serverError 当成 500 返回，而不是最普通的 404。fakeStore 不会重现这个
// Postgres 特有的报错，所以这条必须在 looksLikeUUID 那层单独覆盖。
func TestHandleStrategyDetailMalformedIDReturns404NotServerError(t *testing.T) {
	srv := newTestServer(t, newFakeStore())
	w := getPage(srv, "/strategies/does-not-exist")
	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404（格式不对的 ID 不该冒充成 500）", w.Code)
	}
}

func TestHandleStrategyDetailShowsOrderProvenanceDetail(t *testing.T) {
	const id = "dddddddd-4444-4444-8444-444444444444"
	store := newFakeStore()
	store.strategies[id] = draftStrategy(id)
	store.orders[id] = []types.Order{
		{
			ID: "o1", StrategyID: id, Symbol: "BTCUSDT", Side: types.SideBuy, Type: types.OrderMarket,
			Mode: types.ModePaper, Quantity: decimal.NewFromInt(1), FilledPrice: decimal.NewFromInt(50000),
			Status: types.OrderFilled, CreatedAt: time.Now(),
			Provenance: types.OrderProvenance{
				DecisionID: "dec1", Combine: types.CombineAll, Score: 0.8,
				Signals: []types.Signal{
					{Module: "support_resistance", Direction: types.DirectionLong, Confidence: 0.9, Reason: types.Message{Literal: "价格突破阻力位"}},
					{Module: "volume_breakout", Direction: types.DirectionLong, Confidence: 0.7, Reason: types.Message{Literal: "成交量放大"}, Degraded: false},
				},
				ModuleParams: map[string]map[string]any{
					"support_resistance": {"lookback": 20, "tolerance": 0.01},
					"volume_breakout":    {"threshold": 2.5},
				},
			},
		},
	}

	srv := newTestServer(t, store)
	body := getPage(srv, "/strategies/"+id).Body.String()

	for _, want := range []string{
		"support_resistance", "价格突破阻力位", "lookback=20",
		"volume_breakout", "成交量放大", "threshold=2.5",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("订单触发依据应包含 %q，实际：%s", want, body)
		}
	}
}

func TestHandleStrategyDetailMarksDegradedSignalInProvenance(t *testing.T) {
	const id = "eeeeeeee-5555-4555-8555-555555555555"
	store := newFakeStore()
	store.strategies[id] = draftStrategy(id)
	store.orders[id] = []types.Order{
		{
			ID: "o1", StrategyID: id, Symbol: "BTCUSDT", Side: types.SideBuy, Type: types.OrderMarket,
			Mode: types.ModePaper, Quantity: decimal.NewFromInt(1), Status: types.OrderFilled, CreatedAt: time.Now(),
			Provenance: types.OrderProvenance{
				Combine: types.CombineAll, Score: 0.5,
				Signals: []types.Signal{
					{Module: "cvd_orderflow", Direction: types.DirectionNeutral, Degraded: true, Err: "模块超时"},
				},
			},
		},
	}

	srv := newTestServer(t, store)
	body := getPage(srv, "/strategies/"+id).Body.String()
	if !strings.Contains(body, "降级") || !strings.Contains(body, "模块超时") {
		t.Errorf("降级信号应在触发依据里标出原因，实际：%s", body)
	}
}

func TestHandleStrategyDetailShowsDecisionSignalBreakdown(t *testing.T) {
	const id = "ffffffff-6666-4666-8666-666666666666"
	store := newFakeStore()
	store.strategies[id] = draftStrategy(id)
	store.decisions[id] = []types.Decision{
		{
			ID: "dec1", StrategyID: id, Symbol: "BTCUSDT", Direction: types.DirectionLong,
			Score: 0.8, Triggered: true, Reason: "所有模块一致看多", Timestamp: time.Now(),
			Signals: []types.Signal{
				{Module: "macd_rsi", Direction: types.DirectionLong, Confidence: 0.8, Reason: types.Message{Literal: "MACD 金叉"}},
			},
		},
	}

	srv := newTestServer(t, store)
	body := getPage(srv, "/strategies/"+id).Body.String()
	for _, want := range []string{"macd_rsi", "MACD 金叉"} {
		if !strings.Contains(body, want) {
			t.Errorf("决策记录应展开各模块信号，实际应包含 %q，响应：%s", want, body)
		}
	}
}

func TestHandleStrategyDetailShowsUnlockFormOnlyWhenEligible(t *testing.T) {
	const draftID = "bbbbbbbb-2222-4222-8222-222222222222"
	const eligibleID = "cccccccc-3333-4333-8333-333333333333"
	store := newFakeStore()
	store.strategies[draftID] = draftStrategy(draftID)
	eligible := draftStrategy(eligibleID)
	eligible.State = types.StateLiveEligible
	store.strategies[eligibleID] = eligible

	srv := newTestServer(t, store)

	if body := getPage(srv, "/strategies/"+draftID).Body.String(); strings.Contains(body, "解锁实盘") {
		t.Errorf("DRAFT 状态不应展示解锁实盘表单，实际：%s", body)
	}
	if body := getPage(srv, "/strategies/"+eligibleID).Body.String(); !strings.Contains(body, "解锁实盘") {
		t.Errorf("LIVE_ELIGIBLE 状态应展示解锁实盘表单，实际：%s", body)
	}
}
