package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/internal/modules"
	"tradeforge/internal/modules/cvdorderflow"
	"tradeforge/internal/modules/supportresistance"
	"tradeforge/internal/modules/volumebreakout"
	"tradeforge/pkg/types"
)

// realStrategy 构造一个使用全部三个真实模块的策略配置。
func realStrategy(combine types.CombineMode, threshold float64) types.StrategyConfig {
	return types.StrategyConfig{
		ID:        "22222222-2222-4222-8222-222222222222",
		Name:      "三模块联合策略",
		Symbol:    "BTCUSDT",
		Timeframe: types.TF1h,
		Combine:   combine,
		Threshold: threshold,
		Modules: []types.ModuleConfig{
			{
				Module: supportresistance.ModuleName, Weight: 0.4,
				Params: map[string]any{"pivot_strength": 1, "min_touches": 2},
			},
			{
				Module: volumebreakout.ModuleName, Weight: 0.35,
				Params: map[string]any{"window": 20, "multiplier": 2.0},
			},
			{
				Module: cvdorderflow.ModuleName, Weight: 0.25,
				Params: map[string]any{"window": 50, "imbalance_threshold": 0.2},
			},
		},
		Risk: types.RiskConfig{
			MaxPositionSizeQuote: decimal.NewFromInt(1000),
			MaxDailyLossQuote:    decimal.NewFromInt(100),
			MaxHoldingPeriod:     types.D(0),
			StopLossPct:          0.02,
		},
		State: types.StateDraft,
	}
}

// bullishBreakoutData 构造一段"三个模块的条件同时成立"的行情：
//
//  1. 先在 100~110 区间反复震荡，形成被多次触及的关键位（喂 support_resistance）
//  2. 再用一段主动买盘占优的推升行情走回 110，把 CVD 窗口内的失衡度拉起来
//     （喂 cvd_orderflow —— 震荡段买卖对半，单靠它 CVD 会一直是中性）
//  3. 最后一根放 3 倍量、收在 113（喂 volume_breakout，同时确认突破）
func bullishBreakoutData() types.MarketData {
	b := synth.New("BTCUSDT", types.TF1h, start)
	b.Oscillate(60, 100, 110, 1000)
	b.Trend(19, 100, 110, 1000, 0.85)
	b.AddBar(110, 113, 3000, 0.85)
	return b.Build()
}

// 端到端：三个真实模块 + ALL 组合，在三者同向时应触发。
func TestIntegrationAllModulesAgree(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	cfg := realStrategy(types.CombineAll, 0)
	aud, pub := &recordingAuditor{}, &recordingPublisher{}

	e := New(reg, WithAuditor(aud), WithPublisher(pub), WithLogger(quietLogger()))
	d, err := e.Process(context.Background(), cfg, feedsFor(bullishBreakoutData()))
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}

	t.Logf("决策：triggered=%v dir=%s score=%.3f\n原因：%s", d.Triggered, d.Direction, d.Score, d.Reason)
	for _, s := range d.Signals {
		t.Logf("  %-20s %-8s conf=%.3f  %s", s.Module, s.Direction, s.Confidence, s.Reason)
	}

	if len(d.Signals) != 3 {
		t.Fatalf("信号数 = %d，期望 3", len(d.Signals))
	}
	for _, s := range d.Signals {
		if s.Degraded {
			t.Errorf("模块 %s 意外降级：%s", s.Module, s.Err)
		}
		if s.Direction != types.DirectionLong {
			t.Errorf("模块 %s 方向 = %s，期望 LONG（原因：%s）", s.Module, s.Direction, s.Reason)
		}
	}
	if !d.Triggered {
		t.Fatalf("三个模块同向时 ALL 应触发：%s", d.Reason)
	}
	if d.Direction != types.DirectionLong {
		t.Errorf("决策方向 = %s，期望 LONG", d.Direction)
	}

	// 全链路留痕：审计与发布各一条，且带上完整的信号明细。
	if len(aud.decisions) != 1 || len(pub.decisions) != 1 {
		t.Fatalf("审计 %d 条、发布 %d 条，各期望 1 条", len(aud.decisions), len(pub.decisions))
	}
	if len(aud.decisions[0].Signals) != 3 {
		t.Error("审计记录必须保留全部模块信号，否则事后无法解释这笔交易")
	}
}

// 同一段行情、同一组模块，换成 WEIGHTED 组合也应触发，且分数可解释。
func TestIntegrationWeightedCombine(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	cfg := realStrategy(types.CombineWeighted, 0.5)

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(bullishBreakoutData()))
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	t.Logf("加权决策：triggered=%v dir=%s score=%.3f\n原因：%s", d.Triggered, d.Direction, d.Score, d.Reason)

	if !d.Triggered || d.Direction != types.DirectionLong {
		t.Fatalf("期望触发 LONG，实际 triggered=%v dir=%s：%s", d.Triggered, d.Direction, d.Reason)
	}
	// 权重之和为 1.0，故 Score 就是加权置信度，必须落在 [-1, 1]。
	if d.Score < -1 || d.Score > 1 {
		t.Errorf("Score = %v 超出 [-1, 1]", d.Score)
	}
}

// 平静行情下三个模块都应沉默，两种组合逻辑都不该触发。
func TestIntegrationQuietMarketDoesNotTrigger(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	// 完全平坦：无关键位、无放量、买卖对半。
	b := synth.New("BTCUSDT", types.TF1h, start)
	for i := 0; i < 120; i++ {
		b.AddBar(100, 100, 1000, 0.5)
	}
	md := b.Build()

	for _, tc := range []struct {
		name string
		cfg  types.StrategyConfig
	}{
		{"ALL", realStrategy(types.CombineAll, 0)},
		{"WEIGHTED", realStrategy(types.CombineWeighted, 0.5)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), tc.cfg, feedsFor(md))
			if err != nil {
				t.Fatal(err)
			}
			if d.Triggered {
				t.Errorf("平静行情不应触发：%s", d.Reason)
			}
			for _, s := range d.Signals {
				if s.Direction != types.DirectionNeutral {
					t.Errorf("模块 %s 在平静行情下给出了 %s：%s", s.Module, s.Direction, s.Reason)
				}
			}
		})
	}
}

// 不同标的可以配完全不同的模块组合，且互不干扰——这是平台的一等公民功能。
func TestIntegrationPerSymbolIndependentCombinations(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	e := New(reg, WithLogger(quietLogger()))

	// BTC：三模块 ALL 组合。
	btcCfg := realStrategy(types.CombineAll, 0)
	btcData := bullishBreakoutData()

	// ETH：只用成交量模块，WEIGHTED 组合，参数也完全不同。
	ethCfg := types.StrategyConfig{
		ID: "33333333-3333-4333-8333-333333333333", Name: "ETH 单模块策略",
		Symbol: "ETHUSDT", Timeframe: types.TF15m,
		Combine: types.CombineWeighted, Threshold: 0.5,
		Modules: []types.ModuleConfig{{
			Module: volumebreakout.ModuleName, Weight: 1.0,
			Params: map[string]any{"window": 10, "multiplier": 1.5},
		}},
		Risk:  types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(500)},
		State: types.StateDraft,
	}
	ethB := synth.New("ETHUSDT", types.TF15m, start)
	for i := 0; i < 10; i++ {
		ethB.AddBar(2000, 2000, 500, 0.5)
	}
	ethB.AddBar(2000, 2050, 1200, 0.8) // 2.4 倍量的阳线
	ethData := ethB.Build()

	btcDecision, err := e.Evaluate(context.Background(), btcCfg, feedsFor(btcData))
	if err != nil {
		t.Fatal(err)
	}
	ethDecision, err := e.Evaluate(context.Background(), ethCfg, feedsFor(ethData))
	if err != nil {
		t.Fatal(err)
	}

	if btcDecision.Symbol != "BTCUSDT" || ethDecision.Symbol != "ETHUSDT" {
		t.Fatalf("标的串了：btc=%s eth=%s", btcDecision.Symbol, ethDecision.Symbol)
	}
	if len(btcDecision.Signals) != 3 {
		t.Errorf("BTC 策略信号数 = %d，期望 3", len(btcDecision.Signals))
	}
	if len(ethDecision.Signals) != 1 {
		t.Errorf("ETH 策略信号数 = %d，期望 1", len(ethDecision.Signals))
	}
	if !ethDecision.Triggered {
		t.Errorf("ETH 策略应触发：%s", ethDecision.Reason)
	}
	// ETH 的模块清单不该混入 BTC 的模块。
	for _, s := range ethDecision.Signals {
		if s.Module != volumebreakout.ModuleName {
			t.Errorf("ETH 决策里出现了非配置模块 %s", s.Module)
		}
	}
}

// 每条决策都要能回答"为什么"，Reason 与各模块 Reason 都不得为空。
func TestIntegrationDecisionIsExplainable(t *testing.T) {
	reg := modules.NewDefaultRegistry()
	cfg := realStrategy(types.CombineWeighted, 0.5)

	d, err := New(reg, WithLogger(quietLogger())).Evaluate(context.Background(), cfg, feedsFor(bullishBreakoutData()))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(d.Reason) == "" {
		t.Error("决策必须带原因说明")
	}
	for _, s := range d.Signals {
		if strings.TrimSpace(s.Reason) == "" {
			t.Errorf("模块 %s 的信号没有说明触发原因", s.Module)
		}
		if s.Timestamp.IsZero() {
			t.Errorf("模块 %s 的信号缺少时间戳", s.Module)
		}
	}
	if d.Timestamp.IsZero() || d.EvaluatedAt.IsZero() {
		t.Error("决策必须同时记录行情时间与计算时间")
	}
	if !d.Price.IsPositive() {
		t.Errorf("决策价格 = %s，期望为正", d.Price)
	}
}
