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
			t.Errorf("Provider(%q).Valid() = %v, want %v", tc.p, got, tc.want)
		}
	}
}

// Every registered provider must have a display name and a default model
// (ProviderCustom has no default model as the one exception, since it
// requires the caller to specify one explicitly) — otherwise the UI would
// show an option with no default value to select.
func TestEveryProviderHasLabelAndDefaultModelExceptCustom(t *testing.T) {
	for _, p := range Providers {
		t.Run(string(p), func(t *testing.T) {
			if p.Label() == "" || p.Label() == string(p) {
				t.Errorf("Provider(%q) missing display name", p)
			}
			if p == ProviderCustom {
				if p.DefaultModel() != "" {
					t.Errorf("ProviderCustom should have no default model, got %q", p.DefaultModel())
				}
				return
			}
			if p.DefaultModel() == "" {
				t.Errorf("Provider(%q) missing default model", p)
			}
		})
	}
}

func TestNewLLMRejectsEmptyAPIKey(t *testing.T) {
	for _, p := range Providers {
		t.Run(string(p), func(t *testing.T) {
			if _, err := NewLLM(p, "", "", "", config.AgentConfig{}); !errors.Is(err, ErrNoAPIKey) {
				t.Errorf("empty key should return ErrNoAPIKey, got %v", err)
			}
		})
	}
}

func TestNewLLMRejectsUnknownProvider(t *testing.T) {
	if _, err := NewLLM(Provider("llama"), "key", "", "", config.AgentConfig{}); err == nil {
		t.Fatal("unknown provider should be rejected")
	}
}

func TestNewLLMDispatchesToCorrectProvider(t *testing.T) {
	anthropicLLM, err := NewLLM(ProviderAnthropic, "sk-ant-test", "", "", config.AgentConfig{})
	if err != nil {
		t.Fatalf("failed to construct Anthropic LLM: %v", err)
	}
	if _, ok := anthropicLLM.(*AnthropicLLM); !ok {
		t.Errorf("ProviderAnthropic should produce *AnthropicLLM, got %T", anthropicLLM)
	}

	// Every other registered provider should go through the OpenAI-compatible
	// layer and produce the same *OpenAILLM type.
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
				t.Fatalf("construction failed: %v", err)
			}
			if _, ok := llm.(*OpenAILLM); !ok {
				t.Errorf("should produce *OpenAILLM, got %T", llm)
			}
		})
	}
}

func TestNewLLMUsesProviderDefaultModelWhenUnspecified(t *testing.T) {
	for _, p := range Providers {
		if p == ProviderCustom {
			continue // custom has no default model, must be specified explicitly; tested separately
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
				t.Errorf("model = %q, want default %q", gotModel, p.DefaultModel())
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
		t.Errorf("model = %q, want gpt-4o-mini", o.model)
	}
}

// Providers must not bleed into each other: passing an Anthropic BaseURL to
// OpenAI must not be carried over — this field gets reused in the scenario
// where "the settings page reuses the same config.AgentConfig when switching
// providers", and if it isn't cleared, switching to OpenAI would send
// requests to Anthropic's address.
func TestNewLLMDoesNotLeakAnthropicBaseURLIntoOpenAI(t *testing.T) {
	llm, err := NewLLM(ProviderOpenAI, "sk-test", "", "", config.AgentConfig{BaseURL: "https://api.anthropic.com"})
	if err != nil {
		t.Fatal(err)
	}
	o := llm.(*OpenAILLM)
	if o.baseURL != DefaultOpenAIBaseURL {
		t.Errorf("baseURL = %q, want fallback to OpenAI default %q, not carried over from Anthropic", o.baseURL, DefaultOpenAIBaseURL)
	}
}

func TestNewLLMCustomProviderRequiresBaseURLAndModel(t *testing.T) {
	if _, err := NewLLM(ProviderCustom, "key", "some-model", "", config.AgentConfig{}); err == nil {
		t.Error("custom provider missing API address should error")
	}
	if _, err := NewLLM(ProviderCustom, "key", "", "https://gateway.example.com/v1", config.AgentConfig{}); err == nil {
		t.Error("custom provider missing model name should error")
	}
	llm, err := NewLLM(ProviderCustom, "key", "some-model", "https://gateway.example.com/v1", config.AgentConfig{})
	if err != nil {
		t.Fatalf("should not error when all params are given: %v", err)
	}
	o := llm.(*OpenAILLM)
	if o.baseURL != "https://gateway.example.com/v1" {
		t.Errorf("baseURL = %q, want the user-supplied address", o.baseURL)
	}
	if o.model != "some-model" {
		t.Errorf("model = %q, want the user-supplied model", o.model)
	}
}

// Even when the custom provider is given a baseURL, other preset providers
// must not be contaminated by it.
func TestNewLLMCustomBaseURLDoesNotLeakToKnownProviders(t *testing.T) {
	llm, err := NewLLM(ProviderDeepSeek, "key", "", "https://should-be-ignored.example.com", config.AgentConfig{})
	if err != nil {
		t.Fatal(err)
	}
	o := llm.(*OpenAILLM)
	if o.baseURL != providerSpecs[ProviderDeepSeek].baseURL {
		t.Errorf("baseURL = %q, want DeepSeek's own registered address %q, should not be overridden by the passed-in baseURL",
			o.baseURL, providerSpecs[ProviderDeepSeek].baseURL)
	}
}
