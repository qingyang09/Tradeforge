// Package agent 实现自然语言到 StrategyConfig 的翻译层。
//
// 这是全项目最需要谨慎设计的部分，三条不可让步的原则：
//
//  1. LLM 的输出必须过严格 schema + 平台校验，不合规就拒绝重来，绝不"尽力修复"
//  2. 生成的配置必须先复述给用户确认，用户点头之前不进入系统
//  3. 面向用户的一切文案都不得包含投资建议措辞
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/modules"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// Outcome 是一次翻译的结果类型。
type Outcome string

const (
	// OutcomeConfig 表示已翻译出配置，等待用户确认。
	OutcomeConfig Outcome = "config"
	// OutcomeClarify 表示描述有歧义，需要用户先回答问题。
	OutcomeClarify Outcome = "clarification_needed"
)

// Proposal 是 Agent 的一次输出：要么是待确认的配置，要么是一组澄清问题。
type Proposal struct {
	Outcome Outcome `json:"outcome"`
	// Restatement 是用大白话复述的"我理解的策略"，必须展示给用户确认。
	Restatement string `json:"restatement"`
	// Config 是翻译出的配置，Outcome 为 OutcomeClarify 时为 nil。
	Config *types.StrategyConfig `json:"config"`
	// Questions 是澄清问题，Outcome 为 OutcomeConfig 时为空。
	Questions []string `json:"questions"`
	// SourceUtterance 是产生本次提案的用户原话。
	SourceUtterance string `json:"source_utterance"`
	// Attempts 是本次翻译实际调用模型的次数，用于观测重试率。
	Attempts int `json:"attempts"`
}

// NeedsClarification 报告是否需要用户先回答问题。
func (p *Proposal) NeedsClarification() bool { return p.Outcome == OutcomeClarify }

// Agent 是翻译层。
type Agent struct {
	llm        LLM
	registry   *modules.Registry
	schema     Schema
	system     string
	maxRetries int
}

// New 构造 Agent。maxRetries 是校验失败后允许重新生成的次数。
func New(llm LLM, reg *modules.Registry, maxRetries int) *Agent {
	if maxRetries < 0 {
		maxRetries = 0
	}
	return &Agent{
		llm:        llm,
		registry:   reg,
		schema:     BuildSchema(reg),
		system:     SystemPrompt(reg),
		maxRetries: maxRetries,
	}
}

// Schema 返回本 Agent 使用的 JSON Schema，便于调试与展示。
func (a *Agent) Schema() Schema { return a.schema }

// SystemPrompt 返回本 Agent 使用的系统提示词。
func (a *Agent) SystemPrompt() string { return a.system }

// Translate 把一句自然语言翻译成待确认的提案。
//
// history 是之前的对话轮次（用户回答澄清问题时传入），可为 nil。
//
// 注意：本方法只产出提案，绝不把配置写入系统。写入必须经由 Confirm。
func (a *Agent) Translate(ctx context.Context, utterance string, history []Turn) (*Proposal, error) {
	if strings.TrimSpace(utterance) == "" {
		return nil, errors.New("策略描述不能为空")
	}

	turns := append(append([]Turn{}, history...), Turn{Role: "user", Text: UserPrompt(utterance)})

	// 多周期一致性检查要看"用户到目前为止说过的全部原话"，不能只看这一轮——
	// 周期信息完全可能是在更早的澄清轮次里说的。
	var allUserText strings.Builder
	for _, t := range history {
		if t.Role == "user" {
			allUserText.WriteString(t.Text)
			allUserText.WriteString(" ")
		}
	}
	allUserText.WriteString(utterance)

	var lastErr error
	for attempt := 1; attempt <= a.maxRetries+1; attempt++ {
		raw, err := a.llm.Complete(ctx, a.system, a.schema, turns)
		if err != nil {
			// 调用失败是基础设施问题，不是模型输出问题，不走重试逻辑。
			return nil, fmt.Errorf("第 %d 次翻译请求失败：%w", attempt, err)
		}

		proposal, err := a.parseAndValidate(raw)
		if err == nil && proposal.Outcome == OutcomeConfig {
			// 结构上合法不等于语义上忠实：模型偶尔会在多周期策略里漏填某个模块的
			// timeframe，让它悄悄跟着触发周期走。这道检查专门抓这种情况，抓到了
			// 就当成校验失败走重试，不能让"能通过 schema"掩盖"翻译丢了东西"。
			err = checkTimeframeCoverage(*proposal.Config, allUserText.String())
		}
		if err == nil {
			proposal.SourceUtterance = utterance
			proposal.Attempts = attempt
			return proposal, nil
		}
		lastErr = err

		if attempt > a.maxRetries {
			break
		}
		// 重试是"重新生成"，不是"修补上次输出"：把上次的输出和问题一起回给模型，
		// 由它整份重来。
		turns = append(turns,
			Turn{Role: "assistant", Text: raw},
			Turn{Role: "user", Text: RetryPrompt(err.Error())},
		)
	}

	return nil, fmt.Errorf("模型输出经 %d 次尝试仍未通过校验，已拒绝：%w", a.maxRetries+1, lastErr)
}

// parseAndValidate 解析模型输出并跑完全部校验。任何一项不过就整体拒绝。
func (a *Agent) parseAndValidate(raw string) (*Proposal, error) {
	var wire wireProposal
	dec := json.NewDecoder(strings.NewReader(raw))
	// 拒绝 schema 之外的字段：多出来的字段说明模型没按约定输出，
	// 静默忽略它们等于放任幻觉进入系统。
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("输出不是合法的 JSON 或含有未定义字段：%w", err)
	}

	p := &Proposal{
		Outcome:     wire.Outcome,
		Restatement: strings.TrimSpace(wire.Restatement),
		Questions:   wire.Questions,
	}

	switch p.Outcome {
	case OutcomeClarify:
		if len(p.Questions) == 0 {
			return nil, errors.New("outcome 为 clarification_needed 但没有给出任何澄清问题")
		}
		if wire.Config != nil {
			return nil, errors.New("outcome 为 clarification_needed 时 config 必须为 null")
		}
	case OutcomeConfig:
		if len(p.Questions) > 0 {
			return nil, errors.New("outcome 为 config 但仍带有澄清问题，语义矛盾")
		}
		if wire.Config == nil {
			return nil, errors.New("outcome 为 config 但没有给出配置")
		}
		cfg, err := wire.Config.toStrategyConfig()
		if err != nil {
			return nil, err
		}
		// 用与组合引擎完全相同的校验：Agent 说通过的，引擎必须也认。
		if _, err := strategy.Validate(cfg, a.registry); err != nil {
			return nil, err
		}
		p.Config = &cfg
	default:
		return nil, fmt.Errorf("未知的 outcome %q", p.Outcome)
	}

	if p.Restatement == "" {
		return nil, errors.New("缺少复述内容，用户无法确认自己的规则是否被正确理解")
	}
	if err := checkCompliance(p); err != nil {
		return nil, err
	}
	return p, nil
}

// Confirm 在用户确认后，把提案正式转成可入库的策略配置。
//
// 这是配置进入系统的唯一入口。它刻意要求显式传入 confirmed=true：
// 调用方必须真的拿到用户的点头，而不是顺手把提案当成结果用掉。
func (a *Agent) Confirm(p *Proposal, confirmed bool) (types.StrategyConfig, error) {
	if p == nil {
		return types.StrategyConfig{}, errors.New("提案为空")
	}
	if p.NeedsClarification() {
		return types.StrategyConfig{}, errors.New("该提案仍在等待用户澄清，不能直接确认")
	}
	if p.Config == nil {
		return types.StrategyConfig{}, errors.New("提案中没有配置")
	}
	if !confirmed {
		return types.StrategyConfig{}, ErrNotConfirmed
	}

	// 确认时重跑一遍校验：提案可能在生成后被调用方改过。
	if _, err := strategy.Validate(*p.Config, a.registry); err != nil {
		return types.StrategyConfig{}, fmt.Errorf("确认时校验未通过：%w", err)
	}

	cfg := *p.Config
	cfg.SourceUtterance = p.SourceUtterance
	// 新策略一律从 DRAFT 开始，必须走完回测与模拟盘才能上实盘。
	cfg.State = types.StateDraft
	now := time.Now().UTC()
	cfg.CreatedAt, cfg.UpdatedAt = now, now
	return cfg, nil
}

// ErrNotConfirmed 表示用户尚未确认，配置不得进入系统。
var ErrNotConfirmed = errors.New("用户尚未确认该策略配置，拒绝写入")

// ---------- 线格式 ----------

// wireProposal 是模型输出的线格式。它与 Proposal 分开，
// 因为金额等字段在线上是字符串（避免浮点精度问题），需要显式转换。
type wireProposal struct {
	Outcome     Outcome     `json:"outcome"`
	Restatement string      `json:"restatement"`
	Questions   []string    `json:"questions"`
	Config      *wireConfig `json:"config"`
}

type wireConfig struct {
	Name      string       `json:"name"`
	Symbol    string       `json:"symbol"`
	Timeframe string       `json:"timeframe"`
	Combine   string       `json:"combine"`
	Threshold float64      `json:"threshold"`
	Modules   []wireModule `json:"modules"`
	Risk      wireRisk     `json:"risk"`
}

type wireModule struct {
	Module    string         `json:"module"`
	Params    map[string]any `json:"params"`
	Weight    float64        `json:"weight"`
	Timeframe string         `json:"timeframe"`
}

type wireRisk struct {
	MaxPositionSizeQuote string  `json:"max_position_size_quote"`
	MaxDailyLossQuote    string  `json:"max_daily_loss_quote"`
	MaxHoldingPeriod     string  `json:"max_holding_period"`
	StopLossMode         string  `json:"stop_loss_mode"`
	StopLossPct          float64 `json:"stop_loss_pct"`
	TakeProfitMode       string  `json:"take_profit_mode"`
	TakeProfitPct        float64 `json:"take_profit_pct"`
	PositionSizingMode   string  `json:"position_sizing_mode"`
	AccountEquityQuote   string  `json:"account_equity_quote"`
	RiskPerTradePct      float64 `json:"risk_per_trade_pct"`
}

// DecodeStrategyConfigJSON 把线格式的 JSON（跟 LLM 输出的 config 字段同一套形状：
// 金额是字符串，避免浮点精度问题）解码成 types.StrategyConfig。
//
// 导出这个函数是为了让"可视化建策"这条不经过 LLM 的路径，跟自然语言翻译共用同一份
// decimal-safe 的解析逻辑，不必重新实现一遍金额/时长的转换规则。
func DecodeStrategyConfigJSON(blob []byte) (types.StrategyConfig, error) {
	var w wireConfig
	dec := json.NewDecoder(strings.NewReader(string(blob)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return types.StrategyConfig{}, fmt.Errorf("配置不是合法的 JSON 或含有未定义字段：%w", err)
	}
	return w.toStrategyConfig()
}

func (w *wireConfig) toStrategyConfig() (types.StrategyConfig, error) {
	cfg := types.StrategyConfig{
		Name:      strings.TrimSpace(w.Name),
		Symbol:    strings.ToUpper(strings.TrimSpace(w.Symbol)),
		Timeframe: types.Timeframe(w.Timeframe),
		Combine:   types.CombineMode(w.Combine),
		Threshold: w.Threshold,
		State:     types.StateDraft,
	}

	for _, m := range w.Modules {
		cfg.Modules = append(cfg.Modules, types.ModuleConfig{
			Module:    m.Module,
			Params:    m.Params,
			Weight:    m.Weight,
			Timeframe: types.Timeframe(m.Timeframe),
		})
	}

	risk, err := w.Risk.toRiskConfig()
	if err != nil {
		return types.StrategyConfig{}, err
	}
	cfg.Risk = risk
	return cfg, nil
}

func (w wireRisk) toRiskConfig() (types.RiskConfig, error) {
	out := types.RiskConfig{
		StopLossMode:       types.RiskLevelMode(w.StopLossMode),
		StopLossPct:        w.StopLossPct,
		TakeProfitMode:     types.RiskLevelMode(w.TakeProfitMode),
		TakeProfitPct:      w.TakeProfitPct,
		PositionSizingMode: types.PositionSizingMode(w.PositionSizingMode),
		RiskPerTradePct:    w.RiskPerTradePct,
	}

	// 金额走 decimal.NewFromString：绝不经过 float64。
	if s := strings.TrimSpace(w.MaxPositionSizeQuote); s != "" {
		v, err := decimal.NewFromString(s)
		if err != nil {
			return out, fmt.Errorf("单笔最大仓位 %q 不是合法的金额：%w", s, err)
		}
		out.MaxPositionSizeQuote = v
	}
	if s := strings.TrimSpace(w.MaxDailyLossQuote); s != "" {
		v, err := decimal.NewFromString(s)
		if err != nil {
			return out, fmt.Errorf("单日最大亏损 %q 不是合法的金额：%w", s, err)
		}
		out.MaxDailyLossQuote = v
	}
	if s := strings.TrimSpace(w.MaxHoldingPeriod); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return out, fmt.Errorf("最大持仓时间 %q 无法解析：%w", s, err)
		}
		out.MaxHoldingPeriod = types.D(d)
	}
	if s := strings.TrimSpace(w.AccountEquityQuote); s != "" {
		v, err := decimal.NewFromString(s)
		if err != nil {
			return out, fmt.Errorf("账户权益 %q 不是合法的金额：%w", s, err)
		}
		out.AccountEquityQuote = v
	}
	return out, nil
}
