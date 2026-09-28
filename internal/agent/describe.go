package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"tradeforge/pkg/types"
)

// descSchema 约束 Describe 的输出：只有一个字段，不像 Translate 那样要处理
// outcome/questions/config 分支——配置已经是确定的，没有歧义可澄清。
var descSchema = Schema{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"restatement"},
	"properties": map[string]any{
		"restatement": map[string]any{
			"type": "string",
			"description": "用大白话复述下面给出的结构化配置，供用户确认。" +
				"只能描述配置里已有的内容，不得增删或重新解读，不得包含任何评价性或投资建议措辞。",
		},
	},
}

// DescribeSystemPrompt 是 Describe 使用的系统提示词。
//
// 跟 SystemPrompt（翻译自然语言）的角色边界一致，但范围更窄：这里没有"翻译"，
// 配置已经由用户在可视化界面上亲手确定，模型唯一的工作是把它复述成大白话。
func DescribeSystemPrompt() string {
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

// DescribeUserPrompt 把已构建的配置序列化后交给模型复述。
func DescribeUserPrompt(cfg types.StrategyConfig) (string, error) {
	blob, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化配置失败：%w", err)
	}
	return fmt.Sprintf("请复述下面这份已经配置好的策略：\n\n%s", blob), nil
}

// Describe 把一份已经构建好的结构化配置复述成大白话，供可视化建策界面展示给用户确认。
//
// 跟 Translate 的关键区别：这里没有"翻译"这一步，配置已经是确定的——不接受
// 澄清问题分支，也不做重试（复述失败大概率是模型抽风，直接报错让上层换成确定性
// 兜底复述，比反复重试更省时间）。
func (a *Agent) Describe(ctx context.Context, cfg types.StrategyConfig) (string, error) {
	prompt, err := DescribeUserPrompt(cfg)
	if err != nil {
		return "", err
	}
	raw, err := a.llm.Complete(ctx, DescribeSystemPrompt(), descSchema, []Turn{{Role: "user", Text: prompt}})
	if err != nil {
		return "", fmt.Errorf("复述请求失败：%w", err)
	}

	var wire struct {
		Restatement string `json:"restatement"`
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return "", fmt.Errorf("复述输出不是合法的 JSON 或含有未定义字段：%w", err)
	}

	restatement := strings.TrimSpace(wire.Restatement)
	if restatement == "" {
		return "", errors.New("复述内容为空")
	}
	if err := scanText("复述（restatement）", restatement); err != nil {
		return "", err
	}
	return restatement, nil
}
