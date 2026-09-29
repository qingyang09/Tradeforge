package agent

import (
	"context"
	"strings"
	"testing"

	"tradeforge/internal/i18n"
	"tradeforge/pkg/types"
)

func TestMentionedTimeframesDetectsMultiple(t *testing.T) {
	got := mentionedTimeframes("1小时判断盘整假突破，15分钟判断放量下跌")
	if !got[types.TF1h] || !got[types.TF15m] {
		t.Errorf("should detect 1h and 15m, got %v", got)
	}
	if len(got) != 2 {
		t.Errorf("should not detect any extra timeframes, got %v", got)
	}
}

func TestMentionedTimeframesSingleMention(t *testing.T) {
	got := mentionedTimeframes("BTC 一小时线，放量做多")
	if len(got) != 1 || !got[types.TF1h] {
		t.Errorf("should only detect 1h, got %v", got)
	}
}

func TestCheckTimeframeCoverageSingleMentionAlwaysPasses(t *testing.T) {
	cfg := types.StrategyConfig{Timeframe: types.TF1h}
	if err := checkTimeframeCoverage(cfg, "BTC 一小时线放量做多"); err != nil {
		t.Errorf("should not reject when only one timeframe is mentioned: %v", err)
	}
}

func TestCheckTimeframeCoverageFullyCoveredPasses(t *testing.T) {
	cfg := types.StrategyConfig{
		Timeframe: types.TF15m,
		Modules:   []types.ModuleConfig{{Module: "fakeout", Timeframe: types.TF1h}, {Module: "volume_breakout"}},
	}
	err := checkTimeframeCoverage(cfg, "1小时判断盘整假突破，15分钟判断放量下跌")
	if err != nil {
		t.Errorf("should not reject when every mentioned timeframe is used: %v", err)
	}
}

func TestCheckTimeframeCoverageDetectsDroppedTimeframe(t *testing.T) {
	// The user mentioned 1h and 15m, but every module in the config follows
	// the 15m trigger timeframe — 1h got dropped in translation.
	cfg := types.StrategyConfig{
		Timeframe: types.TF15m,
		Modules:   []types.ModuleConfig{{Module: "fakeout"}, {Module: "volume_breakout"}},
	}
	err := checkTimeframeCoverage(cfg, "1小时判断盘整假突破，15分钟判断放量下跌")
	if err == nil {
		t.Fatal("dropping 1h in translation should be rejected")
	}
	if !strings.Contains(err.Error(), "1h") {
		t.Errorf("error message should name the missing timeframe, got: %v", err)
	}
}

// End-to-end: the model's first output drops the 1h judgment by having it
// follow the 15m trigger timeframe; Translate should automatically retry
// with the specific error message, and the second model call correctly fills
// in 1h — this verifies the retry loop actually regenerates because of this
// check, not just the isolated check function in a unit test.
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
	p, err := a.Translate(context.Background(), utterance, nil, i18n.LangZH)
	if err != nil {
		t.Fatalf("translate failed: %v", err)
	}
	if len(stub.Calls) != 2 {
		t.Fatalf("should retry once (2 calls total), got %d calls", len(stub.Calls))
	}
	if p.Attempts != 2 {
		t.Errorf("Attempts = %d, want 2", p.Attempts)
	}
	var fakeoutTF types.Timeframe
	for _, m := range p.Config.Modules {
		if m.Module == "fakeout" {
			fakeoutTF = m.Timeframe
		}
	}
	if fakeoutTF != types.TF1h {
		t.Errorf("fakeout.timeframe after retry = %q, want 1h", fakeoutTF)
	}
}
