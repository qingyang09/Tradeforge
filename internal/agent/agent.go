// Package agent implements the natural-language-to-StrategyConfig
// translation layer.
//
// This is the part of the project that most needs careful design, under
// three non-negotiable principles:
//
//  1. The LLM's output must pass strict schema + platform validation; if it
//     doesn't comply, reject and regenerate — never "best-effort repair" it
//  2. The generated config must be restated to the user for confirmation
//     first; nothing enters the system until the user has signed off
//  3. Nothing in user-facing copy may contain investment-advice language
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/i18n"
	"tradeforge/internal/modules"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// Outcome is the result type of one translation.
type Outcome string

const (
	// OutcomeConfig means a config has been translated and is awaiting user confirmation.
	OutcomeConfig Outcome = "config"
	// OutcomeClarify means the description is ambiguous and the user must answer questions first.
	OutcomeClarify Outcome = "clarification_needed"
)

// Proposal is one output of the Agent: either a config awaiting confirmation, or a set of clarifying questions.
type Proposal struct {
	Outcome Outcome `json:"outcome"`
	// Restatement is the plain-language restatement of "the strategy I understood",
	// which must be shown to the user for confirmation.
	Restatement string `json:"restatement"`
	// Config is the translated config; nil when Outcome is OutcomeClarify.
	Config *types.StrategyConfig `json:"config"`
	// Questions are the clarifying questions; empty when Outcome is OutcomeConfig.
	Questions []string `json:"questions"`
	// SourceUtterance is the user's original words that produced this proposal.
	SourceUtterance string `json:"source_utterance"`
	// Attempts is how many model calls this translation actually took, for observing the retry rate.
	Attempts int `json:"attempts"`
}

// NeedsClarification reports whether the user must answer questions first.
func (p *Proposal) NeedsClarification() bool { return p.Outcome == OutcomeClarify }

// Agent is the translation layer.
type Agent struct {
	llm        LLM
	registry   *modules.Registry
	maxRetries int
}

// New constructs an Agent. maxRetries is how many times regeneration is allowed after a validation failure.
func New(llm LLM, reg *modules.Registry, maxRetries int) *Agent {
	if maxRetries < 0 {
		maxRetries = 0
	}
	return &Agent{
		llm:        llm,
		registry:   reg,
		maxRetries: maxRetries,
	}
}

// Schema returns the JSON Schema this Agent uses, in lang, for debugging and display.
func (a *Agent) Schema(lang i18n.Lang) Schema { return BuildSchema(a.registry, lang) }

// SystemPrompt returns the system prompt this Agent uses, in lang.
func (a *Agent) SystemPrompt(lang i18n.Lang) string { return SystemPrompt(a.registry, lang) }

// Translate translates one natural-language utterance into a proposal awaiting confirmation.
//
// history is the prior conversation turns (passed in when the user is answering clarifying questions); may be nil.
// lang selects which language the system prompt, schema hints, and the
// model's own restatement/questions come back in.
//
// Note: this method only produces a proposal and never writes the config into the system. Writing must go through Confirm.
func (a *Agent) Translate(ctx context.Context, utterance string, history []Turn, lang i18n.Lang) (*Proposal, error) {
	if strings.TrimSpace(utterance) == "" {
		return nil, errors.New("strategy description cannot be empty")
	}

	system := SystemPrompt(a.registry, lang)
	schema := BuildSchema(a.registry, lang)
	turns := append(append([]Turn{}, history...), Turn{Role: "user", Text: UserPrompt(utterance, lang)})

	// The multi-timeframe consistency check needs "everything the user has
	// said so far", not just this turn — timeframe info may well have been
	// given in an earlier clarification round.
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
		raw, err := a.llm.Complete(ctx, system, schema, turns)
		if err != nil {
			// A call failure is an infrastructure problem, not a model-output
			// problem, so it doesn't go through the retry logic.
			return nil, fmt.Errorf("translation request attempt %d failed: %w", attempt, err)
		}

		proposal, err := a.parseAndValidate(raw)
		if err == nil && proposal.Outcome == OutcomeConfig {
			// Structurally valid isn't the same as semantically faithful: the
			// model occasionally leaves a module's timeframe unset in a
			// multi-timeframe strategy, letting it silently follow the
			// trigger timeframe instead. This check specifically catches
			// that case, and treats a catch as a validation failure that
			// goes through retry — passing the schema must not mask
			// something getting lost in translation.
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
		// A retry is "regenerate", not "patch the previous output": feed the
		// previous output and the problem back to the model together, and
		// let it start over from scratch.
		turns = append(turns,
			Turn{Role: "assistant", Text: raw},
			Turn{Role: "user", Text: RetryPrompt(err.Error(), lang)},
		)
	}

	return nil, fmt.Errorf("model output failed validation after %d attempts, rejected: %w", a.maxRetries+1, lastErr)
}

// parseAndValidate parses the model output and runs it through every validation. Any single failure rejects the whole thing.
func (a *Agent) parseAndValidate(raw string) (*Proposal, error) {
	var wire wireProposal
	dec := json.NewDecoder(strings.NewReader(raw))
	// Reject fields outside the schema: extra fields mean the model didn't
	// output what was agreed, and silently ignoring them would let
	// hallucinations into the system.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&wire); err != nil {
		return nil, fmt.Errorf("output is not valid JSON or contains undefined fields: %w", err)
	}

	p := &Proposal{
		Outcome:     wire.Outcome,
		Restatement: strings.TrimSpace(wire.Restatement),
		Questions:   wire.Questions,
	}

	switch p.Outcome {
	case OutcomeClarify:
		if len(p.Questions) == 0 {
			return nil, errors.New("outcome is clarification_needed but no clarifying questions were given")
		}
		if wire.Config != nil {
			return nil, errors.New("config must be null when outcome is clarification_needed")
		}
	case OutcomeConfig:
		if len(p.Questions) > 0 {
			return nil, errors.New("outcome is config but still carries clarifying questions, a semantic contradiction")
		}
		if wire.Config == nil {
			return nil, errors.New("outcome is config but no config was given")
		}
		cfg, err := wire.Config.toStrategyConfig()
		if err != nil {
			return nil, err
		}
		// Uses the exact same validation as the combination engine: if the
		// Agent says it passes, the engine must agree.
		if _, err := strategy.Validate(cfg, a.registry); err != nil {
			return nil, err
		}
		p.Config = &cfg
	default:
		return nil, fmt.Errorf("unknown outcome %q", p.Outcome)
	}

	if p.Restatement == "" {
		return nil, errors.New("missing restatement, the user has no way to confirm their rule was understood correctly")
	}
	if err := checkCompliance(p); err != nil {
		return nil, err
	}
	return p, nil
}

// Confirm turns a proposal into a strategy config ready to be persisted, after the user has confirmed it.
//
// This is the only entry point through which a config enters the system. It
// deliberately requires confirmed=true to be passed explicitly: the caller
// must have actually gotten the user's sign-off, not just casually treat the
// proposal as the result.
func (a *Agent) Confirm(p *Proposal, confirmed bool) (types.StrategyConfig, error) {
	if p == nil {
		return types.StrategyConfig{}, errors.New("proposal is nil")
	}
	if p.NeedsClarification() {
		return types.StrategyConfig{}, errors.New("this proposal is still awaiting user clarification and cannot be confirmed directly")
	}
	if p.Config == nil {
		return types.StrategyConfig{}, errors.New("proposal has no config")
	}
	if !confirmed {
		return types.StrategyConfig{}, ErrNotConfirmed
	}

	// Re-run validation on confirm: the proposal may have been modified by the caller after generation.
	if _, err := strategy.Validate(*p.Config, a.registry); err != nil {
		return types.StrategyConfig{}, fmt.Errorf("validation failed on confirm: %w", err)
	}

	cfg := *p.Config
	cfg.SourceUtterance = p.SourceUtterance
	// New strategies always start at DRAFT; they must complete backtesting and paper trading before going live.
	cfg.State = types.StateDraft
	now := time.Now().UTC()
	cfg.CreatedAt, cfg.UpdatedAt = now, now
	return cfg, nil
}

// ErrNotConfirmed indicates the user has not yet confirmed; the config must not enter the system.
var ErrNotConfirmed = errors.New("user has not confirmed this strategy config, write rejected")

// ---------- wire format ----------

// wireProposal is the wire format of the model's output. It is kept separate
// from Proposal because fields like monetary amounts are strings on the wire
// (to avoid float precision issues) and need explicit conversion.
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

// DecodeStrategyConfigJSON decodes wire-format JSON (the same shape as the
// LLM's config field: amounts as strings, to avoid float precision issues)
// into a types.StrategyConfig.
//
// This function is exported so the "visual strategy builder" path, which
// bypasses the LLM, can share the same decimal-safe parsing logic as
// natural-language translation instead of reimplementing the amount/duration
// conversion rules a second time.
func DecodeStrategyConfigJSON(blob []byte) (types.StrategyConfig, error) {
	var w wireConfig
	dec := json.NewDecoder(strings.NewReader(string(blob)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return types.StrategyConfig{}, fmt.Errorf("config is not valid JSON or contains undefined fields: %w", err)
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

	// Amounts go through decimal.NewFromString — never through float64.
	if s := strings.TrimSpace(w.MaxPositionSizeQuote); s != "" {
		v, err := decimal.NewFromString(s)
		if err != nil {
			return out, fmt.Errorf("max position size per trade %q is not a valid amount: %w", s, err)
		}
		out.MaxPositionSizeQuote = v
	}
	if s := strings.TrimSpace(w.MaxDailyLossQuote); s != "" {
		v, err := decimal.NewFromString(s)
		if err != nil {
			return out, fmt.Errorf("max daily loss %q is not a valid amount: %w", s, err)
		}
		out.MaxDailyLossQuote = v
	}
	if s := strings.TrimSpace(w.MaxHoldingPeriod); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return out, fmt.Errorf("max holding period %q could not be parsed: %w", s, err)
		}
		out.MaxHoldingPeriod = types.D(d)
	}
	if s := strings.TrimSpace(w.AccountEquityQuote); s != "" {
		v, err := decimal.NewFromString(s)
		if err != nil {
			return out, fmt.Errorf("account equity %q is not a valid amount: %w", s, err)
		}
		out.AccountEquityQuote = v
	}
	return out, nil
}
