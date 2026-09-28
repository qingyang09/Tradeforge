package agent

import (
	"errors"
	"testing"

	"tradeforge/internal/config"
)

func TestProviderValid(t *testing.T) {
	cases := []struct {
		p    Provider
		want bool
	}{
		{ProviderAnthropic, true},
		{ProviderOpenAI, true},
		{ProviderGemini, true},
		{ProviderGrok, true},
		{ProviderDeepSeek, true},
		{ProviderMistral, true},
		{ProviderQwen, true},
		{ProviderGLM, true},
		{ProviderKimi, true},
		{ProviderCustom, true},
		{Provider("llama"), false},
		{Provider(""), false},
	}
	for _, tc := range cases {
		if got := tc.p.Valid(); got != tc.want {
			t.Errorf("Provider(%q).Valid() = %v，期望 %v", tc.p, got, tc.want)
		}
	}
}

// 每个登记过的供应商都必须有展示名和默认模型（ProviderCustom 没有默认模型是例外，
// 它要求调用方显式指定），否则界面上会出现一个选不出默认值的选项。
func TestEveryProviderHasLabelAndDefaultModelExceptCustom(t *testing.T) {
	for _, p := range Providers {
		t.Run(string(p), func(t *testing.T) {
			if p.Label() == "" || p.Label() == string(p) {
				t.Errorf("Provider(%q) 缺少展示名", p)
			}
			if p == ProviderCustom {
				if p.DefaultModel() != "" {
					t.Errorf("ProviderCustom 不该有默认模型，实际 %q", p.DefaultModel())
				}
				return
			}
			if p.DefaultModel() == "" {
				t.Errorf("Provider(%q) 缺少默认模型", p)
			}
		})
	}
}

func TestNewLLMRejectsEmptyAPIKey(t *testing.T) {
	for _, p := range Providers {
		t.Run(string(p), func(t *testing.T) {
			if _, err := NewLLM(p, "", "", "", config.AgentConfig{}); !errors.Is(err, ErrNoAPIKey) {
				t.Errorf("空 key 应返回 ErrNoAPIKey，得到 %v", err)
			}
		})
	}
}

func TestNewLLMRejectsUnknownProvider(t *testing.T) {
	if _, err := NewLLM(Provider("llama"), "key", "", "", config.AgentConfig{}); err == nil {
		t.Fatal("未知供应商应被拒绝")
	}
}

func TestNewLLMDispatchesToCorrectProvider(t *testing.T) {
	anthropicLLM, err := NewLLM(ProviderAnthropic, "sk-ant-test", "", "", config.AgentConfig{})
	if err != nil {
		t.Fatalf("构造 Anthropic LLM 失败：%v", err)
	}
	if _, ok := anthropicLLM.(*AnthropicLLM); !ok {
		t.Errorf("ProviderAnthropic 应产出 *AnthropicLLM，实际 %T", anthropicLLM)
	}

	// 其余全部登记过的供应商都应该走 OpenAI 兼容层，产出同一个 *OpenAILLM 类型。
	for _, p := range Providers {
		if p == ProviderAnthropic {
			continue
		}
		t.Run(string(p), func(t *testing.T) {
			baseURL, model := "", ""
			if p.RequiresBaseURL() {
				baseURL, model = "https://gateway.example.com/v1", "some-model"
			}
			llm, err := NewLLM(p, "test-key", model, baseURL, config.AgentConfig{})
			if err != nil {
				t.Fatalf("构造失败：%v", err)
			}
			if _, ok := llm.(*OpenAILLM); !ok {
				t.Errorf("应产出 *OpenAILLM，实际 %T", llm)
			}
		})
	}
}

func TestNewLLMUsesProviderDefaultModelWhenUnspecified(t *testing.T) {
	for _, p := range Providers {
		if p == ProviderCustom {
			continue // 自定义没有默认模型，必须显式指定，另外单独测
		}
		t.Run(string(p), func(t *testing.T) {
			llm, err := NewLLM(p, "test-key", "", "", config.AgentConfig{})
			if err != nil {
				t.Fatal(err)
			}
			var gotModel string
			switch v := llm.(type) {
			case *AnthropicLLM:
				gotModel = v.model
			case *OpenAILLM:
				gotModel = v.model
			}
			if gotModel != p.DefaultModel() {
				t.Errorf("model = %q，期望默认值 %q", gotModel, p.DefaultModel())
			}
		})
	}
}

func TestNewLLMHonorsExplicitModel(t *testing.T) {
	llm, err := NewLLM(ProviderOpenAI, "sk-test", "gpt-4o-mini", "", config.AgentConfig{})
	if err != nil {
		t.Fatal(err)
	}
	o := llm.(*OpenAILLM)
	if o.model != "gpt-4o-mini" {
		t.Errorf("model = %q，期望 gpt-4o-mini", o.model)
	}
}

// Provider 之间不能串味：给 OpenAI 传一个 Anthropic 的 BaseURL 不该被沿用——
// 这个字段的复用场景是"设置页面切换供应商时复用同一份 config.AgentConfig"，
// 如果不清空，切到 OpenAI 时请求会打到 Anthropic 的地址上。
func TestNewLLMDoesNotLeakAnthropicBaseURLIntoOpenAI(t *testing.T) {
	llm, err := NewLLM(ProviderOpenAI, "sk-test", "", "", config.AgentConfig{BaseURL: "https://api.anthropic.com"})
	if err != nil {
		t.Fatal(err)
	}
	o := llm.(*OpenAILLM)
	if o.baseURL != DefaultOpenAIBaseURL {
		t.Errorf("baseURL = %q，期望回落到 OpenAI 默认地址 %q，而不是沿用 Anthropic 的", o.baseURL, DefaultOpenAIBaseURL)
	}
}

func TestNewLLMCustomProviderRequiresBaseURLAndModel(t *testing.T) {
	if _, err := NewLLM(ProviderCustom, "key", "some-model", "", config.AgentConfig{}); err == nil {
		t.Error("自定义供应商缺 API 地址应报错")
	}
	if _, err := NewLLM(ProviderCustom, "key", "", "https://gateway.example.com/v1", config.AgentConfig{}); err == nil {
		t.Error("自定义供应商缺模型名应报错")
	}
	llm, err := NewLLM(ProviderCustom, "key", "some-model", "https://gateway.example.com/v1", config.AgentConfig{})
	if err != nil {
		t.Fatalf("参数齐全时不该报错：%v", err)
	}
	o := llm.(*OpenAILLM)
	if o.baseURL != "https://gateway.example.com/v1" {
		t.Errorf("baseURL = %q，期望使用用户填写的地址", o.baseURL)
	}
	if o.model != "some-model" {
		t.Errorf("model = %q，期望使用用户填写的模型", o.model)
	}
}

// 自定义供应商即便传了 baseURL，其它预置供应商也不该被它污染。
func TestNewLLMCustomBaseURLDoesNotLeakToKnownProviders(t *testing.T) {
	llm, err := NewLLM(ProviderDeepSeek, "key", "", "https://should-be-ignored.example.com", config.AgentConfig{})
	if err != nil {
		t.Fatal(err)
	}
	o := llm.(*OpenAILLM)
	if o.baseURL != providerSpecs[ProviderDeepSeek].baseURL {
		t.Errorf("baseURL = %q，期望 DeepSeek 自己登记的地址 %q，不该被传入的 baseURL 参数覆盖",
			o.baseURL, providerSpecs[ProviderDeepSeek].baseURL)
	}
}
