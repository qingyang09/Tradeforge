package agent

import (
	"errors"
	"fmt"
	"strings"

	"tradeforge/internal/config"
)

// Provider 标识翻译层可以对接的大语言模型供应商。
//
// Agent 的全部逻辑（schema 校验、合规检查、确认循环）只依赖 LLM 接口，不关心
// 具体是哪家——新增一个供应商只需要在 providerSpecs 里登记一行。
//
// 除 Anthropic 走原生 SDK 外，其余全部复用 OpenAILLM：这些供应商都提供
// OpenAI 兼容的 Chat Completions 接口（同样的请求/响应格式、同样的 tool-calling
// 机制），只是 BaseURL、默认模型不同，没有必要为每一家单独写一份客户端代码。
// ProviderCustom 是为不在这份名单里的供应商（自建网关、本地模型、新出现的厂商）
// 留的逃生舱：用户自己填 API 地址，只要对方兼容 OpenAI 协议就能接。
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

// Providers 列出当前支持的供应商，顺序即界面展示顺序。
var Providers = []Provider{
	ProviderAnthropic, ProviderOpenAI, ProviderGemini, ProviderGrok,
	ProviderDeepSeek, ProviderMistral, ProviderQwen, ProviderGLM, ProviderKimi,
	ProviderCustom,
}

// providerSpec 登记一个供应商的展示名、OpenAI 兼容层地址、默认模型。
//
// BaseURL 为空只对 Anthropic 成立，表示走原生 SDK 而不是 OpenAI 兼容层。
// 这里列出的地址是各家官方文档在实现时的公开信息，没有用真实 key 逐一验证过
// （这个环境里没有各家的 API key）——接入前建议对照对应厂商的最新文档确认一遍，
// 尤其是 BaseURL 和模型名，这些厂商更新频率不低。
type providerSpec struct {
	label        string
	baseURL      string
	defaultModel string
}

var providerSpecs = map[Provider]providerSpec{
	ProviderAnthropic: {
		label: "Anthropic（Claude）", defaultModel: DefaultModel,
	},
	ProviderOpenAI: {
		label: "OpenAI（GPT）", baseURL: DefaultOpenAIBaseURL, defaultModel: DefaultOpenAIModel,
	},
	ProviderGemini: {
		label:        "Google（Gemini）",
		baseURL:      "https://generativelanguage.googleapis.com/v1beta/openai/",
		defaultModel: "gemini-2.5-flash",
	},
	ProviderGrok: {
		label: "xAI（Grok）", baseURL: "https://api.x.ai/v1", defaultModel: "grok-4",
	},
	ProviderDeepSeek: {
		label: "DeepSeek（深度求索）", baseURL: "https://api.deepseek.com/v1", defaultModel: "deepseek-chat",
	},
	ProviderMistral: {
		label: "Mistral AI", baseURL: "https://api.mistral.ai/v1", defaultModel: "mistral-large-latest",
	},
	ProviderQwen: {
		label:        "阿里云（通义千问）",
		baseURL:      "https://dashscope.aliyuncs.com/compatible-mode/v1",
		defaultModel: "qwen-plus",
	},
	ProviderGLM: {
		label: "智谱（GLM）", baseURL: "https://open.bigmodel.cn/api/paas/v4", defaultModel: "glm-4.6",
	},
	ProviderKimi: {
		label: "月之暗面（Kimi）", baseURL: "https://api.moonshot.cn/v1", defaultModel: "kimi-k2-0905-preview",
	},
	ProviderCustom: {
		label: "自定义（OpenAI 兼容接口）",
	},
}

// Valid 报告是否为已支持的供应商。
func (p Provider) Valid() bool {
	_, ok := providerSpecs[p]
	return ok
}

// Label 返回供应商的人类可读名称，供界面展示。
func (p Provider) Label() string {
	if s, ok := providerSpecs[p]; ok {
		return s.label
	}
	return string(p)
}

// DefaultModel 返回该供应商在未显式指定模型时使用的默认模型。
// ProviderCustom 没有默认模型，调用方必须显式指定。
func (p Provider) DefaultModel() string {
	return providerSpecs[p].defaultModel
}

// RequiresBaseURL 报告该供应商是否必须由调用方显式提供 API 地址
// （目前只有 ProviderCustom，因为它不在预置名单里）。
func (p Provider) RequiresBaseURL() bool {
	return p == ProviderCustom
}

// NewLLM 按供应商构造对应的 LLM 实现。
//
// apiKey 必填；model 为空时使用该供应商的默认模型（ProviderCustom 除外，
// 必须显式指定）；baseURL 只有 ProviderCustom 会用到，其余供应商用各自登记的地址，
// 传了也会被忽略——不能让一次误填的自定义地址意外顶替掉某个知名供应商的官方端点。
func NewLLM(provider Provider, apiKey, model, baseURL string, cfg config.AgentConfig) (LLM, error) {
	spec, ok := providerSpecs[provider]
	if !ok {
		return nil, fmt.Errorf("不支持的模型供应商 %q", provider)
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
			return nil, errors.New("自定义供应商必须填写 API 地址")
		}
		if cfg.Model == "" {
			return nil, errors.New("自定义供应商必须填写模型名称")
		}
	}
	return NewOpenAILLM(cfg)
}
