package agent

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"tradeforge/internal/modules"
	"tradeforge/pkg/types"
)

func sampleConfig() types.StrategyConfig {
	return types.StrategyConfig{
		Name: "BTC 放量策略", Symbol: "BTCUSDT", Timeframe: types.TF1h, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{
			{Module: "volume_breakout", Params: map[string]any{"multiplier": 3.0}},
		},
		Risk: types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
	}
}

func TestDescribeReturnsRestatement(t *testing.T) {
	stub := &StubLLM{Responses: []string{MustJSON(map[string]any{
		"restatement": "标的 BTCUSDT，1 小时周期，成交量突破均量 3 倍时触发，单笔最大仓位 1000。",
	})}}
	a := New(stub, modules.NewDefaultRegistry(), 2)

	got, err := a.Describe(context.Background(), sampleConfig())
	if err != nil {
		t.Fatalf("Describe() 失败：%v", err)
	}
	if got == "" {
		t.Error("复述内容不应为空")
	}
	if len(stub.Calls) != 1 {
		t.Fatalf("应只调用一次模型，实际 %d 次", len(stub.Calls))
	}
}

func TestDescribeRejectsAdvisoryLanguage(t *testing.T) {
	stub := &StubLLM{Responses: []string{MustJSON(map[string]any{
		"restatement": "这个策略不错，建议按当前参数执行。",
	})}}
	a := New(stub, modules.NewDefaultRegistry(), 2)

	_, err := a.Describe(context.Background(), sampleConfig())
	if err == nil {
		t.Fatal("包含投资建议措辞的复述应被拒绝")
	}
	var complianceErr *ComplianceError
	if !asComplianceError(err, &complianceErr) {
		t.Errorf("错误类型应为 ComplianceError，实际：%v", err)
	}
}

func TestDescribeRejectsEmptyRestatement(t *testing.T) {
	stub := &StubLLM{Responses: []string{MustJSON(map[string]any{"restatement": "  "})}}
	a := New(stub, modules.NewDefaultRegistry(), 2)

	_, err := a.Describe(context.Background(), sampleConfig())
	if err == nil {
		t.Fatal("空复述应被拒绝")
	}
}

func TestDescribeRejectsMalformedJSON(t *testing.T) {
	stub := &StubLLM{Responses: []string{"不是 JSON"}}
	a := New(stub, modules.NewDefaultRegistry(), 2)

	_, err := a.Describe(context.Background(), sampleConfig())
	if err == nil {
		t.Fatal("非法 JSON 输出应被拒绝")
	}
}

func TestDescribeSurfacesLLMError(t *testing.T) {
	stub := &StubLLM{Err: errStub}
	a := New(stub, modules.NewDefaultRegistry(), 2)

	_, err := a.Describe(context.Background(), sampleConfig())
	if err == nil {
		t.Fatal("模型调用失败应向上传播")
	}
}

func asComplianceError(err error, target **ComplianceError) bool {
	ce, ok := err.(*ComplianceError)
	if ok {
		*target = ce
	}
	return ok
}

var errStub = &stubErr{"模拟的模型调用失败"}

type stubErr struct{ msg string }

func (e *stubErr) Error() string { return e.msg }
