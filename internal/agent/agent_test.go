package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"tradeforge/internal/i18n"
	"tradeforge/internal/modules"
	"tradeforge/pkg/types"
)

func newAgent(responses ...string) (*Agent, *StubLLM) {
	stub := &StubLLM{Responses: responses}
	return New(stub, modules.NewDefaultRegistry(), 2), stub
}

// reply builds one piece of mock model-output JSON text.
func reply(outcome Outcome, restatement string, cfg map[string]any, questions ...string) string {
	if questions == nil {
		questions = []string{}
	}
	return MustJSON(map[string]any{
		"outcome":     string(outcome),
		"restatement": restatement,
		"questions":   questions,
		"config":      cfg,
	})
}

func risk(pos string) map[string]any {
	return map[string]any{"max_position_size_quote": pos}
}

// ---------- 1. Normal translation: natural language -> expected config shape ----------

// These cases are "one sentence -> StrategyConfig" contract tests.
// They assert on the translation layer's validation and conversion behavior; model output is supplied by a stub.
func TestTranslationPairs(t *testing.T) {
	cases := []struct {
		name      string
		utterance string
		modelJSON string
		want      func(*testing.T, types.StrategyConfig)
	}{
		{
			name:      "1. single module + explicit params",
			utterance: "BTC 一小时线，成交量放大到均量 3 倍时做多",
			modelJSON: reply(OutcomeConfig, "标的 BTCUSDT，1 小时周期，当成交量达到近期均量 3 倍时触发。", map[string]any{
				"name": "BTC 放量策略", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{map[string]any{
					"module": "volume_breakout",
					"params": map[string]any{"multiplier": 3.0},
				}},
				"risk": risk("1000"),
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if c.Symbol != "BTCUSDT" || c.Timeframe != types.TF1h {
					t.Errorf("symbol/timeframe = %s/%s", c.Symbol, c.Timeframe)
				}
				if len(c.Modules) != 1 || c.Modules[0].Module != "volume_breakout" {
					t.Fatalf("modules = %+v", c.Modules)
				}
				if c.Modules[0].Params["multiplier"] != 3.0 {
					t.Errorf("multiplier = %v, want 3.0", c.Modules[0].Params["multiplier"])
				}
				// A param the user never mentioned must be absent, with the system filling in the default, not the model inventing a value.
				if _, ok := c.Modules[0].Params["window"]; ok {
					t.Error("window, which the user never mentioned, should not appear in the config")
				}
			},
		},
		{
			name:      "2. multiple modules, ALL combine",
			utterance: "以太坊 4 小时，突破阻力位并且同时放量 2 倍才做多",
			modelJSON: reply(OutcomeConfig, "标的 ETHUSDT，4 小时周期，需同时满足突破关键位与放量 2 倍。", map[string]any{
				"name": "ETH 突破放量", "symbol": "ETHUSDT", "timeframe": "4h", "combine": "ALL",
				"modules": []any{
					map[string]any{"module": "support_resistance", "params": map[string]any{}},
					map[string]any{"module": "volume_breakout", "params": map[string]any{"multiplier": 2.0}},
				},
				"risk": risk("500"),
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if c.Combine != types.CombineAll {
					t.Errorf("combine = %s, want ALL", c.Combine)
				}
				if len(c.Modules) != 2 {
					t.Errorf("module count = %d, want 2", len(c.Modules))
				}
			},
		},
		{
			name:      "3. WEIGHTED combine with weights and threshold",
			utterance: "BTC 一小时，支撑阻力占七成、CVD 占三成，综合分超过 0.6 就开仓",
			modelJSON: reply(OutcomeConfig, "标的 BTCUSDT，1 小时，支撑阻力权重 0.7、CVD 权重 0.3，加权分超过 0.6 触发。", map[string]any{
				"name": "BTC 加权组合", "symbol": "BTCUSDT", "timeframe": "1h",
				"combine": "WEIGHTED", "threshold": 0.6,
				"modules": []any{
					map[string]any{"module": "support_resistance", "weight": 0.7, "params": map[string]any{}},
					map[string]any{"module": "cvd_orderflow", "weight": 0.3, "params": map[string]any{}},
				},
				"risk": risk("2000"),
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if c.Combine != types.CombineWeighted || c.Threshold != 0.6 {
					t.Errorf("combine/threshold = %s/%v", c.Combine, c.Threshold)
				}
				if c.Modules[0].Weight != 0.7 || c.Modules[1].Weight != 0.3 {
					t.Errorf("weights = %v / %v", c.Modules[0].Weight, c.Modules[1].Weight)
				}
			},
		},
		{
			name:      "4. full risk control params",
			utterance: "BTC 15 分钟放量 2.5 倍做多，单笔最多 800U，单日亏 200U 就停，止损 2%，最多持仓 4 小时",
			modelJSON: reply(OutcomeConfig, "标的 BTCUSDT，15 分钟，放量 2.5 倍触发；单笔 800、日亏上限 200、止损 2%、最长持仓 4 小时。", map[string]any{
				"name": "BTC 短线放量", "symbol": "BTCUSDT", "timeframe": "15m", "combine": "ALL",
				"modules": []any{map[string]any{
					"module": "volume_breakout", "params": map[string]any{"multiplier": 2.5},
				}},
				"risk": map[string]any{
					"max_position_size_quote": "800",
					"max_daily_loss_quote":    "200",
					"stop_loss_pct":           0.02,
					"max_holding_period":      "4h",
				},
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if c.Risk.MaxPositionSizeQuote.String() != "800" {
					t.Errorf("max position size = %s", c.Risk.MaxPositionSizeQuote)
				}
				if c.Risk.MaxDailyLossQuote.String() != "200" {
					t.Errorf("max daily loss = %s", c.Risk.MaxDailyLossQuote)
				}
				if c.Risk.MaxHoldingPeriod.Std().Hours() != 4 {
					t.Errorf("max holding period = %s", c.Risk.MaxHoldingPeriod)
				}
				if c.Risk.StopLossPct != 0.02 {
					t.Errorf("stop loss = %v", c.Risk.StopLossPct)
				}
			},
		},
		{
			name:      "5. CVD imbalance module with enum param",
			utterance: "BTC 一小时，主动买盘占比超过 40% 时做多",
			modelJSON: reply(OutcomeConfig, "标的 BTCUSDT，1 小时，CVD 失衡度超过 0.4 时触发。", map[string]any{
				"name": "BTC 订单流失衡", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{map[string]any{
					"module": "cvd_orderflow",
					"params": map[string]any{"imbalance_threshold": 0.4, "detect": "imbalance"},
				}},
				"risk": risk("1000"),
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				p := c.Modules[0].Params
				if p["imbalance_threshold"] != 0.4 || p["detect"] != "imbalance" {
					t.Errorf("params = %+v", p)
				}
			},
		},
		{
			name:      "6. symbol name normalized to exchange symbol",
			utterance: "比特币日线，跌破支撑就平仓",
			modelJSON: reply(OutcomeConfig, "标的 BTCUSDT，日线，跌破关键支撑位时触发。", map[string]any{
				"name": "BTC 日线支撑", "symbol": "btcusdt", "timeframe": "1d", "combine": "ALL",
				"modules": []any{map[string]any{"module": "support_resistance", "params": map[string]any{}}},
				"risk":    risk("1000"),
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if c.Symbol != "BTCUSDT" {
					t.Errorf("symbol = %q, want normalized uppercase BTCUSDT", c.Symbol)
				}
			},
		},
		{
			name:      "7. support_resistance fine-grained params",
			utterance: "BTC 4 小时，回看 300 根 K 线，至少被摸过 3 次的位才算有效",
			modelJSON: reply(OutcomeConfig, "标的 BTCUSDT，4 小时，回看窗口 300 根，关键位至少触及 3 次。", map[string]any{
				"name": "BTC 关键位", "symbol": "BTCUSDT", "timeframe": "4h", "combine": "ALL",
				"modules": []any{map[string]any{
					"module": "support_resistance",
					"params": map[string]any{"lookback": 300, "min_touches": 3},
				}},
				"risk": risk("1500"),
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				p := c.Modules[0].Params
				// JSON numbers decode as float64; ResolveParams normalizes them when handing off to the engine.
				if p["lookback"] != float64(300) || p["min_touches"] != float64(3) {
					t.Errorf("params = %+v", p)
				}
			},
		},
		{
			name:      "8. three-module full combine",
			utterance: "BTC 一小时，突破关键位、放量、且订单流同向，三个条件都满足才做",
			modelJSON: reply(OutcomeConfig, "标的 BTCUSDT，1 小时，需同时满足关键位突破、放量与订单流同向。", map[string]any{
				"name": "BTC 三重确认", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{
					map[string]any{"module": "support_resistance", "params": map[string]any{}},
					map[string]any{"module": "volume_breakout", "params": map[string]any{}},
					map[string]any{"module": "cvd_orderflow", "params": map[string]any{}},
				},
				"risk": risk("3000"),
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if len(c.Modules) != 3 {
					t.Errorf("module count = %d, want 3", len(c.Modules))
				}
			},
		},
		{
			name:      "9. amounts carried as strings, no precision loss",
			utterance: "BTC 一小时放量做多，单笔 1234.56789 USDT",
			modelJSON: reply(OutcomeConfig, "标的 BTCUSDT，1 小时，放量触发，单笔仓位 1234.56789。", map[string]any{
				"name": "BTC 精度测试", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
				"risk":    risk("1234.56789"),
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if got := c.Risk.MaxPositionSizeQuote.String(); got != "1234.56789" {
					t.Errorf("amount = %s, want 1234.56789 (must not pass through float64)", got)
				}
			},
		},
		{
			name:      "10. ETH config is independent, unrelated to BTC",
			utterance: "ETH 5 分钟，CVD 出现背离就反手",
			modelJSON: reply(OutcomeConfig, "标的 ETHUSDT，5 分钟，检测 CVD 与价格背离。", map[string]any{
				"name": "ETH 背离", "symbol": "ETHUSDT", "timeframe": "5m", "combine": "ALL",
				"modules": []any{map[string]any{
					"module": "cvd_orderflow", "params": map[string]any{"detect": "divergence"},
				}},
				"risk": risk("300"),
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if c.Symbol != "ETHUSDT" || c.Timeframe != types.TF5m {
					t.Errorf("symbol/timeframe = %s/%s", c.Symbol, c.Timeframe)
				}
			},
		},
		{
			name:      "11. stop loss/take profit described via support/resistance, not a fixed percentage",
			utterance: "BTC 一小时线，放量突破做多，跌破支撑位就止损，到阻力位就止盈",
			modelJSON: reply(OutcomeConfig, "标的 BTCUSDT，1 小时周期，放量突破时做多；止损设在开仓时最近的支撑位，止盈设在最近的阻力位。", map[string]any{
				"name": "支撑阻力位止损止盈", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{
					map[string]any{"module": "volume_breakout", "params": map[string]any{}},
					map[string]any{"module": "support_resistance", "params": map[string]any{}},
				},
				"risk": map[string]any{
					"max_position_size_quote": "1000",
					"stop_loss_mode":          "support_resistance",
					"take_profit_mode":        "support_resistance",
				},
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if c.Risk.StopLossMode != types.RiskLevelModeSupportResistance {
					t.Errorf("StopLossMode = %q, want support_resistance", c.Risk.StopLossMode)
				}
				if c.Risk.TakeProfitMode != types.RiskLevelModeSupportResistance {
					t.Errorf("TakeProfitMode = %q, want support_resistance", c.Risk.TakeProfitMode)
				}
				if c.Risk.StopLossPct != 0 || c.Risk.TakeProfitPct != 0 {
					t.Errorf("support_resistance mode should not also carry a percentage: sl=%v tp=%v",
						c.Risk.StopLossPct, c.Risk.TakeProfitPct)
				}
				hasSR := false
				for _, m := range c.Modules {
					if m.Module == "support_resistance" {
						hasSR = true
					}
				}
				if !hasSR {
					t.Error("support_resistance mode requires the support_resistance module to also be in the modules list")
				}
			},
		},
		{
			name:      "12. stop loss described via POC (volume point of control)",
			utterance: "ETH 一小时线，放量突破做多，收回成交量分布重心（POC）就止损",
			modelJSON: reply(OutcomeConfig, "标的 ETHUSDT，1 小时周期，放量突破时做多；止损设在开仓时的成交量分布重心（POC）。", map[string]any{
				"name": "POC 止损", "symbol": "ETHUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{
					map[string]any{"module": "volume_breakout", "params": map[string]any{}},
					map[string]any{"module": "poc", "params": map[string]any{}},
				},
				"risk": map[string]any{
					"max_position_size_quote": "1000",
					"stop_loss_mode":          "poc",
				},
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if c.Risk.StopLossMode != types.RiskLevelModePOC {
					t.Errorf("StopLossMode = %q, want poc", c.Risk.StopLossMode)
				}
				if c.Risk.StopLossPct != 0 {
					t.Errorf("poc mode should not also carry a percentage: sl=%v", c.Risk.StopLossPct)
				}
				hasPOC := false
				for _, m := range c.Modules {
					if m.Module == "poc" {
						hasPOC = true
					}
				}
				if !hasPOC {
					t.Error("poc mode requires the poc module to also be in the modules list")
				}
			},
		},
		{
			name: "13. multi-timeframe: 1h judges a range fakeout, 15m judges a volume-driven drop that triggers",
			utterance: "BTC，1 小时级别观察高位盘整区，等前高假突破以后，观察 15 分钟级别是否走出" +
				"超过均量的下跌，出现就入场做空；如果 1 小时 K 线收回 POC 就止损",
			modelJSON: reply(OutcomeConfig,
				"标的 BTCUSDT；1 小时周期判断盘整区假突破（背景条件）与 POC（止损参考），"+
					"15 分钟周期判断放量下跌并作为实际入场触发，整条策略的触发节奏是 15 分钟。"+
					"提醒：当前组合方式下，poc 模块自己的方向信号也会参与是否触发入场的判断，"+
					"可能让触发条件比预想更严格。",
				map[string]any{
					"name": "1h 假突破 + 15m 放量下跌", "symbol": "BTCUSDT", "timeframe": "15m", "combine": "ALL",
					"modules": []any{
						map[string]any{"module": "fakeout", "timeframe": "1h", "params": map[string]any{}},
						map[string]any{"module": "volume_breakout", "params": map[string]any{}},
						map[string]any{"module": "poc", "timeframe": "1h", "params": map[string]any{}},
					},
					"risk": map[string]any{
						"max_position_size_quote": "1000",
						"stop_loss_mode":          "poc",
					},
				}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if c.Timeframe != types.TF15m {
					t.Errorf("top-level timeframe = %s, want 15m (the fastest one, i.e. the trigger timeframe)", c.Timeframe)
				}
				byName := map[string]types.ModuleConfig{}
				for _, m := range c.Modules {
					byName[m.Module] = m
				}
				if byName["fakeout"].Timeframe != types.TF1h {
					t.Errorf("fakeout.timeframe = %q, want 1h", byName["fakeout"].Timeframe)
				}
				if byName["poc"].Timeframe != types.TF1h {
					t.Errorf("poc.timeframe = %q, want 1h", byName["poc"].Timeframe)
				}
				if tf := byName["volume_breakout"].Timeframe; tf != "" && tf != types.TF15m {
					t.Errorf("volume_breakout.timeframe = %q, want empty or 15m (following the trigger timeframe)", tf)
				}
				if c.Risk.StopLossMode != types.RiskLevelModePOC {
					t.Errorf("StopLossMode = %q, want poc", c.Risk.StopLossMode)
				}
			},
		},
		{
			name:      "14. size the position by account risk percentage, not a fixed amount",
			utterance: "BTC 一小时线，放量突破做多，止损 2%；账户权益 1 万 USDT，每笔最多亏本金的 1%",
			modelJSON: reply(OutcomeConfig,
				"标的 BTCUSDT，1 小时周期，放量突破时做多，止损 2%；按风险百分比开仓——账户权益 "+
					"10000 USDT，单笔风险 1%，仓位由止损距离和风险比例算出；单笔最大仓位（硬上限）"+
					"用户没有额外说一个具体数字，默认设为账户权益本身 10000。",
				map[string]any{
					"name": "按风险百分比开仓", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
					"modules": []any{
						map[string]any{"module": "volume_breakout", "params": map[string]any{}},
					},
					"risk": map[string]any{
						"max_position_size_quote": "10000",
						"stop_loss_pct":           0.02,
						"position_sizing_mode":    "risk_pct",
						"account_equity_quote":    "10000",
						"risk_per_trade_pct":      0.01,
					},
				}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if c.Risk.PositionSizingMode != types.PositionSizingModeRiskPct {
					t.Errorf("PositionSizingMode = %q, want risk_pct", c.Risk.PositionSizingMode)
				}
				if got := c.Risk.AccountEquityQuote.String(); got != "10000" {
					t.Errorf("AccountEquityQuote = %s, want 10000", got)
				}
				if c.Risk.RiskPerTradePct != 0.01 {
					t.Errorf("RiskPerTradePct = %v, want 0.01", c.Risk.RiskPerTradePct)
				}
				if c.Risk.StopLossPct != 0.02 {
					t.Errorf("StopLossPct = %v, want 0.02", c.Risk.StopLossPct)
				}
				if got := c.Risk.MaxPositionSizeQuote.String(); got != "10000" {
					t.Errorf("MaxPositionSizeQuote = %s, want it to default to account equity 10000", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newAgent(tc.modelJSON)
			p, err := a.Translate(context.Background(), tc.utterance, nil, i18n.LangZH)
			if err != nil {
				t.Fatalf("translate failed: %v", err)
			}
			if p.NeedsClarification() {
				t.Fatalf("expected a config directly, got a clarification request instead: %v", p.Questions)
			}
			if p.Restatement == "" {
				t.Error("must have a restatement for the user to confirm")
			}
			if strings.HasPrefix(tc.name, "13.") &&
				!strings.Contains(p.Restatement, "参与") {
				t.Errorf("poc is only used to derive the stop-loss price, it isn't one of the entry conditions the user described; "+
					"the restatement must warn that its own directional signal also participates in the entry decision, got: %q", p.Restatement)
			}
			tc.want(t, *p.Config)
		})
	}
}

// ---------- 2. Ambiguity handling: must ask, never guess ----------

func TestAmbiguousInputsAskInsteadOfGuessing(t *testing.T) {
	cases := []struct {
		name      string
		utterance string
		modelJSON string
	}{
		{
			name:      "symbol not stated",
			utterance: "放量就买",
			modelJSON: reply(OutcomeClarify, "你想在成交量放大时开仓，但还缺少一些必要信息。", nil,
				"你想交易哪个标的？请给出交易对，例如 BTCUSDT。",
				"用哪个 K 线周期？可选 1m/5m/15m/1h/4h/1d。",
				"成交量要放大到均量的多少倍才触发？"),
		},
		{
			name:      "breakout direction unclear",
			utterance: "BTC 一小时，突破就操作",
			modelJSON: reply(OutcomeClarify, "标的 BTCUSDT、周期 1 小时已明确，但触发条件还需要确认。", nil,
				"这里的“突破”指向上突破阻力位，还是向下跌破支撑位？",
				"单笔最多投入多少 USDT？"),
		},
		{
			name:      "param outside the allowed range",
			utterance: "BTC 一小时，回看 5000 根 K 线找支撑位",
			modelJSON: reply(OutcomeClarify, "标的 BTCUSDT、1 小时周期已明确，回看窗口的取值需要确认。", nil,
				"回看窗口 5000 超出了平台允许的 30 ~ 1000 范围，你希望改用哪个数值？"),
		},
		{
			name:      "indicator not among the platform's supported modules",
			utterance: "BTC 一小时，MACD 金叉就买",
			modelJSON: reply(OutcomeClarify, "标的 BTCUSDT、1 小时周期已明确。", nil,
				"平台目前提供的模块为支撑阻力、成交量突破、CVD 订单流，暂不支持 MACD。你希望改用其中哪一个来表达这条规则？"),
		},
		{
			name:      "no risk control mentioned at all",
			utterance: "ETH 4 小时放量 2 倍做多",
			modelJSON: reply(OutcomeClarify, "标的 ETHUSDT，4 小时，放量 2 倍触发。", nil,
				"单笔最多投入多少 USDT？这是平台强制要求的风控项。"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newAgent(tc.modelJSON)
			p, err := a.Translate(context.Background(), tc.utterance, nil, i18n.LangZH)
			if err != nil {
				t.Fatalf("translate failed: %v", err)
			}
			if !p.NeedsClarification() {
				t.Fatalf("ambiguous input should trigger a question, got a config directly instead: %+v", p.Config)
			}
			if len(p.Questions) == 0 {
				t.Error("must give specific questions when requesting clarification")
			}
			if p.Config != nil {
				t.Error("must not also give a config when requesting clarification")
			}
		})
	}
}

// After clarification, the user supplies more info; the second round should produce a config, with history carried into the context.
func TestClarificationLoopContinuesWithHistory(t *testing.T) {
	first := reply(OutcomeClarify, "缺少标的信息。", nil, "你想交易哪个标的？")
	second := reply(OutcomeConfig, "标的 BTCUSDT，1 小时，放量 2 倍触发。", map[string]any{
		"name": "BTC 放量", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{"multiplier": 2.0}}},
		"risk":    risk("1000"),
	})

	a, stub := newAgent(first, second)

	p1, err := a.Translate(context.Background(), "放量 2 倍，一小时线", nil, i18n.LangZH)
	if err != nil {
		t.Fatal(err)
	}
	if !p1.NeedsClarification() {
		t.Fatal("the first round should ask a question")
	}

	history := []Turn{
		{Role: "user", Text: "放量 2 倍，一小时线"},
		{Role: "assistant", Text: strings.Join(p1.Questions, "\n")},
	}
	p2, err := a.Translate(context.Background(), "BTC", history, i18n.LangZH)
	if err != nil {
		t.Fatal(err)
	}
	if p2.NeedsClarification() {
		t.Fatalf("still asking questions after clarification was supplied: %v", p2.Questions)
	}
	if p2.Config.Symbol != "BTCUSDT" {
		t.Errorf("symbol = %s", p2.Config.Symbol)
	}
	// The second call must carry history, otherwise the model can't see the prior round's Q&A.
	if len(stub.Calls[1]) <= len(stub.Calls[0]) {
		t.Errorf("second call's turn count %d did not exceed the first call's %d, history was not carried",
			len(stub.Calls[1]), len(stub.Calls[0]))
	}
}

// ---------- 3. Reject invalid output, no "best-effort repair" ----------

func TestRejectsInvalidModelOutput(t *testing.T) {
	cases := []struct {
		name     string
		response string
		wantErr  string
	}{
		{
			name: "invents a nonexistent module",
			response: reply(OutcomeConfig, "用 MACD 金叉判断。", map[string]any{
				"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{map[string]any{"module": "macd_cross", "params": map[string]any{}}},
				"risk":    risk("1000"),
			}),
			wantErr: "unknown module",
		},
		{
			name: "invents a nonexistent parameter",
			response: reply(OutcomeConfig, "放量策略。", map[string]any{
				"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{map[string]any{
					"module": "volume_breakout",
					"params": map[string]any{"magic_factor": 3},
				}},
				"risk": risk("1000"),
			}),
			wantErr: "no such parameter",
		},
		{
			name: "parameter out of range",
			response: reply(OutcomeConfig, "回看 5000 根。", map[string]any{
				"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{map[string]any{
					"module": "support_resistance",
					"params": map[string]any{"lookback": 5000},
				}},
				"risk": risk("1000"),
			}),
			wantErr: "above the allowed maximum",
		},
		{
			name: "unsupported timeframe",
			response: reply(OutcomeConfig, "3 秒线。", map[string]any{
				"name": "X", "symbol": "BTCUSDT", "timeframe": "3s", "combine": "ALL",
				"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
				"risk":    risk("1000"),
			}),
			wantErr: "is not supported",
		},
		{
			name: "WEIGHTED missing a weight",
			response: reply(OutcomeConfig, "加权组合。", map[string]any{
				"name": "X", "symbol": "BTCUSDT", "timeframe": "1h",
				"combine": "WEIGHTED", "threshold": 0.5,
				"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
				"risk":    risk("1000"),
			}),
			wantErr: "weight",
		},
		{
			name: "missing max position size per trade",
			response: reply(OutcomeConfig, "没有风控。", map[string]any{
				"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
				"risk":    map[string]any{},
			}),
			wantErr: "max position size per trade",
		},
		{
			name:     "outcome contradicts content: says clarify but gives a config",
			response: `{"outcome":"clarification_needed","restatement":"x","questions":["q"],"config":{"name":"X","symbol":"BTCUSDT","timeframe":"1h","combine":"ALL","modules":[],"risk":{}}}`,
			wantErr:  "must be null",
		},
		{
			name:     "outcome is config but no config was given",
			response: reply(OutcomeConfig, "有个策略。", nil),
			wantErr:  "no config was given",
		},
		{
			name:     "outcome is clarify but no questions",
			response: reply(OutcomeClarify, "不太清楚。", nil),
			wantErr:  "no clarifying questions were given",
		},
		{
			name:     "missing restatement",
			response: reply(OutcomeClarify, "   ", nil, "你想交易什么？"),
			wantErr:  "missing restatement",
		},
		{
			name:     "output isn't JSON at all",
			response: "好的，我建议你用 MACD 金叉策略。",
			wantErr:  "not valid JSON",
		},
		{
			name:     "extra field outside the schema",
			response: `{"outcome":"clarification_needed","restatement":"x","questions":["q"],"config":null,"confidence":0.9}`,
			wantErr:  "undefined fields",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// maxRetries=0: confirms a single failure is rejected outright, no repair attempted.
			a := New(&StubLLM{Responses: []string{tc.response}}, modules.NewDefaultRegistry(), 0)
			_, err := a.Translate(context.Background(), "随便一句话", nil, i18n.LangZH)
			if err == nil {
				t.Fatal("expected this output to be rejected, but it passed")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error message does not contain %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

// A retry must be a full do-over, not patching up the previous flawed output.
func TestRetryRegeneratesInsteadOfPatching(t *testing.T) {
	bad := reply(OutcomeConfig, "用 MACD。", map[string]any{
		"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": []any{map[string]any{"module": "macd_cross", "params": map[string]any{}}},
		"risk":    risk("1000"),
	})
	good := reply(OutcomeConfig, "标的 BTCUSDT，1 小时，放量触发。", map[string]any{
		"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
		"risk":    risk("1000"),
	})

	a, stub := newAgent(bad, good)
	p, err := a.Translate(context.Background(), "BTC 一小时", nil, i18n.LangZH)
	if err != nil {
		t.Fatalf("second attempt should succeed: %v", err)
	}
	if p.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", p.Attempts)
	}
	if p.Config.Modules[0].Module != "volume_breakout" {
		t.Errorf("final module = %s", p.Config.Modules[0].Module)
	}
	// The retry request must tell the model the reason for the previous failure.
	last := stub.Calls[1]
	retryText := last[len(last)-1].Text
	if !strings.Contains(retryText, "unknown module") {
		t.Errorf("retry prompt is missing the specific failure reason: %s", retryText)
	}
	if !strings.Contains(retryText, "重新生成") {
		t.Errorf("retry prompt should ask for regeneration, not a patch: %s", retryText)
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	bad := reply(OutcomeConfig, "用 MACD。", map[string]any{
		"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": []any{map[string]any{"module": "macd_cross", "params": map[string]any{}}},
		"risk":    risk("1000"),
	})
	a, stub := newAgent(bad)
	if _, err := a.Translate(context.Background(), "BTC", nil, i18n.LangZH); err == nil {
		t.Fatal("should eventually fail when the model keeps producing invalid output")
	}
	if len(stub.Calls) != 3 { // 1 initial call + 2 retries
		t.Errorf("call count = %d, want 3 (maxRetries=2)", len(stub.Calls))
	}
}

// ---------- 4. Compliance red line ----------

func TestRejectsInvestmentAdviceLanguage(t *testing.T) {
	cfg := map[string]any{
		"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
		"risk":    risk("1000"),
	}
	cases := []struct {
		name     string
		response string
	}{
		{"advice slipped into the restatement", reply(OutcomeConfig, "标的 BTCUSDT。建议你把倍数调到 3 倍效果更好。", cfg)},
		{"restatement evaluates the strategy", reply(OutcomeConfig, "标的 BTCUSDT，这个策略风险较高。", cfg)},
		{"restatement talks about returns", reply(OutcomeConfig, "标的 BTCUSDT，预期收益不错。", cfg)},
		{"advice slipped into a question", reply(OutcomeClarify, "标的 BTCUSDT。", nil, "推荐你设置 1000 USDT 的上限，可以吗？")},
		{"English advice wording", reply(OutcomeConfig, "Symbol BTCUSDT. I recommend raising the multiplier.", cfg)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := New(&StubLLM{Responses: []string{tc.response}}, modules.NewDefaultRegistry(), 0)
			_, err := a.Translate(context.Background(), "BTC 一小时放量", nil, i18n.LangZH)
			if err == nil {
				t.Fatal("output with investment-advice wording must be rejected")
			}
			var ce *ComplianceError
			if !errors.As(err, &ce) {
				t.Fatalf("expected *ComplianceError, got %T: %v", err, err)
			}
		})
	}
}

// A purely factual restatement should not be caught by the compliance check.
func TestCompliantRestatementPasses(t *testing.T) {
	texts := []string{
		"标的 BTCUSDT，1 小时周期，当成交量达到近期 20 根均量的 3 倍时触发做多信号。单笔最大仓位 1000 USDT。",
		"标的 ETHUSDT，4 小时周期。需要同时满足：价格突破关键阻力位，且成交量放大 2 倍。",
		"以下参数由你指定：倍数 3。以下参数使用系统默认值：均量窗口 20、方向判定来源 candle。",
	}
	for _, text := range texts {
		if err := scanText("restatement", text); err != nil {
			t.Errorf("a normal restatement was flagged as non-compliant: %v", err)
		}
	}
}

// ---------- 5. Confirmation loop: never persisted without confirmation ----------

func TestConfirmRequiresExplicitApproval(t *testing.T) {
	good := reply(OutcomeConfig, "标的 BTCUSDT，1 小时，放量触发。", map[string]any{
		"name": "BTC 放量", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
		"risk":    risk("1000"),
	})
	a, _ := newAgent(good)
	p, err := a.Translate(context.Background(), "BTC 一小时放量做多", nil, i18n.LangZH)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Confirm(p, false); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("should return ErrNotConfirmed when not confirmed, got: %v", err)
	}

	cfg, err := a.Confirm(p, true)
	if err != nil {
		t.Fatalf("should succeed once confirmed: %v", err)
	}
	if cfg.State != types.StateDraft {
		t.Errorf("new strategy state = %s, want DRAFT (must complete backtesting and paper trading before going live)", cfg.State)
	}
	if cfg.SourceUtterance != "BTC 一小时放量做多" {
		t.Errorf("the user's original words were not preserved, got: %q", cfg.SourceUtterance)
	}
	if cfg.CreatedAt.IsZero() {
		t.Error("a creation timestamp should be stamped on confirm")
	}
}

func TestConfirmRejectsClarificationProposal(t *testing.T) {
	a, _ := newAgent(reply(OutcomeClarify, "还缺信息。", nil, "交易什么？"))
	p, err := a.Translate(context.Background(), "放量就买", nil, i18n.LangZH)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Confirm(p, true); err == nil {
		t.Fatal("a proposal still awaiting clarification must not be confirmable")
	}
}

// If a proposal is tampered with by the caller after generation, confirm must catch it via revalidation.
func TestConfirmRevalidatesTamperedProposal(t *testing.T) {
	good := reply(OutcomeConfig, "标的 BTCUSDT，1 小时，放量触发。", map[string]any{
		"name": "BTC 放量", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
		"risk":    risk("1000"),
	})
	a, _ := newAgent(good)
	p, _ := a.Translate(context.Background(), "BTC 一小时放量", nil, i18n.LangZH)

	p.Config.Modules[0].Module = "不存在的模块"
	if _, err := a.Confirm(p, true); err == nil {
		t.Fatal("a tampered proposal should be caught at confirm time")
	}
}

func TestTranslateRejectsEmptyUtterance(t *testing.T) {
	a, _ := newAgent(reply(OutcomeClarify, "x", nil, "q"))
	if _, err := a.Translate(context.Background(), "   ", nil, i18n.LangZH); err == nil {
		t.Fatal("an empty description should be rejected")
	}
}

// ---------- 6. Schema stays in sync with the module registry ----------

// The schema must be generated live from the registry: it should automatically
// keep up when a module is added, rather than leaving a handwritten copy to rot.
func TestSchemaIsDerivedFromRegistry(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	schema := BuildSchema(reg, i18n.LangZH)

	blob, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	text := string(blob)

	for _, name := range reg.Names() {
		if !strings.Contains(text, name) {
			t.Errorf("schema is missing registered module %q", name)
		}
	}
	// Every module's parameter names should appear in the schema.
	for _, m := range reg.All() {
		for _, p := range m.RequiredParams() {
			if !strings.Contains(text, p.Name) {
				t.Errorf("schema is missing param %q of module %s", p.Name, m.Name())
			}
		}
	}
}

// The system prompt must state the compliance boundaries up front, and include the module catalog.
func TestSystemPromptCarriesBoundariesAndCatalog(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	prompt := SystemPrompt(reg, i18n.LangZH)

	for _, must := range []string{"不是投资顾问", "绝不评价", "绝不主动推荐", "clarification_needed"} {
		if !strings.Contains(prompt, must) {
			t.Errorf("system prompt is missing a key constraint: %q", must)
		}
	}
	for _, name := range reg.Names() {
		if !strings.Contains(prompt, name) {
			t.Errorf("system prompt's module catalog is missing %q", name)
		}
	}
}
