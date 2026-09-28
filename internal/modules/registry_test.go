package modules

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

func TestDefaultRegistryContainsAllModules(t *testing.T) {
	r := NewDefaultRegistry()
	want := []string{"cvd_orderflow", "fakeout", "macd_rsi", "news_sentiment", "poc", "support_resistance", "volume_breakout"}
	got := r.Names()
	if len(got) != len(want) {
		t.Fatalf("已注册模块 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names()[%d] = %q，期望 %q（应按字典序稳定输出）", i, got[i], want[i])
		}
	}
}

// 引用不存在的模块必须报错并列出可选项，这样 Agent 幻觉出的模块名在配置校验期就会暴露。
func TestGetUnknownModuleListsAvailable(t *testing.T) {
	r := NewDefaultRegistry()
	_, err := r.Get("moving_average_cross")
	if err == nil {
		t.Fatal("期望未知模块报错")
	}
	var ume *UnknownModuleError
	if !asUnknown(err, &ume) {
		t.Fatalf("期望 *UnknownModuleError，得到 %T", err)
	}
	if len(ume.Available) == 0 {
		t.Error("错误里必须带上可选模块清单")
	}
}

func asUnknown(err error, target **UnknownModuleError) bool {
	u, ok := err.(*UnknownModuleError)
	if ok {
		*target = u
	}
	return ok
}

// 每个模块的参数规格都必须自洽：有默认值、默认值在范围内、且能通过自己的校验。
func TestEveryModuleHasUsableDefaults(t *testing.T) {
	for _, m := range NewDefaultRegistry().All() {
		t.Run(m.Name(), func(t *testing.T) {
			specs := m.RequiredParams()
			if len(specs) == 0 {
				t.Fatal("模块没有声明任何参数")
			}
			for _, s := range specs {
				if s.Description == "" {
					t.Errorf("参数 %q 缺少说明，Agent 无法据此理解它的含义", s.Name)
				}
				if !s.Required && s.Default == nil {
					t.Errorf("参数 %q 既非必填也没有默认值", s.Name)
				}
			}
			// 空参数应当被默认值填满并通过校验。
			if _, err := ResolveParams(m, map[string]any{}); err != nil {
				t.Errorf("默认参数未能通过自身校验：%v", err)
			}
		})
	}
}

// 所有模块面对空行情都必须返回中性信号而不是 panic 或报错。
func TestEveryModuleHandlesEmptyData(t *testing.T) {
	for _, m := range NewDefaultRegistry().All() {
		t.Run(m.Name(), func(t *testing.T) {
			sig, err := m.Evaluate(context.Background(), types.MarketData{Symbol: "BTCUSDT"}, nil)
			if err != nil {
				t.Fatalf("空行情不应报错，得到：%v", err)
			}
			if sig.Direction != types.DirectionNeutral {
				t.Errorf("方向 = %s，期望 NEUTRAL", sig.Direction)
			}
			if sig.Module != m.Name() {
				t.Errorf("Signal.Module = %q，期望 %q", sig.Module, m.Name())
			}
		})
	}
}

// 即便没有信号，中性输出也必须带上当时的参考价与时间戳：
// 审计和回放需要知道"这一根 K 线上模块看到了什么"，price=0 会污染下游记录。
func TestNeutralSignalsStillCarryPriceAndTime(t *testing.T) {
	md := flatMarketData(300, 100, 1000)
	for _, m := range NewDefaultRegistry().All() {
		t.Run(m.Name(), func(t *testing.T) {
			sig, err := m.Evaluate(context.Background(), md, nil)
			if err != nil {
				t.Fatalf("意外错误：%v", err)
			}
			if sig.Direction != types.DirectionNeutral {
				t.Skipf("该行情下模块给出了 %s 信号，本用例只覆盖中性输出", sig.Direction)
			}
			if !sig.Price.IsPositive() {
				t.Errorf("中性信号的 Price = %s，期望带上当时的收盘价", sig.Price)
			}
			if sig.Timestamp.IsZero() {
				t.Error("中性信号的 Timestamp 不能为零值")
			}
		})
	}
}

// flatMarketData 构造一段完全平静的行情：价格不动、量能均匀、买卖对半，
// 任何模块在这上面都不该产出方向性信号。
func flatMarketData(n int, price, volume float64) types.MarketData {
	md := types.MarketData{Symbol: "BTCUSDT", Timeframe: types.TF1h}
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	p := decimal.NewFromFloat(price)
	v := decimal.NewFromFloat(volume)
	for i := 0; i < n; i++ {
		openTime := t0.Add(time.Duration(i) * time.Hour)
		md.Candles = append(md.Candles, types.Candle{
			OpenTime:       openTime,
			CloseTime:      openTime.Add(time.Hour),
			Open:           p,
			High:           p,
			Low:            p,
			Close:          p,
			Volume:         v,
			TakerBuyVolume: v.Div(decimal.NewFromInt(2)),
		})
	}
	return md
}

func TestRegisterDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("重复注册同名模块应当 panic")
		}
	}()
	r := NewDefaultRegistry()
	r.Register(NewDefaultRegistry().All()[0])
}
