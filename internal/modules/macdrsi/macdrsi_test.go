package macdrsi

import (
	"context"
	"errors"
	"math/rand"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

var base = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

func d(f float64) decimal.Decimal { return decimal.NewFromFloat(f) }

// ---- 指标计算的正确性：脱离 Evaluate 直接核对数值，避免"看起来能跑"。----

// emaSeries 用简单平均作种子，第一个有效值必须恰好等于前 period 个值的均值，
// 之后按标准 EMA 递推公式滚动。
func TestEMASeriesKnownValues(t *testing.T) {
	values := []decimal.Decimal{d(1), d(2), d(3), d(4), d(5), d(6)}
	series, from := emaSeries(values, 3)
	if from != 2 {
		t.Fatalf("validFrom = %d，期望 2", from)
	}
	// 种子 = (1+2+3)/3 = 2
	if !series[2].Equal(d(2)) {
		t.Errorf("series[2] = %s，期望 2", series[2])
	}
	// k = 2/(3+1) = 0.5；series[3] = 4*0.5 + 2*0.5 = 3
	if !series[3].Equal(d(3)) {
		t.Errorf("series[3] = %s，期望 3", series[3])
	}
	// series[4] = 5*0.5 + 3*0.5 = 4
	if !series[4].Equal(d(4)) {
		t.Errorf("series[4] = %s，期望 4", series[4])
	}
}

func TestEMASeriesInsufficientData(t *testing.T) {
	values := []decimal.Decimal{d(1), d(2)}
	series, from := emaSeries(values, 5)
	if from != len(values) {
		t.Fatalf("validFrom = %d，期望 %d（数据不足时整体不可用）", from, len(values))
	}
	if len(series) != len(values) {
		t.Fatalf("series 长度 = %d，期望与 values 等长", len(series))
	}
}

// 持续上涨（每根都创新高）时，平均损失恒为零，RSI 必须是 100，而不是除零崩溃或误判。
func TestRSISeriesAllGainsIsHundred(t *testing.T) {
	closes := make([]decimal.Decimal, 20)
	for i := range closes {
		closes[i] = d(100 + float64(i))
	}
	rsi, from := rsiSeries(closes, 14)
	if from != 14 {
		t.Fatalf("validFrom = %d，期望 14", from)
	}
	for i := from; i < len(rsi); i++ {
		if rsi[i] != 100 {
			t.Errorf("rsi[%d] = %v，期望 100（持续上涨）", i, rsi[i])
		}
	}
}

// 持续下跌同理必须是 0。
func TestRSISeriesAllLossesIsZero(t *testing.T) {
	closes := make([]decimal.Decimal, 20)
	for i := range closes {
		closes[i] = d(100 - float64(i))
	}
	rsi, _ := rsiSeries(closes, 14)
	for i := 14; i < len(rsi); i++ {
		if rsi[i] != 0 {
			t.Errorf("rsi[%d] = %v，期望 0（持续下跌）", i, rsi[i])
		}
	}
}

// 价格完全不变：没有涨跌，RSI 应为中性的 50，而不是因为 0/0 报错或被判成极值。
func TestRSISeriesFlatIsFifty(t *testing.T) {
	closes := make([]decimal.Decimal, 20)
	for i := range closes {
		closes[i] = d(100)
	}
	rsi, _ := rsiSeries(closes, 14)
	if rsi[14] != 50 {
		t.Errorf("rsi[14] = %v，期望 50（价格无波动）", rsi[14])
	}
}

// 涨跌交替、涨跌幅相等：种子窗口（前 period 根）里涨跌各半，RS=1，第一个有效值必须正好是 50。
//
// 只断言种子点：Wilder 平滑是非对称递推（每步只混入一个新涨跌，而不是种子窗口那种
// 对称的等量涨跌均值），所以种子点之后哪怕输入继续严格交替，RSI 也会继续围绕 50
// 小幅摆动而不会钉死在 50——这是该算法本身的正确行为，不是需要修正的误差。
func TestRSISeriesBalancedIsFiftyAtSeed(t *testing.T) {
	closes := make([]decimal.Decimal, 30)
	price := 100.0
	for i := range closes {
		closes[i] = d(price)
		if i%2 == 0 {
			price += 1
		} else {
			price -= 1
		}
	}
	rsi, from := rsiSeries(closes, 14)
	if from != 14 {
		t.Fatalf("validFrom = %d，期望 14", from)
	}
	if rsi[from] < 49.99 || rsi[from] > 50.01 {
		t.Errorf("rsi[%d] = %v，期望恰好 50（种子窗口涨跌互抵）", from, rsi[from])
	}
}

// ---- decideDirection：confluence 过滤逻辑的核心，直接用真值表验证，不依赖行情构造。----

func TestDecideDirection(t *testing.T) {
	cases := []struct {
		name                                               string
		mode                                               string
		bullishCross, bearishCross, bullishRev, bearishRev bool
		rsiNow, oversold, overbought                       float64
		wantDir                                            types.Direction
		wantTriggered                                      bool
	}{
		{"macd_cross 金叉总是触发，不看 RSI", ModeMACDCross,
			true, false, false, false, 90, 30, 70, types.DirectionLong, true},
		{"macd_cross 死叉总是触发，不看 RSI", ModeMACDCross,
			false, true, false, false, 5, 30, 70, types.DirectionShort, true},
		{"macd_cross 无穿越则不触发", ModeMACDCross,
			false, false, false, false, 50, 30, 70, types.DirectionNeutral, false},
		{"rsi_reversal 只看反转，不看 MACD", ModeRSIReversal,
			false, false, true, false, 31, 30, 70, types.DirectionLong, true},
		{"rsi_reversal 无反转则不触发", ModeRSIReversal,
			true, true, false, false, 50, 30, 70, types.DirectionNeutral, false},
		{"confluence 金叉且 RSI 未超买 → 触发多头", ModeConfluence,
			true, false, false, false, 60, 30, 70, types.DirectionLong, true},
		{"confluence 金叉但 RSI 已超买 → 过滤掉", ModeConfluence,
			true, false, false, false, 75, 30, 70, types.DirectionNeutral, false},
		{"confluence 死叉且 RSI 未超卖 → 触发空头", ModeConfluence,
			false, true, false, false, 40, 30, 70, types.DirectionShort, true},
		{"confluence 死叉但 RSI 已超卖 → 过滤掉", ModeConfluence,
			false, true, false, false, 25, 30, 70, types.DirectionNeutral, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, _, triggered := decideDirection(tc.mode,
				tc.bullishCross, tc.bearishCross, tc.bullishRev, tc.bearishRev,
				tc.rsiNow, tc.oversold, tc.overbought)
			if triggered != tc.wantTriggered {
				t.Fatalf("triggered = %v，期望 %v", triggered, tc.wantTriggered)
			}
			if dir != tc.wantDir {
				t.Errorf("dir = %s，期望 %s", dir, tc.wantDir)
			}
		})
	}
}

// ---- 端到端：通过 Evaluate 在合成行情上滚动重放，核对方向合理性与边界处理。----

// driftWalk 用带漂移的伪随机游走追加 n 根 K 线：每一步价格变动 = drift + 噪声。
//
// 纯线性趋势（synth.Builder.Trend）会让 MACD 线在几根之内就收敛到一个常数，
// 其自身的信号线也几乎瞬间跟上——差值降到浮点噪声量级后，即便后续价格反转，
// 两者也只是同步平移，不会发生真正意义上的一次性穿越。加入噪声后指标行为
// 才贴近真实行情，能测出有意义的金叉/死叉/RSI 反转。种子固定，结果可复现。
func driftWalk(b *synth.Builder, n int, start, drift, noise, volume, takerBuyRatio float64, seed int64) (*synth.Builder, float64) {
	rng := rand.New(rand.NewSource(seed))
	price := start
	for i := 0; i < n; i++ {
		next := price + drift + (rng.Float64()-0.5)*2*noise
		b.AddBar(price, next, volume, takerBuyRatio)
		price = next
	}
	return b, price
}

// replay 从 minCandles 开始逐根扩大窗口调用 Evaluate，收集所有非中性信号，
// 模拟 backtest-runner 逐根重放的用法。
func replay(t *testing.T, full types.MarketData, params map[string]any) []types.Signal {
	t.Helper()
	m := New()
	var out []types.Signal
	for k := 16; k <= len(full.Candles); k++ {
		md := types.MarketData{Symbol: full.Symbol, Timeframe: full.Timeframe, Candles: full.Candles[:k]}
		sig, err := m.Evaluate(context.Background(), md, params)
		if err != nil {
			t.Fatalf("k=%d：意外错误：%v", k, err)
		}
		if sig.Confidence != 0 && (sig.Confidence < 0.5 || sig.Confidence > 0.95) {
			t.Errorf("k=%d：置信度 %v 超出 [0.5, 0.95]", k, sig.Confidence)
		}
		if sig.Direction != types.DirectionNeutral {
			out = append(out, sig)
		}
	}
	return out
}

// V 形反转（先跌后涨）行情中，macd_cross 模式应至少捕获一次金叉，且方向为多。
func TestMACDCrossCapturesBullishReversal(t *testing.T) {
	b := synth.New("BTCUSDT", types.TF1h, base)
	b, low := driftWalk(b, 80, 200, -1.2, 1.0, 1000, 0.4, 1)
	driftWalk(b, 80, low, 1.2, 1.0, 1000, 0.6, 2)
	full := b.Build()

	sigs := replay(t, full, map[string]any{"mode": ModeMACDCross})
	found := false
	for _, s := range sigs {
		if s.Raw["event"] == eventBullishCross {
			found = true
			if s.Direction != types.DirectionLong {
				t.Errorf("bullish_cross 方向 = %s，期望 LONG", s.Direction)
			}
		}
	}
	if !found {
		t.Fatal("V 形反转行情中应至少出现一次 MACD 金叉")
	}
}

// 倒 V 形（先涨后跌）行情中，macd_cross 模式应至少捕获一次死叉，且方向为空。
func TestMACDCrossCapturesBearishReversal(t *testing.T) {
	b := synth.New("BTCUSDT", types.TF1h, base)
	b, high := driftWalk(b, 80, 100, 1.2, 1.0, 1000, 0.6, 3)
	driftWalk(b, 80, high, -1.2, 1.0, 1000, 0.4, 4)
	full := b.Build()

	sigs := replay(t, full, map[string]any{"mode": ModeMACDCross})
	found := false
	for _, s := range sigs {
		if s.Raw["event"] == eventBearishCross {
			found = true
			if s.Direction != types.DirectionShort {
				t.Errorf("bearish_cross 方向 = %s，期望 SHORT", s.Direction)
			}
		}
	}
	if !found {
		t.Fatal("倒 V 形反转行情中应至少出现一次 MACD 死叉")
	}
}

// 急跌后反弹足以把 RSI 打到超卖区再拉回，rsi_reversal 模式应捕获到多头反转。
func TestRSIReversalCapturesBullishReversal(t *testing.T) {
	b := synth.New("BTCUSDT", types.TF1h, base)
	b, low := driftWalk(b, 30, 100, -2.0, 0.8, 1000, 0.3, 5)
	driftWalk(b, 30, low, 2.0, 0.8, 1000, 0.7, 6)
	full := b.Build()

	sigs := replay(t, full, map[string]any{"mode": ModeRSIReversal})
	found := false
	for _, s := range sigs {
		if s.Raw["event"] == eventBullishReversal {
			found = true
			if s.Direction != types.DirectionLong {
				t.Errorf("bullish_reversal 方向 = %s，期望 LONG", s.Direction)
			}
		}
	}
	if !found {
		t.Fatal("急跌反弹行情中应至少出现一次 RSI 超卖反转")
	}
}

func TestConfluenceModeOnlyEmitsAgreeingSignals(t *testing.T) {
	b := synth.New("BTCUSDT", types.TF1h, base)
	b, low := driftWalk(b, 80, 200, -1.2, 1.0, 1000, 0.4, 1)
	driftWalk(b, 80, low, 1.2, 1.0, 1000, 0.6, 2)
	full := b.Build()

	sigs := replay(t, full, map[string]any{"mode": ModeConfluence})
	if len(sigs) == 0 {
		t.Fatal("confluence 模式在明显的 V 形反转行情中应至少给出一个信号")
	}
	for _, s := range sigs {
		rsiNow := s.Raw["rsi"].(float64)
		switch s.Direction {
		case types.DirectionLong:
			if rsiNow >= 70 {
				t.Errorf("confluence 多头信号 RSI=%.1f，不应处于超买区", rsiNow)
			}
		case types.DirectionShort:
			if rsiNow <= 30 {
				t.Errorf("confluence 空头信号 RSI=%.1f，不应处于超卖区", rsiNow)
			}
		}
	}
}

func TestInsufficientDataReturnsNeutral(t *testing.T) {
	b := synth.New("BTCUSDT", types.TF1h, base)
	for i := 0; i < 10; i++ {
		b.AddFlat(100, 1000)
	}
	sig, err := New().Evaluate(context.Background(), b.Build(), nil)
	if err != nil {
		t.Fatalf("数据不足不应报错，得到：%v", err)
	}
	if sig.Direction != types.DirectionNeutral {
		t.Errorf("方向 = %s，期望 NEUTRAL", sig.Direction)
	}
}

func TestInvalidParamsRejected(t *testing.T) {
	b := synth.New("BTCUSDT", types.TF1h, base)
	b.Trend(60, 100, 150, 1000, 0.5)
	full := b.Build()

	cases := []struct {
		name   string
		params map[string]any
	}{
		{"慢线周期不大于快线", map[string]any{"fast_period": 26, "slow_period": 12}},
		{"超卖阈值不小于超买阈值", map[string]any{"rsi_oversold": 80, "rsi_overbought": 70}},
		{"快线周期越界", map[string]any{"fast_period": 999}},
		{"周期非整数", map[string]any{"rsi_period": 14.5}},
		{"枚举外的模式", map[string]any{"mode": "fibonacci"}},
		{"未知参数", map[string]any{"threshold": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New().Evaluate(context.Background(), full, tc.params); err == nil {
				t.Fatalf("期望拒绝 %v，实际通过了", tc.params)
			}
		})
	}
}

func TestContextCancellationRespected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := synth.New("BTCUSDT", types.TF1h, base)
	b.Trend(60, 100, 150, 1000, 0.5)
	if _, err := New().Evaluate(ctx, b.Build(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，得到：%v", err)
	}
}
