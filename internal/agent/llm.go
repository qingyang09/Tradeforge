package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"tradeforge/internal/config"
)

// Turn 是一轮对话。
type Turn struct {
	// Role 为 "user" 或 "assistant"。
	Role string
	// Text 是该轮的文本内容。
	Text string
}

// LLM 是翻译层对大模型的最小抽象。
//
// 抽掉具体厂商是有意的：Agent 的全部逻辑（schema 校验、合规检查、确认循环）
// 都不依赖某一家的 API，测试也就能用桩实现覆盖全部分支，
// 不必真的调用外部服务。
type LLM interface {
	// Complete 发起一次受 schema 约束的生成，返回模型输出的原始 JSON 文本。
	Complete(ctx context.Context, system string, schema Schema, turns []Turn) (string, error)
}

// ErrNoAPIKey 表示未配置 API 密钥。
var ErrNoAPIKey = errors.New("未配置 ANTHROPIC_API_KEY，Agent 无法调用模型")

// AnthropicLLM 用 Anthropic Messages API 实现 LLM。
type AnthropicLLM struct {
	client anthropic.Client
	model  string
	// maxTokens 是单次生成的输出上限。
	maxTokens int64
}

// NewAnthropicLLM 按配置构造客户端。
func NewAnthropicLLM(cfg config.AgentConfig) (*AnthropicLLM, error) {
	if cfg.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	model := cfg.Model
	if model == "" {
		model = DefaultModel
	}
	return &AnthropicLLM{
		client:    anthropic.NewClient(opts...),
		model:     model,
		maxTokens: 8192,
	}, nil
}

// DefaultModel 是翻译层默认使用的模型。
const DefaultModel = "claude-opus-5"

// toolName 是承载结构化输出的工具名。
//
// 用强制工具调用（tool_choice 指定这个工具）而不是"请你输出 JSON"，
// 是因为前者由 API 保证结构合法，后者只是祈求模型配合——
// 平台明确要求"绝不允许自由文本直接进入执行链路"。
const toolName = "emit_strategy_translation"

// Complete 实现 LLM。
func (a *AnthropicLLM) Complete(ctx context.Context, system string, schema Schema, turns []Turn) (string, error) {
	if len(turns) == 0 {
		return "", errors.New("对话轮次为空")
	}

	msgs := make([]anthropic.MessageParam, 0, len(turns))
	for _, t := range turns {
		block := anthropic.NewTextBlock(t.Text)
		switch t.Role {
		case "assistant":
			msgs = append(msgs, anthropic.NewAssistantMessage(block))
		default:
			msgs = append(msgs, anthropic.NewUserMessage(block))
		}
	}

	tool := anthropic.ToolParam{
		Name:        toolName,
		Description: anthropic.String("提交翻译结果。这是唯一允许的输出通道。"),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: schema["properties"],
			ExtraFields: map[string]any{
				"required":             schema["required"],
				"additionalProperties": false,
			},
		},
		// strict 让 API 保证工具入参严格符合 schema，而不是"尽量符合"。
		Strict: anthropic.Bool(true),
	}

	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: a.maxTokens,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages:  msgs,
		Tools:     []anthropic.ToolUnionParam{{OfTool: &tool}},
		// 强制走工具调用，堵死模型返回自由文本的可能。
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfTool: &anthropic.ToolChoiceToolParam{Name: toolName},
		},
	})
	if err != nil {
		return "", fmt.Errorf("调用模型失败：%w", err)
	}

	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", fmt.Errorf("模型拒绝了本次请求（%s）", resp.StopDetails.Explanation)
	}

	for _, block := range resp.Content {
		if use, ok := block.AsAny().(anthropic.ToolUseBlock); ok && use.Name == toolName {
			return use.JSON.Input.Raw(), nil
		}
	}
	return "", fmt.Errorf("模型未通过工具通道返回结果（stop_reason=%s）", resp.StopReason)
}

// StubLLM 是可编程的桩实现，供测试与离线演示使用。
type StubLLM struct {
	// Responses 按调用顺序返回。用完后继续调用会返回最后一个。
	Responses []string
	// Err 非 nil 时所有调用都返回该错误。
	Err error
	// Calls 记录每次调用收到的对话轮次，供测试断言重试行为。
	Calls [][]Turn
}

// Complete 实现 LLM。
func (s *StubLLM) Complete(_ context.Context, _ string, _ Schema, turns []Turn) (string, error) {
	s.Calls = append(s.Calls, turns)
	if s.Err != nil {
		return "", s.Err
	}
	if len(s.Responses) == 0 {
		return "", errors.New("StubLLM 未配置任何响应")
	}
	i := len(s.Calls) - 1
	if i >= len(s.Responses) {
		i = len(s.Responses) - 1
	}
	return s.Responses[i], nil
}

// MustJSON 是测试辅助：把 Go 值序列化成 JSON 字符串，失败即 panic。
func MustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
