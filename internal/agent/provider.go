package agent

import (
	"errors"
	"fmt"
	"strings"

	"tradeforge/internal/config"
	"tradeforge/pkg/types"
)

// Provider identifies a large language model vendor the translation layer can
// talk to.
//
// None of the Agent's logic (schema validation, compliance checks, the
// confirmation loop) depends on which vendor is in use — it only depends on
// the LLM interface — so adding a new provider just means registering one
// entry in providerSpecs.
//
// Except for Anthropic, which uses its native SDK, every other provider
// reuses OpenAILLM: they all expose an OpenAI-compatible Chat Completions
// interface (same request/response shape, same tool-calling mechanism), just
// with a different BaseURL and default model, so there's no need to write a
// separate client for each one. ProviderCustom is the escape hatch for
// vendors not on this list (self-hosted gateways, local models, new entrants):
// the user supplies their own API address, and anything OpenAI-protocol
// compatible works.
type Provider string

const (
	ProviderAnthropic Provider = "anthropic"
	ProviderOpenAI    Provider = "openai"
	ProviderGemini    Provider = "gemini"
	ProviderGrok      Provider = "grok"
	ProviderDeepSeek  Provider = "deepseek"
	ProviderMistral   Provider = "mistral"
	ProviderQwen      Provider = "qwen"
	ProviderGLM       Provider = "glm"
	ProviderKimi      Provider = "kimi"
	ProviderCustom    Provider = "custom"
)

// Providers lists the currently supported vendors, in the order they should
// be displayed in the UI.
var Providers = []Provider{
	ProviderAnthropic, ProviderOpenAI, ProviderGemini, ProviderGrok,
	ProviderDeepSeek, ProviderMistral, ProviderQwen, ProviderGLM, ProviderKimi,
	ProviderCustom,
}

// providerSpec registers a vendor's display name, OpenAI-compatible-layer
// address, and default model.
//
// An empty BaseURL is only valid for Anthropic, meaning it goes through the
// native SDK rather than an OpenAI-compatible layer. The addresses listed
// here reflect each vendor's public documentation as of implementation time
// and haven't been individually verified with a real key (no vendor API keys
// are available in this environment) — before wiring one up, cross-check the
// vendor's latest docs, especially the BaseURL and model name, since these
// vendors update fairly often.
type providerSpec struct {
	// labelKey is the internal/i18n catalog key for this provider's display
	// name (see catalog_agent_provider.go) -- most of these are brand names
	// that don't really "translate," but several of the non-US vendors are
	// commonly known in Chinese by a different name than their English one
	// (DeepSeek/深度求索, Alibaba Cloud/阿里云, Zhipu/智谱, Moonshot AI/月之暗面),
	// so the label is still per-language, not a hardcoded string.
	labelKey     string
	baseURL      string
	defaultModel string
}

var providerSpecs = map[Provider]providerSpec{
	ProviderAnthropic: {
		labelKey: "agent.provider.anthropic", defaultModel: DefaultModel,
	},
	ProviderOpenAI: {
		labelKey: "agent.provider.openai", baseURL: DefaultOpenAIBaseURL, defaultModel: DefaultOpenAIModel,
	},
	ProviderGemini: {
		labelKey:     "agent.provider.gemini",
		baseURL:      "https://generativelanguage.googleapis.com/v1beta/openai/",
		defaultModel: "gemini-2.5-flash",
	},
	ProviderGrok: {
		labelKey: "agent.provider.grok", baseURL: "https://api.x.ai/v1", defaultModel: "grok-4",
	},
	ProviderDeepSeek: {
		labelKey: "agent.provider.deepseek", baseURL: "https://api.deepseek.com/v1", defaultModel: "deepseek-chat",
	},
	ProviderMistral: {
		labelKey: "agent.provider.mistral", baseURL: "https://api.mistral.ai/v1", defaultModel: "mistral-large-latest",
	},
	ProviderQwen: {
		labelKey:     "agent.provider.qwen",
		baseURL:      "https://dashscope.aliyuncs.com/compatible-mode/v1",
		defaultModel: "qwen-plus",
	},
	ProviderGLM: {
		labelKey: "agent.provider.glm", baseURL: "https://open.bigmodel.cn/api/paas/v4", defaultModel: "glm-4.6",
	},
	ProviderKimi: {
		labelKey: "agent.provider.kimi", baseURL: "https://api.moonshot.cn/v1", defaultModel: "kimi-k2-0905-preview",
	},
	ProviderCustom: {
		labelKey: "agent.provider.custom",
	},
}

// Valid reports whether this is a supported provider.
func (p Provider) Valid() bool {
	_, ok := providerSpecs[p]
	return ok
}

// Label returns the provider's human-readable name, for display in the UI.
func (p Provider) Label() types.Message {
	if s, ok := providerSpecs[p]; ok {
		return types.Msg(s.labelKey)
	}
	return types.Message{Literal: string(p)}
}

// DefaultModel returns the model this provider uses when none is explicitly
// specified. ProviderCustom has no default model; callers must specify one.
func (p Provider) DefaultModel() string {
	return providerSpecs[p].defaultModel
}

// RequiresBaseURL reports whether this provider requires the caller to
// explicitly supply an API address (currently only ProviderCustom, since it
// isn't on the preset list).
func (p Provider) RequiresBaseURL() bool {
	return p == ProviderCustom
}

// NewLLM constructs the LLM implementation for the given provider.
//
// apiKey is required; when model is empty, the provider's default model is
// used (except for ProviderCustom, which must specify one explicitly);
// baseURL is only used by ProviderCustom — other providers use their own
// registered address and ignore whatever is passed in, so a mistakenly
// filled-in custom address can't accidentally override a well-known
// provider's official endpoint.
func NewLLM(provider Provider, apiKey, model, baseURL string, cfg config.AgentConfig) (LLM, error) {
	spec, ok := providerSpecs[provider]
	if !ok {
		return nil, fmt.Errorf("unsupported model provider %q", provider)
	}
	if apiKey == "" {
		return nil, ErrNoAPIKey
	}

	cfg.APIKey = apiKey
	cfg.Model = model
	if cfg.Model == "" {
		cfg.Model = spec.defaultModel
	}

	if provider == ProviderAnthropic {
		cfg.BaseURL = "https://api.anthropic.com"
		return NewAnthropicLLM(cfg)
	}

	cfg.BaseURL = spec.baseURL
	if provider.RequiresBaseURL() {
		cfg.BaseURL = strings.TrimSpace(baseURL)
		if cfg.BaseURL == "" {
			return nil, errors.New("custom provider requires an API address")
		}
		if cfg.Model == "" {
			return nil, errors.New("custom provider requires a model name")
		}
	}
	return NewOpenAILLM(cfg)
}
