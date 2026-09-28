package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"

	"tradeforge/internal/config"
)

// DefaultOpenAIModel 是 OpenAI 供应商在未显式指定模型时使用的默认模型。
const DefaultOpenAIModel = "gpt-4o"

// DefaultOpenAIBaseURL 是 OpenAI 官方 API 地址。
const DefaultOpenAIBaseURL = "https://api.openai.com/v1"

// OpenAILLM 用 OpenAI 的 Chat Completions API 实现 LLM。
//
// 跟 AnthropicLLM 结构上完全对称：同样强制走单一工具调用（而不是"请你输出 JSON"）
// 拿到结构化输出，同样的合规要求——绝不允许自由文本直接进入执行链路——在这里
// 同样必须守住，不能因为换了供应商就降低约束强度。
type OpenAILLM struct {
	client    openai.Client
	model     string
	baseURL   string
	maxTokens int64
}

// NewOpenAILLM 按配置构造客户端。
func NewOpenAILLM(cfg config.AgentConfig) (*OpenAILLM, error) {
	if cfg.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultOpenAIBaseURL
	}
	opts = append(opts, option.WithBaseURL(baseURL))

	model := cfg.Model
	if model == "" {
		model = DefaultOpenAIModel
	}
	return &OpenAILLM{
		client:    openai.NewClient(opts...),
		model:     model,
		baseURL:   baseURL,
		maxTokens: 8192,
	}, nil
}

// Complete 实现 LLM。
func (a *OpenAILLM) Complete(ctx context.Context, system string, schema Schema, turns []Turn) (string, error) {
	if len(turns) == 0 {
		return "", errors.New("对话轮次为空")
	}

	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(turns)+1)
	msgs = append(msgs, openai.SystemMessage(system))
	for _, t := range turns {
		switch t.Role {
		case "assistant":
			msgs = append(msgs, openai.AssistantMessage(t.Text))
		default:
			msgs = append(msgs, openai.UserMessage(t.Text))
		}
	}

	tool := openai.ChatCompletionToolParam{
		Function: shared.FunctionDefinitionParam{
			Name:        toolName,
			Description: openai.String("提交翻译结果。这是唯一允许的输出通道。"),
			Parameters:  shared.FunctionParameters(schema),
			// strict 让 API 保证参数严格符合 schema，语义等价于 AnthropicLLM 里的
			// Strict: true——两家供应商都必须走这条路，不能有一个是"尽量符合"。
			Strict: openai.Bool(true),
		},
	}

	resp, err := a.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:               a.model,
		Messages:            msgs,
		Tools:               []openai.ChatCompletionToolParam{tool},
		MaxCompletionTokens: openai.Int(a.maxTokens),
		// 强制走工具调用，堵死模型返回自由文本的可能，跟 Anthropic 侧的
		// ToolChoice 是同一个目的。
		ToolChoice: openai.ChatCompletionToolChoiceOptionParamOfChatCompletionNamedToolChoice(
			openai.ChatCompletionNamedToolChoiceFunctionParam{Name: toolName},
		),
	})
	if err != nil {
		return "", fmt.Errorf("调用模型失败：%w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("模型未返回任何选择")
	}

	msg := resp.Choices[0].Message
	if msg.Refusal != "" {
		return "", fmt.Errorf("模型拒绝了本次请求（%s）", msg.Refusal)
	}
	for _, call := range msg.ToolCalls {
		if call.Function.Name == toolName {
			return call.Function.Arguments, nil
		}
	}
	return "", errors.New("模型未通过工具通道返回结果")
}
