package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tradeforge/internal/i18n"
	"tradeforge/pkg/types"
)

// descSchema constrains Describe's output: just one field, unlike Translate,
// which has to handle the outcome/questions/config branches -- the config is
// already fixed here, nothing ambiguous left to clarify.
func descSchema(lang i18n.Lang) Schema {
	return Schema{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"restatement"},
		"properties": map[string]any{
			"restatement": map[string]any{
				"type":        "string",
				"description": i18n.T(lang, "agent.schema.describe.restatement"),
			},
		},
	}
}

// DescribeSystemPrompt is the system prompt Describe uses, in lang.
//
// Same role boundary as SystemPrompt (translating natural language), but
// narrower in scope: there's no "translation" here — the config has already
// been fixed by the user in the visual builder, and the model's only job is
// to restate it in plain language.
func DescribeSystemPrompt(lang i18n.Lang) string {
	if lang == i18n.LangEN {
		return `You are a trading-strategy "restater." The user has already fully configured a
trading strategy by hand through the visual builder (which modules, each module's
parameters, how they're combined, what risk controls) -- your only job is to restate
this already-decided, structured config in plain language, so the user can confirm
what they just set up was recorded correctly.

# Role boundary (highest priority, must never be crossed under any circumstance)

- The config is already decided and structured, not an ambiguous description awaiting
  translation -- never reinterpret it, never guess at the user's intent, never add or
  omit anything from it
- Never judge whether this config is good or bad -- never say things like "this is a
  good setup," "the risk is a bit high," or "you should adjust..."
- Never predict market movement, never discuss win rate or expected returns

# Restatement requirements

State in plain language: which symbol, which timeframe, which modules and their
parameters, how they're combined, and the risk-control settings.
Add no evaluative wording of any kind.

# Output format

Output only the structure defined by the given JSON Schema (a single restatement
field) -- nothing else.`
	}
	return `你是一个交易策略"复述器"。用户已经通过可视化界面亲手配置好了一份完整的
交易策略（选了哪些模块、每个模块什么参数、怎么组合、什么风控），你的唯一职责，
是把这份已经确定的结构化配置，用大白话复述一遍给用户看，方便他确认自己刚才的操作
有没有被正确记录。

# 角色边界（最高优先级，任何情况下都不得突破）

- 配置已经是确定的、结构化的，不是待翻译的模糊描述——不许重新解读、不许猜测用户的
  意图、不许增加或省略任何一项配置
- 绝不评价这份配置好坏，不说"这样设置不错""风险偏高""建议调整……"
- 绝不预测行情，不谈胜率或收益预期

# 复述要求

用大白话说清楚：什么标的、什么周期、用了哪些模块及其参数、怎么组合、风控设置。
不加任何评价性词汇。

# 输出格式

只输出符合给定 JSON Schema 的结构（一个 restatement 字段），不要有任何额外文字。`
}

// DescribeUserPrompt serializes the already-built config and hands it to
// the model to restate, in lang.
func DescribeUserPrompt(cfg types.StrategyConfig, lang i18n.Lang) (string, error) {
	blob, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to serialize config: %w", err)
	}
	if lang == i18n.LangEN {
		return fmt.Sprintf("Please restate the following already-configured strategy:\n\n%s", blob), nil
	}
	return fmt.Sprintf("请复述下面这份已经配置好的策略：\n\n%s", blob), nil
}

// Describe restates an already-built structured config in plain language, in
// lang, for the visual strategy builder to show the user for confirmation.
//
// The key difference from Translate: there's no "translation" step here --
// the config is already fixed, so this doesn't accept a clarification-
// question branch and doesn't retry (a restatement failure is most likely
// the model glitching; erroring out immediately and letting the caller fall
// back to the deterministic restatement saves more time than retrying would).
func (a *Agent) Describe(ctx context.Context, cfg types.StrategyConfig, lang i18n.Lang) (string, error) {
	prompt, err := DescribeUserPrompt(cfg, lang)
	if err != nil {
		return "", err
	}
	raw, err := a.llm.Complete(ctx, DescribeSystemPrompt(lang), descSchema(lang), []Turn{{Role: "user", Text: prompt}}, lang)
	if err != nil {
		return "", fmt.Errorf("restatement request failed: %w", err)
	}

	var wire struct {
		Restatement string `json:"restatement"`
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return "", fmt.Errorf("restatement output is not valid JSON or contains undefined fields: %w", err)
	}

	restatement := strings.TrimSpace(wire.Restatement)
	if restatement == "" {
		return "", errors.New("restatement is empty")
	}
	if err := scanText("restatement", restatement); err != nil {
		return "", err
	}
	return restatement, nil
}
