package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"

	"tradeforge/internal/config"
	"tradeforge/internal/i18n"
)

// DefaultOpenAIModel is the model the OpenAI provider uses when no model is
// explicitly specified.
const DefaultOpenAIModel = "gpt-4o"

// DefaultOpenAIBaseURL is OpenAI's official API address.
const DefaultOpenAIBaseURL = "https://api.openai.com/v1"

// OpenAILLM implements LLM using OpenAI's Chat Completions API.
//
// Structurally a perfect mirror of AnthropicLLM: it likewise forces a single
// tool call (instead of "please output JSON") to get structured output, and
// the same compliance requirement — free-form text must never enter the
// execution chain — must be upheld here just as strictly; switching vendors
// is no excuse to relax the constraint.
type OpenAILLM struct {
	client    openai.Client
	model     string
	baseURL   string
	maxTokens int64
}

// NewOpenAILLM constructs a client from the given config.
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

// Complete implements LLM.
func (a *OpenAILLM) Complete(ctx context.Context, system string, schema Schema, turns []Turn, lang i18n.Lang) (string, error) {
	if len(turns) == 0 {
		return "", errors.New("conversation has no turns")
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
			Description: openai.String(toolDescription(lang)),
			Parameters:  shared.FunctionParameters(schema),
			// strict makes the API guarantee the arguments strictly conform to
			// the schema, semantically equivalent to AnthropicLLM's Strict: true
			// — both vendors must follow this path, neither gets to be
			// "best effort".
			Strict: openai.Bool(true),
		},
	}

	resp, err := a.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:               a.model,
		Messages:            msgs,
		Tools:               []openai.ChatCompletionToolParam{tool},
		MaxCompletionTokens: openai.Int(a.maxTokens),
		// Force tool-use routing to close off any path for the model to return
		// free-form text — same purpose as the ToolChoice on the Anthropic side.
		ToolChoice: openai.ChatCompletionToolChoiceOptionParamOfChatCompletionNamedToolChoice(
			openai.ChatCompletionNamedToolChoiceFunctionParam{Name: toolName},
		),
	})
	if err != nil {
		return "", fmt.Errorf("model call failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", errors.New("model returned no choices")
	}

	msg := resp.Choices[0].Message
	if msg.Refusal != "" {
		return "", fmt.Errorf("model refused this request (%s)", msg.Refusal)
	}
	for _, call := range msg.ToolCalls {
		if call.Function.Name == toolName {
			return call.Function.Arguments, nil
		}
	}
	return "", errors.New("model did not return a result via the tool channel")
}
