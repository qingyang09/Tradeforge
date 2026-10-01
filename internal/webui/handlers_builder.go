package webui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"tradeforge/internal/agent"
	"tradeforge/internal/i18n"
	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// symbolPattern roughly validates that a path param looks like a symbol
// (e.g. "BTCUSDT"), rejecting obviously unreasonable input (empty, too long,
// special characters).
var symbolPattern = regexp.MustCompile(`^[A-Za-z0-9]{2,20}$`)

func looksLikeSymbol(s string) bool { return symbolPattern.MatchString(s) }

type builderListData struct {
	Symbols []string
}

// handleBuilderList follows the same pattern as handleChartList: pick a
// symbol already used by an existing strategy, or type one in directly to
// jump to the builder page.
func (s *Server) handleBuilderList(w http.ResponseWriter, r *http.Request) {
	if symbol := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol"))); symbol != "" {
		http.Redirect(w, r, "/builder/"+symbol, http.StatusFound)
		return
	}

	userID, _ := currentUserID(r)
	all, err := s.store.ListStrategies(r.Context(), userID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	seen := make(map[string]bool)
	var symbols []string
	for _, sc := range all {
		if !seen[sc.Symbol] {
			seen[sc.Symbol] = true
			symbols = append(symbols, sc.Symbol)
		}
	}
	sort.Strings(symbols)

	s.renderPage(w, r, i18n.T(resolveLang(r), "webui.builder.list.title"), "builder_list_content", builderListData{Symbols: symbols})
}

type builderViewData struct {
	Symbol string
	// Timeframe is the main timeframe selected when the builder first loads.
	// Supports a ?timeframe= deep link (when the strategy detail page's
	// "view in builder" link carries that strategy's timeframe, the builder
	// should show a chart on the same timeframe the moment it opens, not
	// always fall back to a hardcoded default).
	Timeframe string
	// AgentReady controls whether the "describe your trading plan in your
	// own words" natural-language entry point is shown -- it's an
	// enhancement on top of the main module/drag path that needs no LLM,
	// hidden when no model is configured, without affecting the builder's
	// own usability.
	AgentReady bool
	// InitialConfigJSON supports a ?strategy=<id> deep link (jumping here
	// from the strategy detail page's "view in builder" link, carrying a
	// strategy ID): when non-empty, it's that strategy's current config as
	// wire-format JSON, the exact same shape as wizardConfirmData.
	// ConfigJSON -- the builder page's JS uses the same applyStrategyConfig
	// to draw it onto the chart, turning modules into cards, stop-loss/
	// take-profit into draggable lines, and triggering a fresh live preview
	// for modules like support_resistance that have no fixed point using
	// its saved parameters. Left empty when not found (the strategy doesn't
	// exist / doesn't belong to the current user / the param wasn't given),
	// and the builder falls back to a blank canvas as usual, not blocking
	// the "just look at the chart" use case.
	InitialConfigJSON string
	Lang              i18n.Lang
}

// handleBuilderView renders the builder page: an independent, dark,
// immersive document. The builder serves both "just look at the chart" and
// "build a strategy" at once -- with no modules added and no rule
// described, the landing page is simply a real, draggable/zoomable candle
// chart (historically this was /chart's separate job using the TradingView
// widget on its own; having each page maintain its own "enter a symbol,
// look at the chart" entry point made no sense, so they've been merged into
// one builder entry point).
//
// ?strategy=<id> breaks the "no modules added" default, but only takes
// effect when that param is explicitly present and the strategy genuinely
// belongs to the current user -- the strategy detail page's "view in
// builder" link has only ever carried timeframe since Stage 7, so the user
// clicking it couldn't see the lines they originally drew; that's an
// experience gap, not something intentional.
func (s *Server) handleBuilderView(w http.ResponseWriter, r *http.Request) {
	symbol := strings.ToUpper(r.PathValue("symbol"))
	if !looksLikeSymbol(symbol) {
		http.NotFound(w, r)
		return
	}
	tf := types.Timeframe(r.URL.Query().Get("timeframe"))
	if !tf.Valid() {
		tf = types.TF1h
	}
	userID, _ := currentUserID(r)
	ag, _ := s.userAgent(r.Context(), userID)

	var initialConfigJSON string
	if id := r.URL.Query().Get("strategy"); looksLikeUUID(id) {
		switch sc, err := s.store.GetStrategy(r.Context(), userID, id); {
		case err == nil:
			if b, err := json.Marshal(sc); err == nil {
				initialConfigJSON = string(b)
			} else {
				s.logger.Warn("failed to serialize the strategy config, builder falling back to a blank canvas", "strategy_id", id, "err", err)
			}
		case errors.Is(err, storage.ErrNotFound):
			// The strategy doesn't exist, or doesn't belong to the current
			// user (both return the same ErrNotFound): the link may have
			// been shared by someone else, or the strategy may have been
			// deleted -- silently falling back to a blank canvas, not a
			// genuine error worth interrupting the "at least you can still
			// look at the chart" experience for.
		default:
			s.logger.Warn("failed to query the builder's initial config, falling back to a blank canvas", "strategy_id", id, "err", err)
		}
	}

	s.renderStandalone(w, r, "builder_view_page", builderViewData{
		Symbol:            symbol,
		Timeframe:         string(tf),
		AgentReady:        ag != nil,
		InitialConfigJSON: initialConfigJSON,
		Lang:              resolveLang(r),
	})
}

const maxBuilderConfigBytes = 1 << 20 // 1MiB -- a builder config could never genuinely be this large, purely a sanity guard

// handleBuilderDescribe turns the JSON assembled after dragging/tuning
// params on the builder into a StrategyConfig, validates it, generates the
// restatement text, and then renders the exact same confirmation fragment
// as the text wizard -- the two strategy-building paths converge here,
// reusing the same save/audit logic from this step onward (see
// handlers_wizard.go's confirmProposal and handleWizardConfirm).
func (s *Server) handleBuilderDescribe(w http.ResponseWriter, r *http.Request) {
	lang := resolveLang(r)
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBuilderConfigBytes+1))
	if err != nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.builder.err.read_body_failed", "error", err.Error())})
		return
	}
	if len(body) > maxBuilderConfigBytes {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.builder.err.config_too_large")})
		return
	}

	cfg, err := agent.DecodeStrategyConfigJSON(body)
	if err != nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.builder.err.bad_config_format", "error", err.Error())})
		return
	}

	// Uses the exact same registry as confirmProposal -- the error the user
	// can see at this step is identical to the validation that actually
	// runs on save, so there's never a gap where "it passed here, but got
	// rejected on save."
	if _, err := strategy.Validate(cfg, s.registry); err != nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.builder.err.validation_failed", "error", err.Error())})
		return
	}

	userID, _ := currentUserID(r)
	restatement := s.describeConfig(r.Context(), userID, cfg, lang)

	p := &agent.Proposal{
		Outcome:         agent.OutcomeConfig,
		Restatement:     restatement,
		Config:          &cfg,
		SourceUtterance: i18n.T(lang, "webui.builder.source_utterance_label"),
	}
	s.renderProposal(w, r, p, nil, "")
}

// handleBuilderTranslate is the entry point where the visual builder plugs
// into the natural-language translation layer: the user doesn't need to
// understand terms like "volume_breakout" or "WEIGHTED," just describe the
// trading plan in plain language. Once the Agent translates it into a
// config, it goes through exactly the same confirmation chain as the text
// wizard (renderProposal reuses wizard_confirm_fragment/
// wizard_clarify_fragment, asking a follow-up question on ambiguity just
// the same, rather than guessing on the user's behalf) -- the only
// difference is that the confirmation fragment embeds a config as JSON
// (wizardConfirmData.ConfigJSON), which the builder page's JS reads and
// uses to actually draw the modules, parameters, and stop-loss/take-profit
// onto the chart. What the user wants is "don't just leave it as abstract
// text, show it on the chart as much as possible," and that JSON is what
// delivers it -- not a newly written translation path.
//
// The symbol is force-locked to the builder's current SYMBOL (ForceSymbol,
// see wizardState), rather than relying on the LLM to correctly guess it
// from the user's description: the user is talking on this symbol's
// builder page, so the rule should land on this symbol -- this constraint
// carries through any clarification rounds that follow, never expiring
// just because one more question got asked.
func (s *Server) handleBuilderTranslate(w http.ResponseWriter, r *http.Request) {
	lang := resolveLang(r)
	userID, _ := currentUserID(r)
	ag, _ := s.userAgent(r.Context(), userID)
	if ag == nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.agent_not_ready")})
		return
	}
	symbol := strings.ToUpper(r.PathValue("symbol"))
	if !looksLikeSymbol(symbol) {
		http.NotFound(w, r)
		return
	}
	utterance := strings.TrimSpace(r.FormValue("utterance"))
	if utterance == "" {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.builder.err.empty_plan")})
		return
	}

	// The symbol context is given to the model not to constrain it
	// (Config.Symbol gets force-overridden in renderProposal anyway), but
	// so the restatement text reads consistently with the symbol the
	// builder actually enforces -- without this, when the user never
	// mentions a symbol at all, the model might make up an unrelated one in
	// the restatement that doesn't match what the builder force-applies.
	contextualUtterance := i18n.T(lang, "webui.builder.context_prefix", "symbol", symbol, "utterance", utterance)

	p, err := ag.Translate(r.Context(), contextualUtterance, nil, lang)
	if err != nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.translate_failed", "err", err.Error())})
		return
	}
	// Deliberately not stripping the injected symbol-context prefix here
	// (unlike an earlier version): p.SourceUtterance isn't just for
	// display -- when handleWizardClarify rebuilds the conversation
	// history, it feeds this back to the model as "what the user said in
	// the first turn" (see that function's newHistory concatenation).
	// Stripping the prefix here before, leaving only the user's original
	// words, meant that once the clarification Q&A reached a second round,
	// the model no longer knew "the symbol is already locked to this one,"
	// so it kept re-asking about the symbol every round -- a loop actually
	// reproduced with DeepSeek. The cost is that the source_utterance
	// ultimately stored in the database carries this injected text, but
	// that honestly reflects the complete input actually sent to the
	// model, which isn't a bad thing from an audit standpoint.
	s.renderProposal(w, r, p, nil, symbol)
}

// describeConfig prefers the Agent to generate the restatement; when the
// Agent isn't ready (no LLM key configured), it falls back to a
// deterministic restatement -- the visual builder's config has no
// ambiguity that needs a model to understand in the first place, so the
// fallback works just as well.
func (s *Server) describeConfig(ctx context.Context, userID string, cfg types.StrategyConfig, lang i18n.Lang) string {
	ag, _ := s.userAgent(ctx, userID)
	if ag == nil {
		return describePlain(lang, cfg)
	}
	text, err := ag.Describe(ctx, cfg, lang)
	if err != nil {
		s.logger.Warn("Agent restatement failed, falling back to the deterministic restatement", "err", err)
		return describePlain(lang, cfg)
	}
	return text
}
