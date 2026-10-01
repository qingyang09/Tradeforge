package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"tradeforge/internal/config"
	"tradeforge/internal/i18n"
)

// Turn is one turn of the conversation.
type Turn struct {
	// Role is "user" or "assistant".
	Role string
	// Text is this turn's text content.
	Text string
}

// LLM is the translation layer's minimal abstraction over a large language model.
//
// Abstracting away the specific vendor is deliberate: none of the Agent's logic
// (schema validation, compliance checks, the confirmation loop) depends on any
// one API, so tests can cover every branch with a stub implementation without
// ever calling an external service.
type LLM interface {
	// Complete makes one schema-constrained generation call and returns the
	// model's raw JSON output text. lang selects the language of any
	// LLM-facing text this call builds itself (e.g. the tool description),
	// matching the language system/schema were already built in by the
	// caller -- see prompt.go/schema.go for why this matters: a model
	// given a fully English conversation except for one stray Chinese
	// instruction is exactly the kind of inconsistency this project's
	// bilingual work set out to eliminate.
	Complete(ctx context.Context, system string, schema Schema, turns []Turn, lang i18n.Lang) (string, error)
}

// ErrNoAPIKey indicates no API key is configured.
var ErrNoAPIKey = errors.New("ANTHROPIC_API_KEY is not configured, the Agent cannot call the model")

// AnthropicLLM implements LLM using the Anthropic Messages API.
type AnthropicLLM struct {
	client anthropic.Client
	model  string
	// maxTokens is the output cap for a single generation.
	maxTokens int64
}

// NewAnthropicLLM constructs a client from the given config.
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

// DefaultModel is the model the translation layer uses by default.
const DefaultModel = "claude-opus-5"

// toolName is the name of the tool that carries the structured output.
//
// Forcing tool use (tool_choice pinned to this tool) instead of just asking
// for "please output JSON" is deliberate: the former has the API guarantee
// structural validity, the latter only hopes the model cooperates — the
// platform explicitly requires that free-form text never enters the
// execution chain directly.
const toolName = "emit_strategy_translation"

// toolDescription is the structured-output tool's description, shared by
// both LLM backends (llm.go, llm_openai.go) -- in lang, matching whatever
// language the system prompt and schema were already built in, so nothing
// in the model's view of the conversation is left in a different language.
func toolDescription(lang i18n.Lang) string {
	if lang == i18n.LangEN {
		return "Submit the translation result. This is the only permitted output channel."
	}
	return "提交翻译结果。这是唯一允许的输出通道。"
}

// Complete implements LLM.
func (a *AnthropicLLM) Complete(ctx context.Context, system string, schema Schema, turns []Turn, lang i18n.Lang) (string, error) {
	if len(turns) == 0 {
		return "", errors.New("conversation has no turns")
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
		Description: anthropic.String(toolDescription(lang)),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: schema["properties"],
			ExtraFields: map[string]any{
				"required":             schema["required"],
				"additionalProperties": false,
			},
		},
		// strict makes the API guarantee the tool input strictly conforms to the
		// schema, rather than just "best effort".
		Strict: anthropic.Bool(true),
	}

	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: a.maxTokens,
		System:    []anthropic.TextBlockParam{{Text: system}},
		Messages:  msgs,
		Tools:     []anthropic.ToolUnionParam{{OfTool: &tool}},
		// Force tool-use routing to close off any path for the model to return
		// free-form text.
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfTool: &anthropic.ToolChoiceToolParam{Name: toolName},
		},
	})
	if err != nil {
		return "", fmt.Errorf("model call failed: %w", err)
	}

	if resp.StopReason == anthropic.StopReasonRefusal {
		return "", fmt.Errorf("model refused this request (%s)", resp.StopDetails.Explanation)
	}

	for _, block := range resp.Content {
		if use, ok := block.AsAny().(anthropic.ToolUseBlock); ok && use.Name == toolName {
			return use.JSON.Input.Raw(), nil
		}
	}
	return "", fmt.Errorf("model did not return a result via the tool channel (stop_reason=%s)", resp.StopReason)
}

// StubLLM is a programmable stub implementation for tests and offline demos.
type StubLLM struct {
	// Responses are returned in call order. Once exhausted, further calls
	// keep returning the last one.
	Responses []string
	// Err, when non-nil, is returned by every call.
	Err error
	// Calls records the turns received by each call, so tests can assert on
	// retry behavior.
	Calls [][]Turn
}

// Complete implements LLM.
func (s *StubLLM) Complete(_ context.Context, _ string, _ Schema, turns []Turn, _ i18n.Lang) (string, error) {
	s.Calls = append(s.Calls, turns)
	if s.Err != nil {
		return "", s.Err
	}
	if len(s.Responses) == 0 {
		return "", errors.New("StubLLM has no responses configured")
	}
	i := len(s.Calls) - 1
	if i >= len(s.Responses) {
		i = len(s.Responses) - 1
	}
	return s.Responses[i], nil
}

// MustJSON is a test helper: marshals a Go value to a JSON string, panicking on failure.
func MustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
