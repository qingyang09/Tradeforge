package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"tradeforge/internal/modules"
	"tradeforge/pkg/types"
)

func newAgent(responses ...string) (*Agent, *StubLLM) {
	stub := &StubLLM{Responses: responses}
	return New(stub, modules.NewDefaultRegistry(), 2), stub
}

// reply 构造一份模型输出的 JSON 文本。
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

// ---------- 一、正常翻译：自然语言 → 期望的配置结构 ----------

// 这十组用例是"一句话 → StrategyConfig"的契约测试。
// 断言的是翻译层的校验与转换行为，模型输出由桩提供。
func TestTranslationPairs(t *testing.T) {
	cases := []struct {
		name      string
		utterance string
		modelJSON string
		want      func(*testing.T, types.StrategyConfig)
	}{
		{
			name:      "1. 单模块 + 明确参数",
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
					t.Errorf("标的/周期 = %s/%s", c.Symbol, c.Timeframe)
				}
				if len(c.Modules) != 1 || c.Modules[0].Module != "volume_breakout" {
					t.Fatalf("模块 = %+v", c.Modules)
				}
				if c.Modules[0].Params["multiplier"] != 3.0 {
					t.Errorf("multiplier = %v，期望 3.0", c.Modules[0].Params["multiplier"])
				}
				// 用户没提的参数必须缺席，由系统填默认值，而不是被模型编造。
				if _, ok := c.Modules[0].Params["window"]; ok {
					t.Error("用户没提的 window 不应出现在配置里")
				}
			},
		},
		{
			name:      "2. 多模块 ALL 组合",
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
					t.Errorf("组合方式 = %s，期望 ALL", c.Combine)
				}
				if len(c.Modules) != 2 {
					t.Errorf("模块数 = %d，期望 2", len(c.Modules))
				}
			},
		},
		{
			name:      "3. WEIGHTED 组合带权重与阈值",
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
					t.Errorf("组合/阈值 = %s/%v", c.Combine, c.Threshold)
				}
				if c.Modules[0].Weight != 0.7 || c.Modules[1].Weight != 0.3 {
					t.Errorf("权重 = %v / %v", c.Modules[0].Weight, c.Modules[1].Weight)
				}
			},
		},
		{
			name:      "4. 完整风控参数",
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
					t.Errorf("单笔仓位 = %s", c.Risk.MaxPositionSizeQuote)
				}
				if c.Risk.MaxDailyLossQuote.String() != "200" {
					t.Errorf("日亏上限 = %s", c.Risk.MaxDailyLossQuote)
				}
				if c.Risk.MaxHoldingPeriod.Std().Hours() != 4 {
					t.Errorf("持仓上限 = %s", c.Risk.MaxHoldingPeriod)
				}
				if c.Risk.StopLossPct != 0.02 {
					t.Errorf("止损 = %v", c.Risk.StopLossPct)
				}
			},
		},
		{
			name:      "5. CVD 失衡模块与枚举参数",
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
					t.Errorf("参数 = %+v", p)
				}
			},
		},
		{
			name:      "6. 标的名归一化为交易所符号",
			utterance: "比特币日线，跌破支撑就平仓",
			modelJSON: reply(OutcomeConfig, "标的 BTCUSDT，日线，跌破关键支撑位时触发。", map[string]any{
				"name": "BTC 日线支撑", "symbol": "btcusdt", "timeframe": "1d", "combine": "ALL",
				"modules": []any{map[string]any{"module": "support_resistance", "params": map[string]any{}}},
				"risk":    risk("1000"),
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if c.Symbol != "BTCUSDT" {
					t.Errorf("标的 = %q，期望归一化为大写 BTCUSDT", c.Symbol)
				}
			},
		},
		{
			name:      "7. 支撑阻力的细化参数",
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
				// JSON 数字解出来是 float64，交给引擎时再由 ResolveParams 规范化。
				if p["lookback"] != float64(300) || p["min_touches"] != float64(3) {
					t.Errorf("参数 = %+v", p)
				}
			},
		},
		{
			name:      "8. 三模块全组合",
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
					t.Errorf("模块数 = %d，期望 3", len(c.Modules))
				}
			},
		},
		{
			name:      "9. 金额用字符串承载，不丢精度",
			utterance: "BTC 一小时放量做多，单笔 1234.56789 USDT",
			modelJSON: reply(OutcomeConfig, "标的 BTCUSDT，1 小时，放量触发，单笔仓位 1234.56789。", map[string]any{
				"name": "BTC 精度测试", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
				"risk":    risk("1234.56789"),
			}),
			want: func(t *testing.T, c types.StrategyConfig) {
				if got := c.Risk.MaxPositionSizeQuote.String(); got != "1234.56789" {
					t.Errorf("金额 = %s，期望 1234.56789（不得经过 float64）", got)
				}
			},
		},
		{
			name:      "10. ETH 独立配置，与 BTC 互不相干",
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
					t.Errorf("标的/周期 = %s/%s", c.Symbol, c.Timeframe)
				}
			},
		},
		{
			name:      "11. 用支撑/阻力位描述止损止盈，不是固定百分比",
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
					t.Errorf("StopLossMode = %q，期望 support_resistance", c.Risk.StopLossMode)
				}
				if c.Risk.TakeProfitMode != types.RiskLevelModeSupportResistance {
					t.Errorf("TakeProfitMode = %q，期望 support_resistance", c.Risk.TakeProfitMode)
				}
				if c.Risk.StopLossPct != 0 || c.Risk.TakeProfitPct != 0 {
					t.Errorf("support_resistance 模式下不应该同时带百分比：sl=%v tp=%v",
						c.Risk.StopLossPct, c.Risk.TakeProfitPct)
				}
				hasSR := false
				for _, m := range c.Modules {
					if m.Module == "support_resistance" {
						hasSR = true
					}
				}
				if !hasSR {
					t.Error("support_resistance 模式要求 support_resistance 模块也在 modules 列表里")
				}
			},
		},
		{
			name:      "12. 用 POC（成交量分布重心）描述止损",
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
					t.Errorf("StopLossMode = %q，期望 poc", c.Risk.StopLossMode)
				}
				if c.Risk.StopLossPct != 0 {
					t.Errorf("poc 模式下不应该同时带百分比：sl=%v", c.Risk.StopLossPct)
				}
				hasPOC := false
				for _, m := range c.Modules {
					if m.Module == "poc" {
						hasPOC = true
					}
				}
				if !hasPOC {
					t.Error("poc 模式要求 poc 模块也在 modules 列表里")
				}
			},
		},
		{
			name: "13. 多周期：1 小时判断盘整假突破、15 分钟判断放量下跌触发",
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
					t.Errorf("顶层 timeframe = %s，期望 15m（最快的那个，即触发周期）", c.Timeframe)
				}
				byName := map[string]types.ModuleConfig{}
				for _, m := range c.Modules {
					byName[m.Module] = m
				}
				if byName["fakeout"].Timeframe != types.TF1h {
					t.Errorf("fakeout.timeframe = %q，期望 1h", byName["fakeout"].Timeframe)
				}
				if byName["poc"].Timeframe != types.TF1h {
					t.Errorf("poc.timeframe = %q，期望 1h", byName["poc"].Timeframe)
				}
				if tf := byName["volume_breakout"].Timeframe; tf != "" && tf != types.TF15m {
					t.Errorf("volume_breakout.timeframe = %q，期望留空或 15m（跟随触发周期）", tf)
				}
				if c.Risk.StopLossMode != types.RiskLevelModePOC {
					t.Errorf("StopLossMode = %q，期望 poc", c.Risk.StopLossMode)
				}
			},
		},
		{
			name:      "14. 按账户风险百分比开仓，而不是固定金额",
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
					t.Errorf("PositionSizingMode = %q，期望 risk_pct", c.Risk.PositionSizingMode)
				}
				if got := c.Risk.AccountEquityQuote.String(); got != "10000" {
					t.Errorf("AccountEquityQuote = %s，期望 10000", got)
				}
				if c.Risk.RiskPerTradePct != 0.01 {
					t.Errorf("RiskPerTradePct = %v，期望 0.01", c.Risk.RiskPerTradePct)
				}
				if c.Risk.StopLossPct != 0.02 {
					t.Errorf("StopLossPct = %v，期望 0.02", c.Risk.StopLossPct)
				}
				if got := c.Risk.MaxPositionSizeQuote.String(); got != "10000" {
					t.Errorf("MaxPositionSizeQuote = %s，期望默认等于账户权益 10000", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newAgent(tc.modelJSON)
			p, err := a.Translate(context.Background(), tc.utterance, nil)
			if err != nil {
				t.Fatalf("翻译失败：%v", err)
			}
			if p.NeedsClarification() {
				t.Fatalf("期望直接给出配置，实际要求澄清：%v", p.Questions)
			}
			if p.Restatement == "" {
				t.Error("必须有复述内容供用户确认")
			}
			if strings.HasPrefix(tc.name, "13.") &&
				!strings.Contains(p.Restatement, "参与") {
				t.Errorf("poc 只是用来取止损价、不是用户描述的入场条件之一，"+
					"restatement 必须提醒它的方向信号也会参与入场判断，实际：%q", p.Restatement)
			}
			tc.want(t, *p.Config)
		})
	}
}

// ---------- 二、歧义处理：必须提问，不许瞎猜 ----------

func TestAmbiguousInputsAskInsteadOfGuessing(t *testing.T) {
	cases := []struct {
		name      string
		utterance string
		modelJSON string
	}{
		{
			name:      "没说标的",
			utterance: "放量就买",
			modelJSON: reply(OutcomeClarify, "你想在成交量放大时开仓，但还缺少一些必要信息。", nil,
				"你想交易哪个标的？请给出交易对，例如 BTCUSDT。",
				"用哪个 K 线周期？可选 1m/5m/15m/1h/4h/1d。",
				"成交量要放大到均量的多少倍才触发？"),
		},
		{
			name:      "突破方向不明",
			utterance: "BTC 一小时，突破就操作",
			modelJSON: reply(OutcomeClarify, "标的 BTCUSDT、周期 1 小时已明确，但触发条件还需要确认。", nil,
				"这里的“突破”指向上突破阻力位，还是向下跌破支撑位？",
				"单笔最多投入多少 USDT？"),
		},
		{
			name:      "参数超出允许范围",
			utterance: "BTC 一小时，回看 5000 根 K 线找支撑位",
			modelJSON: reply(OutcomeClarify, "标的 BTCUSDT、1 小时周期已明确，回看窗口的取值需要确认。", nil,
				"回看窗口 5000 超出了平台允许的 30 ~ 1000 范围，你希望改用哪个数值？"),
		},
		{
			name:      "指标不在平台支持范围内",
			utterance: "BTC 一小时，MACD 金叉就买",
			modelJSON: reply(OutcomeClarify, "标的 BTCUSDT、1 小时周期已明确。", nil,
				"平台目前提供的模块为支撑阻力、成交量突破、CVD 订单流，暂不支持 MACD。你希望改用其中哪一个来表达这条规则？"),
		},
		{
			name:      "完全没提风控",
			utterance: "ETH 4 小时放量 2 倍做多",
			modelJSON: reply(OutcomeClarify, "标的 ETHUSDT，4 小时，放量 2 倍触发。", nil,
				"单笔最多投入多少 USDT？这是平台强制要求的风控项。"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newAgent(tc.modelJSON)
			p, err := a.Translate(context.Background(), tc.utterance, nil)
			if err != nil {
				t.Fatalf("翻译失败：%v", err)
			}
			if !p.NeedsClarification() {
				t.Fatalf("歧义输入应当提问，实际直接给出了配置：%+v", p.Config)
			}
			if len(p.Questions) == 0 {
				t.Error("要求澄清时必须给出具体问题")
			}
			if p.Config != nil {
				t.Error("要求澄清时不得同时给出配置")
			}
		})
	}
}

// 澄清后用户补充信息，第二轮应当能给出配置，且历史被带进上下文。
func TestClarificationLoopContinuesWithHistory(t *testing.T) {
	first := reply(OutcomeClarify, "缺少标的信息。", nil, "你想交易哪个标的？")
	second := reply(OutcomeConfig, "标的 BTCUSDT，1 小时，放量 2 倍触发。", map[string]any{
		"name": "BTC 放量", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{"multiplier": 2.0}}},
		"risk":    risk("1000"),
	})

	a, stub := newAgent(first, second)

	p1, err := a.Translate(context.Background(), "放量 2 倍，一小时线", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !p1.NeedsClarification() {
		t.Fatal("第一轮应当提问")
	}

	history := []Turn{
		{Role: "user", Text: "放量 2 倍，一小时线"},
		{Role: "assistant", Text: strings.Join(p1.Questions, "\n")},
	}
	p2, err := a.Translate(context.Background(), "BTC", history)
	if err != nil {
		t.Fatal(err)
	}
	if p2.NeedsClarification() {
		t.Fatalf("补充信息后仍在提问：%v", p2.Questions)
	}
	if p2.Config.Symbol != "BTCUSDT" {
		t.Errorf("标的 = %s", p2.Config.Symbol)
	}
	// 第二次调用必须带上历史，否则模型看不到上一轮的问答。
	if len(stub.Calls[1]) <= len(stub.Calls[0]) {
		t.Errorf("第二次调用的轮次数 %d 未超过第一次的 %d，历史没有被带上",
			len(stub.Calls[1]), len(stub.Calls[0]))
	}
}

// ---------- 三、拒绝非法输出，不做"尽力修复" ----------

func TestRejectsInvalidModelOutput(t *testing.T) {
	cases := []struct {
		name     string
		response string
		wantErr  string
	}{
		{
			name: "编造不存在的模块",
			response: reply(OutcomeConfig, "用 MACD 金叉判断。", map[string]any{
				"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{map[string]any{"module": "macd_cross", "params": map[string]any{}}},
				"risk":    risk("1000"),
			}),
			wantErr: "未知模块",
		},
		{
			name: "编造不存在的参数",
			response: reply(OutcomeConfig, "放量策略。", map[string]any{
				"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{map[string]any{
					"module": "volume_breakout",
					"params": map[string]any{"magic_factor": 3},
				}},
				"risk": risk("1000"),
			}),
			wantErr: "不存在此参数",
		},
		{
			name: "参数超出范围",
			response: reply(OutcomeConfig, "回看 5000 根。", map[string]any{
				"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{map[string]any{
					"module": "support_resistance",
					"params": map[string]any{"lookback": 5000},
				}},
				"risk": risk("1000"),
			}),
			wantErr: "大于允许的最大值",
		},
		{
			name: "周期不受支持",
			response: reply(OutcomeConfig, "3 秒线。", map[string]any{
				"name": "X", "symbol": "BTCUSDT", "timeframe": "3s", "combine": "ALL",
				"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
				"risk":    risk("1000"),
			}),
			wantErr: "不受支持",
		},
		{
			name: "WEIGHTED 缺少权重",
			response: reply(OutcomeConfig, "加权组合。", map[string]any{
				"name": "X", "symbol": "BTCUSDT", "timeframe": "1h",
				"combine": "WEIGHTED", "threshold": 0.5,
				"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
				"risk":    risk("1000"),
			}),
			wantErr: "权重",
		},
		{
			name: "缺少单笔仓位上限",
			response: reply(OutcomeConfig, "没有风控。", map[string]any{
				"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
				"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
				"risk":    map[string]any{},
			}),
			wantErr: "单笔最大仓位",
		},
		{
			name:     "outcome 与内容矛盾：说要澄清却给了配置",
			response: `{"outcome":"clarification_needed","restatement":"x","questions":["q"],"config":{"name":"X","symbol":"BTCUSDT","timeframe":"1h","combine":"ALL","modules":[],"risk":{}}}`,
			wantErr:  "必须为 null",
		},
		{
			name:     "outcome 为 config 却没有配置",
			response: reply(OutcomeConfig, "有个策略。", nil),
			wantErr:  "没有给出配置",
		},
		{
			name:     "outcome 为澄清却没有问题",
			response: reply(OutcomeClarify, "不太清楚。", nil),
			wantErr:  "没有给出任何澄清问题",
		},
		{
			name:     "缺少复述",
			response: reply(OutcomeClarify, "   ", nil, "你想交易什么？"),
			wantErr:  "缺少复述内容",
		},
		{
			name:     "输出根本不是 JSON",
			response: "好的，我建议你用 MACD 金叉策略。",
			wantErr:  "不是合法的 JSON",
		},
		{
			name:     "多出 schema 之外的字段",
			response: `{"outcome":"clarification_needed","restatement":"x","questions":["q"],"config":null,"confidence":0.9}`,
			wantErr:  "未定义字段",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// maxRetries=0：确认单次失败即拒绝，不做修复。
			a := New(&StubLLM{Responses: []string{tc.response}}, modules.NewDefaultRegistry(), 0)
			_, err := a.Translate(context.Background(), "随便一句话", nil)
			if err == nil {
				t.Fatal("期望拒绝该输出，实际通过了")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("错误信息中未包含 %q，实际：%v", tc.wantErr, err)
			}
		})
	}
}

// 重试必须是"整份重来"，而不是把上一次的残次品修修补补。
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
	p, err := a.Translate(context.Background(), "BTC 一小时", nil)
	if err != nil {
		t.Fatalf("第二次应当成功：%v", err)
	}
	if p.Attempts != 2 {
		t.Errorf("Attempts = %d，期望 2", p.Attempts)
	}
	if p.Config.Modules[0].Module != "volume_breakout" {
		t.Errorf("最终模块 = %s", p.Config.Modules[0].Module)
	}
	// 重试请求里必须把上一次的错误原因告诉模型。
	last := stub.Calls[1]
	retryText := last[len(last)-1].Text
	if !strings.Contains(retryText, "未知模块") {
		t.Errorf("重试提示未包含具体失败原因：%s", retryText)
	}
	if !strings.Contains(retryText, "重新生成") {
		t.Errorf("重试提示应当要求重新生成而非修补：%s", retryText)
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	bad := reply(OutcomeConfig, "用 MACD。", map[string]any{
		"name": "X", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": []any{map[string]any{"module": "macd_cross", "params": map[string]any{}}},
		"risk":    risk("1000"),
	})
	a, stub := newAgent(bad)
	if _, err := a.Translate(context.Background(), "BTC", nil); err == nil {
		t.Fatal("持续输出非法内容时应当最终失败")
	}
	if len(stub.Calls) != 3 { // 1 次首发 + 2 次重试
		t.Errorf("调用次数 = %d，期望 3（maxRetries=2）", len(stub.Calls))
	}
}

// ---------- 四、合规红线 ----------

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
		{"复述里夹带建议", reply(OutcomeConfig, "标的 BTCUSDT。建议你把倍数调到 3 倍效果更好。", cfg)},
		{"复述里评价策略", reply(OutcomeConfig, "标的 BTCUSDT，这个策略风险较高。", cfg)},
		{"复述里谈收益", reply(OutcomeConfig, "标的 BTCUSDT，预期收益不错。", cfg)},
		{"问题里夹带建议", reply(OutcomeClarify, "标的 BTCUSDT。", nil, "推荐你设置 1000 USDT 的上限，可以吗？")},
		{"英文建议措辞", reply(OutcomeConfig, "Symbol BTCUSDT. I recommend raising the multiplier.", cfg)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := New(&StubLLM{Responses: []string{tc.response}}, modules.NewDefaultRegistry(), 0)
			_, err := a.Translate(context.Background(), "BTC 一小时放量", nil)
			if err == nil {
				t.Fatal("含投资建议措辞的输出必须被拒绝")
			}
			var ce *ComplianceError
			if !errors.As(err, &ce) {
				t.Fatalf("期望 *ComplianceError，得到 %T：%v", err, err)
			}
		})
	}
}

// 纯事实性的复述不应被合规检查误伤。
func TestCompliantRestatementPasses(t *testing.T) {
	texts := []string{
		"标的 BTCUSDT，1 小时周期，当成交量达到近期 20 根均量的 3 倍时触发做多信号。单笔最大仓位 1000 USDT。",
		"标的 ETHUSDT，4 小时周期。需要同时满足：价格突破关键阻力位，且成交量放大 2 倍。",
		"以下参数由你指定：倍数 3。以下参数使用系统默认值：均量窗口 20、方向判定来源 candle。",
	}
	for _, text := range texts {
		if err := scanText("restatement", text); err != nil {
			t.Errorf("正常复述被误判为违规：%v", err)
		}
	}
}

// ---------- 五、确认循环：未确认绝不入库 ----------

func TestConfirmRequiresExplicitApproval(t *testing.T) {
	good := reply(OutcomeConfig, "标的 BTCUSDT，1 小时，放量触发。", map[string]any{
		"name": "BTC 放量", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
		"risk":    risk("1000"),
	})
	a, _ := newAgent(good)
	p, err := a.Translate(context.Background(), "BTC 一小时放量做多", nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := a.Confirm(p, false); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("未确认时应返回 ErrNotConfirmed，得到：%v", err)
	}

	cfg, err := a.Confirm(p, true)
	if err != nil {
		t.Fatalf("确认后应当成功：%v", err)
	}
	if cfg.State != types.StateDraft {
		t.Errorf("新策略状态 = %s，期望 DRAFT（必须走完回测与模拟盘才能上实盘）", cfg.State)
	}
	if cfg.SourceUtterance != "BTC 一小时放量做多" {
		t.Errorf("未保留用户原话，实际：%q", cfg.SourceUtterance)
	}
	if cfg.CreatedAt.IsZero() {
		t.Error("确认时应打上创建时间")
	}
}

func TestConfirmRejectsClarificationProposal(t *testing.T) {
	a, _ := newAgent(reply(OutcomeClarify, "还缺信息。", nil, "交易什么？"))
	p, err := a.Translate(context.Background(), "放量就买", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Confirm(p, true); err == nil {
		t.Fatal("待澄清的提案不能被确认")
	}
}

// 提案生成后若被调用方篡改，确认时必须重新校验拦下。
func TestConfirmRevalidatesTamperedProposal(t *testing.T) {
	good := reply(OutcomeConfig, "标的 BTCUSDT，1 小时，放量触发。", map[string]any{
		"name": "BTC 放量", "symbol": "BTCUSDT", "timeframe": "1h", "combine": "ALL",
		"modules": []any{map[string]any{"module": "volume_breakout", "params": map[string]any{}}},
		"risk":    risk("1000"),
	})
	a, _ := newAgent(good)
	p, _ := a.Translate(context.Background(), "BTC 一小时放量", nil)

	p.Config.Modules[0].Module = "不存在的模块"
	if _, err := a.Confirm(p, true); err == nil {
		t.Fatal("被篡改的提案应当在确认时被拦下")
	}
}

func TestTranslateRejectsEmptyUtterance(t *testing.T) {
	a, _ := newAgent(reply(OutcomeClarify, "x", nil, "q"))
	if _, err := a.Translate(context.Background(), "   ", nil); err == nil {
		t.Fatal("空描述应当被拒绝")
	}
}

// ---------- 六、Schema 与模块注册表保持同步 ----------

// Schema 必须由注册表现场生成：新增模块时它应自动跟上，
// 而不是留一份手写副本等着腐烂。
func TestSchemaIsDerivedFromRegistry(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	schema := BuildSchema(reg)

	blob, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	text := string(blob)

	for _, name := range reg.Names() {
		if !strings.Contains(text, name) {
			t.Errorf("schema 中缺少已注册模块 %q", name)
		}
	}
	// 每个模块的参数名都应出现在 schema 里。
	for _, m := range reg.All() {
		for _, p := range m.RequiredParams() {
			if !strings.Contains(text, p.Name) {
				t.Errorf("schema 中缺少模块 %s 的参数 %q", m.Name(), p.Name)
			}
		}
	}
}

// system prompt 必须把合规边界写在最前面，且带上模块清单。
func TestSystemPromptCarriesBoundariesAndCatalog(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	prompt := SystemPrompt(reg)

	for _, must := range []string{"不是投资顾问", "绝不评价", "绝不主动推荐", "clarification_needed"} {
		if !strings.Contains(prompt, must) {
			t.Errorf("system prompt 缺少关键约束：%q", must)
		}
	}
	for _, name := range reg.Names() {
		if !strings.Contains(prompt, name) {
			t.Errorf("system prompt 的模块清单里缺少 %q", name)
		}
	}
}
