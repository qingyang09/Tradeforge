package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"tradeforge/internal/agent"
	"tradeforge/internal/i18n"
	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// maxClarificationRounds mirrors cmd/agent-service's CLI wizard: too many
// clarification rounds means the description itself just isn't complete
// enough -- better to prompt the user to re-describe than loop forever.
const maxClarificationRounds = 5

type wizardStartData struct {
	AgentReady bool
}

func (s *Server) handleWizardStart(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	ag, _ := s.userAgent(r.Context(), userID)
	s.renderPage(w, r, i18n.T(resolveLang(r), "webui.wizard.start.title"), "wizard_start_content", wizardStartData{AgentReady: ag != nil})
}

type wizardClarifyData struct {
	Questions []string
	State     string
}

type wizardConfirmData struct {
	Restatement string
	Config      types.StrategyConfig
	// ConfigJSON is Config as wire-format JSON (the same shape
	// DecodeStrategyConfigJSON understands), embedded in the confirmation
	// fragment for the builder page's JS to read (see wizard_confirm.html) --
	// it draws this not-yet-persisted config actually onto the chart: module
	// parameters become draggable lines, stop-loss/take-profit become price
	// levels, instead of staying abstract text in this table. The text
	// wizard page never reads this field, so the extra content has no effect
	// on it.
	ConfigJSON string
	State      string
}

// wizardErrorData.Message stays a plain string (not types.Message) rather
// than the usual Class A/B split -- handlers_builder.go (the visual
// chart-builder page, a different in-flight change) constructs this struct
// with bare Chinese literals too, and changing the field's type would break
// that file without touching it. i18n.T renders straight to a string at the
// call site instead, matching its own doc comment's "one-off banner/error
// string" case.
type wizardErrorData struct {
	Message string
}

func (s *Server) handleWizardTranslate(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	ag, _ := s.userAgent(r.Context(), userID)
	lang := resolveLang(r)
	if ag == nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.agent_not_ready")})
		return
	}
	utterance := strings.TrimSpace(r.FormValue("utterance"))
	if utterance == "" {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.empty_utterance")})
		return
	}

	p, err := ag.Translate(r.Context(), utterance, nil, lang)
	if err != nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.translate_failed", "err", err.Error())})
		return
	}
	s.renderProposal(w, r, p, nil, "")
}

func (s *Server) handleWizardClarify(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	ag, _ := s.userAgent(r.Context(), userID)
	lang := resolveLang(r)
	if ag == nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.agent_not_ready")})
		return
	}
	ws, err := decodeState(r.FormValue("state"))
	if err != nil || ws.Proposal == nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.session_expired")})
		return
	}
	answer := strings.TrimSpace(r.FormValue("answer"))
	if answer == "" {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.empty_answer")})
		return
	}

	// Feed the previous round's questions back into the conversation history
	// as "what the assistant said," exactly the same way the CLI's
	// runClarificationLoop assembles history.
	newHistory := append(append([]agent.Turn{}, ws.History...),
		agent.Turn{Role: "user", Text: ws.Proposal.SourceUtterance},
		agent.Turn{Role: "assistant", Text: strings.Join(ws.Proposal.Questions, "\n")})

	if len(newHistory)/2 >= maxClarificationRounds {
		s.renderFragment(w, r, "wizard_error_fragment",
			wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.too_many_clarifications")})
		return
	}

	p, err := ag.Translate(r.Context(), answer, newHistory, lang)
	if err != nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.translate_failed", "err", err.Error())})
		return
	}
	s.renderProposal(w, r, p, newHistory, ws.ForceSymbol)
}

// renderProposal branches on Outcome to render either a clarification
// question or the restatement confirmation; shared by the translate/clarify
// handlers.
//
// When forceSymbol is non-empty it overrides the proposal's symbol (see
// wizardState.ForceSymbol's doc comment) -- the text wizard passes an empty
// string, leaving its behavior unchanged; the visual builder passes its
// current chart's symbol.
func (s *Server) renderProposal(w http.ResponseWriter, r *http.Request, p *agent.Proposal, history []agent.Turn, forceSymbol string) {
	if forceSymbol != "" && p.Outcome == agent.OutcomeConfig && p.Config != nil {
		p.Config.Symbol = forceSymbol
	}
	state, err := encodeState(wizardState{History: history, Proposal: p, ForceSymbol: forceSymbol})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if p.NeedsClarification() {
		s.renderFragment(w, r, "wizard_clarify_fragment", wizardClarifyData{Questions: p.Questions, State: state})
		return
	}
	configJSON, err := json.Marshal(p.Config)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderFragment(w, r, "wizard_confirm_fragment",
		wizardConfirmData{Restatement: p.Restatement, Config: *p.Config, ConfigJSON: string(configJSON), State: state})
}

type wizardResultData struct {
	Success    bool
	Message    types.Message
	StrategyID string
}

// handleWizardConfirm is the shared save entry point for both
// strategy-building paths (the text wizard + the visual builder, see
// handlers_builder.go): given any Proposal whose Outcome is config, it can
// complete the "re-validate -> persist -> record audit" step without any
// Agent/LLM involvement. This used to reach the module registry indirectly
// through ag.Confirm, which meant this save step -- which never actually
// needed an LLM -- got blocked on whether the Agent was ready. Since the
// visual builder is required to work without an LLM key, this was changed to
// validate directly against s.registry instead.
func (s *Server) handleWizardConfirm(w http.ResponseWriter, r *http.Request) {
	lang := resolveLang(r)
	ws, err := decodeState(r.FormValue("state"))
	if err != nil || ws.Proposal == nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.session_expired")})
		return
	}

	if r.FormValue("decision") != "confirm" {
		s.renderFragment(w, r, "wizard_result_fragment",
			wizardResultData{Success: false, Message: types.Msg("webui.wizard.result.cancelled")})
		return
	}

	cfg, err := s.confirmProposal(lang, ws.Proposal)
	if err != nil {
		s.renderFragment(w, r, "wizard_result_fragment",
			wizardResultData{Success: false, Message: types.Msg("webui.wizard.result.confirm_failed", "err", err.Error())})
		return
	}
	cfg.ID = idgen.NewUUID()
	cfg.UserID, _ = currentUserID(r)

	ctx := r.Context()
	if err := s.store.SaveStrategy(ctx, cfg); err != nil {
		s.renderFragment(w, r, "wizard_result_fragment",
			wizardResultData{Success: false, Message: types.Msg("webui.wizard.result.save_failed", "err", err.Error())})
		return
	}
	if err := s.store.RecordTransition(ctx, storage.Transition{
		StrategyID: cfg.ID, From: "", To: types.StateDraft,
		Actor: "user:web", Reason: i18n.T(lang, "webui.wizard.transition.user_confirmed"),
		Evidence: map[string]any{"source_utterance": cfg.SourceUtterance, "attempts": ws.Proposal.Attempts},
	}); err != nil {
		// An audit-record write failure doesn't roll back the already-saved
		// strategy -- the strategy itself is the authoritative data; missing
		// one audit record is better than losing a strategy the user already
		// confirmed.
		s.logger.Error("failed to write audit record", "strategy_id", cfg.ID, "err", err)
	}

	s.renderFragment(w, r, "wizard_result_fragment", wizardResultData{Success: true, StrategyID: cfg.ID})
}

// confirmProposal re-validates a proposal and marks it as ready to persist.
// The logic mirrors agent.Confirm (internal/agent/agent.go); the only
// difference is using s.registry instead of reaching the validation module
// registry through *agent.Agent -- so this step needs no LLM involvement.
func (s *Server) confirmProposal(lang i18n.Lang, p *agent.Proposal) (types.StrategyConfig, error) {
	if p == nil {
		return types.StrategyConfig{}, errors.New(i18n.T(lang, "webui.wizard.err.proposal_empty"))
	}
	if p.NeedsClarification() {
		return types.StrategyConfig{}, errors.New(i18n.T(lang, "webui.wizard.err.proposal_needs_clarification"))
	}
	if p.Config == nil {
		return types.StrategyConfig{}, errors.New(i18n.T(lang, "webui.wizard.err.proposal_missing_config"))
	}
	return s.prepareDraftConfig(lang, *p.Config, p.SourceUtterance)
}

// prepareDraftConfig prepares a config into a ready-to-persist DRAFT state:
// re-validate, stamp timestamps. Shared logic between single-strategy
// confirmation (confirmProposal) and batch-scan confirmation
// (handlers_batch.go's handleBatchScanConfirm, called separately for each
// symbol's copy within one loop), so there's only one "validate + state +
// timestamp" implementation to maintain.
//
// Even if the hidden field were tampered with, re-validation can at most get
// this step rejected -- it can never bypass schema or compliance checks.
func (s *Server) prepareDraftConfig(lang i18n.Lang, cfg types.StrategyConfig, sourceUtterance string) (types.StrategyConfig, error) {
	if _, err := strategy.Validate(cfg, s.registry); err != nil {
		return types.StrategyConfig{}, errors.New(i18n.T(lang, "webui.wizard.err.validation_failed", "err", err.Error()))
	}
	cfg.SourceUtterance = sourceUtterance
	cfg.State = types.StateDraft
	now := time.Now().UTC()
	cfg.CreatedAt, cfg.UpdatedAt = now, now
	return cfg, nil
}
