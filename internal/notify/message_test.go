package notify

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// TestBuildMessageNeverUsesRecommendationWording 是一条 grep 式的机械检查——把
// claude.md"绝不生成建议买什么"这条合规红线，从纯人工审查变成会挂红的检查，跟
// internal/webui/security_guard_test.go"策略→机械检查"是同一个思路。矩阵覆盖
// 方向×原因×模式，任何一种组合的 Title/Body 都不能出现"建议"/"推荐"。
func TestBuildMessageNeverUsesRecommendationWording(t *testing.T) {
	forbidden := []string{"建议", "推荐"}

	// 这里的 Reason 都是事实性描述（跟引擎实际产出的 Reason 文案风格一致，见
	// internal/modules 各模块的 Reason 措辞）——这条测试验证的是 BuildMessage 自己
	// 生成的固定文案骨架不额外引入"建议"/"推荐"，不是验证"引擎产出的 Reason 本身
	// 合规"（那是 internal/agent 系统提示词那条红线已经在管的事，见 claude.md）。
	directions := []types.Direction{types.DirectionLong, types.DirectionShort, types.DirectionNeutral}
	reasons := []string{"支撑位放量突破", "成交量为近期均量的 3 倍", "CVD 失衡达到阈值"}
	modes := []Mode{ModePreview, ModeLive}
	scores := []float64{0.1, 0.5, 0.95}

	sc := types.StrategyConfig{Name: "测试策略", Symbol: "BTCUSDT"}

	for _, dir := range directions {
		for _, reason := range reasons {
			for _, mode := range modes {
				for _, score := range scores {
					d := types.Decision{
						StrategyID: "s1", Symbol: "BTCUSDT", Direction: dir, Score: score,
						Triggered: true, Reason: reason, Price: decimal.NewFromInt(50000),
						Timestamp: time.Now(),
					}
					msg := BuildMessage(sc, d, mode)
					// complianceDisclaimer 本身必然包含"建议"二字（"不构成投资建议"这个
					// 否定短语的一部分，是唯一允许出现该词的地方）——检查前先把它去掉，
					// 只检查 Body 里这句固定声明之外的部分。
					bodyWithoutDisclaimer := strings.Replace(msg.Body, complianceDisclaimer, "", 1)
					for _, bad := range forbidden {
						if strings.Contains(msg.Title, bad) {
							t.Errorf("Title 不应包含 %q，实际：%q（dir=%s mode=%s）", bad, msg.Title, dir, mode)
						}
						if strings.Contains(bodyWithoutDisclaimer, bad) {
							t.Errorf("Body（合规声明之外的部分）不应包含 %q，实际：%q（dir=%s mode=%s）",
								bad, bodyWithoutDisclaimer, dir, mode)
						}
					}
					if !strings.Contains(msg.Body, complianceDisclaimer) {
						t.Errorf("Body 应该包含固定的合规声明，实际：%q", msg.Body)
					}
				}
			}
		}
	}
}

func TestBuildMessageLabelsModeCorrectly(t *testing.T) {
	sc := types.StrategyConfig{Name: "测试策略", Symbol: "BTCUSDT"}
	d := types.Decision{
		StrategyID: "s1", Symbol: "BTCUSDT", Direction: types.DirectionLong, Score: 0.8,
		Triggered: true, Reason: "测试原因", Price: decimal.NewFromInt(100), Timestamp: time.Now(),
	}

	preview := BuildMessage(sc, d, ModePreview)
	if !strings.Contains(preview.Title, "模拟盘预览") {
		t.Errorf("PAPER_TRADING 模式的 Title 应该标注「模拟盘预览」，实际：%q", preview.Title)
	}

	live := BuildMessage(sc, d, ModeLive)
	if !strings.Contains(live.Title, "实盘") {
		t.Errorf("LIVE 模式的 Title 应该标注「实盘」，实际：%q", live.Title)
	}
	if strings.Contains(live.Title, "模拟盘预览") {
		t.Errorf("LIVE 模式的 Title 不应该出现「模拟盘预览」，实际：%q", live.Title)
	}
}

func TestToWebhookPayloadCarriesDecisionFields(t *testing.T) {
	sc := types.StrategyConfig{Name: "测试策略", Symbol: "BTCUSDT"}
	now := time.Now()
	d := types.Decision{
		StrategyID: "s1", Symbol: "BTCUSDT", Direction: types.DirectionShort, Score: 0.7,
		Triggered: true, Reason: "测试原因", Price: decimal.NewFromInt(42000), Timestamp: now,
	}
	msg := BuildMessage(sc, d, ModeLive)
	payload := msg.ToWebhookPayload(d, ModeLive)

	if payload.StrategyID != "s1" || payload.Symbol != "BTCUSDT" || payload.Direction != "SHORT" {
		t.Errorf("载荷字段不符：%+v", payload)
	}
	if payload.Mode != ModeLive {
		t.Errorf("Mode = %q，期望 %q", payload.Mode, ModeLive)
	}
	if !payload.Timestamp.Equal(now) {
		t.Errorf("Timestamp = %v，期望 %v", payload.Timestamp, now)
	}
}
