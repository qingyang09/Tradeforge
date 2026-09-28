package agent

import (
	"context"
	"strings"
	"testing"

	"tradeforge/pkg/types"
)

func TestMentionedTimeframesDetectsMultiple(t *testing.T) {
	got := mentionedTimeframes("1小时判断盘整假突破，15分钟判断放量下跌")
	if !got[types.TF1h] || !got[types.TF15m] {
		t.Errorf("应识别出 1h 和 15m，实际 %v", got)
	}
	if len(got) != 2 {
		t.Errorf("不应识别出多余的周期，实际 %v", got)
	}
}

func TestMentionedTimeframesSingleMention(t *testing.T) {
	got := mentionedTimeframes("BTC 一小时线，放量做多")
	if len(got) != 1 || !got[types.TF1h] {
		t.Errorf("应只识别出 1h，实际 %v", got)
	}
}

func TestCheckTimeframeCoverageSingleMentionAlwaysPasses(t *testing.T) {
	cfg := types.StrategyConfig{Timeframe: types.TF1h}
	if err := checkTimeframeCoverage(cfg, "BTC 一小时线放量做多"); err != nil {
		t.Errorf("只提到一个周期时不应该拒绝：%v", err)
	}
}

func TestCheckTimeframeCoverageFullyCoveredPasses(t *testing.T) {
	cfg := types.StrategyConfig{
		Timeframe: types.TF15m,
		Modules:   []types.ModuleConfig{{Module: "fakeout", Timeframe: types.TF1h}, {Module: "volume_breakout"}},
	}
	err := checkTimeframeCoverage(cfg, "1小时判断盘整假突破，15分钟判断放量下跌")
	if err != nil {
		t.Errorf("提到的周期都用到了，不应该拒绝：%v", err)
	}
}

func TestCheckTimeframeCoverageDetectsDroppedTimeframe(t *testing.T) {
	// 用户提到了 1h 和 15m，但配置里所有模块都跟着触发周期 15m 走——1h 被漏翻了。
	cfg := types.StrategyConfig{
		Timeframe: types.TF15m,
		Modules:   []types.ModuleConfig{{Module: "fakeout"}, {Module: "volume_breakout"}},
	}
	err := checkTimeframeCoverage(cfg, "1小时判断盘整假突破，15分钟判断放量下跌")
	if err == nil {
		t.Fatal("1h 被漏翻应该被拒绝")
	}
	if !strings.Contains(err.Error(), "1h") {
		t.Errorf("错误信息应点名漏掉的周期，实际：%v", err)
	}
}

// 端到端：模型第一次输出把 1h 判断漏翻成跟随 15m 触发周期，Translate 应该自动
// 拿着具体的错误信息重试，第二次模型才把 1h 正确填上——验证的是重试循环真的会
// 因为这道检查而重新生成，不只是单元测试孤立的检查函数本身。
func TestTranslateRetriesWhenModuleTimeframeIsDropped(t *testing.T) {
	utterance := "BTC，1小时级别判断盘整区假突破，15分钟级别判断放量下跌就入场做空"

	firstBad := reply(OutcomeConfig, "标的 BTCUSDT，15 分钟周期，假突破与放量下跌都按 15 分钟判断。", map[string]any{
		"name": "多周期", "symbol": "BTCUSDT", "timeframe": "15m", "combine": "ALL",
		"modules": []any{
			map[string]any{"module": "fakeout", "params": map[string]any{}},
			map[string]any{"module": "volume_breakout", "params": map[string]any{}},
		},
		"risk": risk("1000"),
	})
	secondGood := reply(OutcomeConfig, "标的 BTCUSDT，触发周期 15 分钟；假突破按 1 小时判断，放量下跌按 15 分钟判断。", map[string]any{
		"name": "多周期", "symbol": "BTCUSDT", "timeframe": "15m", "combine": "ALL",
		"modules": []any{
			map[string]any{"module": "fakeout", "timeframe": "1h", "params": map[string]any{}},
			map[string]any{"module": "volume_breakout", "params": map[string]any{}},
		},
		"risk": risk("1000"),
	})

	a, stub := newAgent(firstBad, secondGood)
	p, err := a.Translate(context.Background(), utterance, nil)
	if err != nil {
		t.Fatalf("翻译失败：%v", err)
	}
	if len(stub.Calls) != 2 {
		t.Fatalf("应该重试一次（共调用 2 次），实际调用 %d 次", len(stub.Calls))
	}
	if p.Attempts != 2 {
		t.Errorf("Attempts = %d，期望 2", p.Attempts)
	}
	var fakeoutTF types.Timeframe
	for _, m := range p.Config.Modules {
		if m.Module == "fakeout" {
			fakeoutTF = m.Timeframe
		}
	}
	if fakeoutTF != types.TF1h {
		t.Errorf("重试后 fakeout.timeframe = %q，期望 1h", fakeoutTF)
	}
}
