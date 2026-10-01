package webui

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"tradeforge/internal/agent"
	"tradeforge/internal/i18n"
	"tradeforge/internal/marketdata/okx"
	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// defaultBatchScanCount/maxBatchScanCount decide how many symbols a single
// batch scan can process at most -- without a cap, a user entering a huge
// number would trigger backtests against a pile of nobody-cares-about
// low-volume symbols, and also make the confirmation page's symbol list
// unreadably long. 50 is a cap that comfortably covers "top few dozen by
// volume" without letting things run away, mirroring
// internal/marketdata/okx/client.go's maxCandlesPerRequest clamp-not-error
// style.
const (
	defaultBatchScanCount = 20
	maxBatchScanCount     = 50
)

type batchStartData struct {
	AgentReady bool
	DefaultN   int
	MaxN       int
}

func (s *Server) handleBatchScanStart(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	ag, _ := s.userAgent(r.Context(), userID)
	s.renderPage(w, r, i18n.T(resolveLang(r), "webui.batch.start.title"), "batch_start_content", batchStartData{
		AgentReady: ag != nil, DefaultN: defaultBatchScanCount, MaxN: maxBatchScanCount,
	})
}

type batchConfirmData struct {
	Restatement string
	Config      types.StrategyConfig
	Symbols     []string
	State       string
}

// Reason is a plain string, not types.Message: it's already-rendered text by
// the time it lands here (either prepareDraftConfig's error, itself built
// from i18n.T against this request's language, or an i18n.T call right at
// the assignment site below) -- there's no symbolic key left to carry.
type batchResultRow struct {
	Symbol     string
	StrategyID string
	Skipped    bool
	Reason     string
}

type batchResultData struct {
	Success    bool
	Message    types.Message
	Created    int
	SkippedCnt int
	Rows       []batchResultRow
}

// handleBatchScanTranslate is the batch-scan entry point: first narrow down
// a set of symbols by 24-hour volume, then translate the user's described
// rule into a config template (a single LLM call, not one question per
// symbol) -- actually materializing a DRAFT for each symbol only happens
// once the user has reviewed the full symbol list on the confirmation page
// and clicked "confirm" (handleBatchScanConfirm); this translation step
// itself writes nothing to the database.
func (s *Server) handleBatchScanTranslate(w http.ResponseWriter, r *http.Request) {
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
	n, err := parseBatchCount(lang, r.FormValue("count"))
	if err != nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: err.Error()})
		return
	}

	symbols, err := s.scanTopSymbols(r.Context(), n)
	if err != nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.batch.err.scan_failed", "err", err.Error())})
		return
	}
	if len(symbols) == 0 {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.batch.err.scan_empty")})
		return
	}

	p, err := ag.Translate(r.Context(), batchContextualUtterance(lang, len(symbols), utterance), nil, lang)
	if err != nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.translate_failed", "err", err.Error())})
		return
	}
	s.renderBatchProposal(w, r, p, nil, symbols)
}

// batchContextualUtterance prepends a context note to the user's raw
// description, telling the model that this rule will apply to several
// different symbols and it doesn't need to worry about the symbol field --
// without this, prompt.go's "didn't say which symbol to trade" rule would
// make the model keep asking about a sentence that deliberately omits any
// symbol. Mirrors the same pattern handlers_builder.go's
// handleBuilderTranslate uses to inject the builder's chart-symbol context.
func batchContextualUtterance(lang i18n.Lang, symbolCount int, utterance string) string {
	return i18n.T(lang, "webui.batch.context_prefix", "count", symbolCount, "utterance", utterance)
}

func (s *Server) handleBatchScanClarify(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	ag, _ := s.userAgent(r.Context(), userID)
	lang := resolveLang(r)
	if ag == nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.agent_not_ready")})
		return
	}
	ws, err := decodeState(r.FormValue("state"))
	if err != nil || ws.Proposal == nil || len(ws.BatchSymbols) == 0 {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.session_expired")})
		return
	}
	answer := strings.TrimSpace(r.FormValue("answer"))
	if answer == "" {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.empty_answer")})
		return
	}

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
	// The scanned symbol list stays fixed across the whole multi-round
	// conversation, never re-scanning -- the same reasoning as ForceSymbol
	// never expiring across clarification rounds (see
	// wizardState.BatchSymbols's doc comment).
	s.renderBatchProposal(w, r, p, newHistory, ws.BatchSymbols)
}

// renderBatchProposal is renderProposal's batch counterpart: when
// NeedsClarification, it renders the batch-specific clarification fragment
// (posts to /wizard/batch/clarify); otherwise it renders a confirmation
// fragment carrying the full symbol list, without showing the single
// Config.Symbol -- that's just a placeholder from the translation step and
// has no meaning in a batch context.
func (s *Server) renderBatchProposal(w http.ResponseWriter, r *http.Request, p *agent.Proposal, history []agent.Turn, symbols []string) {
	state, err := encodeState(wizardState{History: history, Proposal: p, BatchSymbols: symbols})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if p.NeedsClarification() {
		s.renderFragment(w, r, "batch_clarify_fragment", wizardClarifyData{Questions: p.Questions, State: state})
		return
	}
	s.renderFragment(w, r, "batch_confirm_fragment",
		batchConfirmData{Restatement: p.Restatement, Config: *p.Config, Symbols: symbols, State: state})
}

// handleBatchScanConfirm applies the confirmed config template separately to
// each scanned symbol, producing an independent DRAFT StrategyConfig for
// each -- one symbol's validation failure must not take down the others
// (mirroring handleBulkDeleteStrategies's isolate-and-tally approach). Once
// saved, each follows the exact same path (backtest/paper trading/advance/
// manual live unlock) as a manually-built strategy, with no special-casing.
func (s *Server) handleBatchScanConfirm(w http.ResponseWriter, r *http.Request) {
	lang := resolveLang(r)
	ws, err := decodeState(r.FormValue("state"))
	if err != nil || ws.Proposal == nil || ws.Proposal.Config == nil || len(ws.BatchSymbols) == 0 {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.session_expired")})
		return
	}

	if r.FormValue("decision") != "confirm" {
		s.renderFragment(w, r, "batch_result_fragment",
			batchResultData{Success: false, Message: types.Msg("webui.batch.result.cancelled")})
		return
	}

	userID, _ := currentUserID(r)
	ctx := r.Context()
	var rows []batchResultRow
	created := 0
	for _, symbol := range ws.BatchSymbols {
		// Shallow copy: Modules' Params is a map, so it shares underlying
		// data with the other N-1 clones, but the loop only ever reads it,
		// never writes, so nothing cross-contaminates -- never mutate
		// cfg.Modules[i].Params inside this loop.
		cfg := *ws.Proposal.Config
		cfg.Symbol = symbol
		cfg.ID = idgen.NewUUID()
		cfg.UserID = userID

		prepared, err := s.prepareDraftConfig(lang, cfg, ws.Proposal.SourceUtterance)
		if err != nil {
			rows = append(rows, batchResultRow{Symbol: symbol, Skipped: true, Reason: err.Error()})
			continue
		}
		if err := s.store.SaveStrategy(ctx, prepared); err != nil {
			rows = append(rows, batchResultRow{Symbol: symbol, Skipped: true,
				Reason: i18n.T(lang, "webui.wizard.result.save_failed", "err", err.Error())})
			continue
		}
		if err := s.store.RecordTransition(ctx, storage.Transition{
			StrategyID: prepared.ID, From: "", To: types.StateDraft,
			Actor:  "user:web",
			Reason: i18n.T(lang, "webui.batch.transition.batch_confirmed", "count", len(ws.BatchSymbols)),
			Evidence: map[string]any{
				"source_utterance": prepared.SourceUtterance, "batch_size": len(ws.BatchSymbols),
			},
		}); err != nil {
			// An audit-record write failure doesn't roll back the
			// already-saved strategy -- same reasoning as handleWizardConfirm.
			s.logger.Error("failed to write audit record", "strategy_id", prepared.ID, "symbol", symbol, "err", err)
		}
		rows = append(rows, batchResultRow{Symbol: symbol, StrategyID: prepared.ID})
		created++
	}

	skipped := len(rows) - created
	s.renderFragment(w, r, "batch_result_fragment", batchResultData{
		Success: created > 0, Created: created, SkippedCnt: skipped, Rows: rows,
		Message: types.Msg("webui.batch.result.success", "created", created, "skipped", skipped),
	})
}

// parseBatchCount parses and clamps the scan count the user entered --
// invalid input is rejected outright, but exceeding the cap is silently
// clamped down to it (mirroring the okx package's handling of limit in
// FetchCandles), not rejected as an error.
func parseBatchCount(lang i18n.Lang, raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultBatchScanCount, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0, errors.New(i18n.T(lang, "webui.batch.err.invalid_count"))
	}
	if n > maxBatchScanCount {
		n = maxBatchScanCount
	}
	return n, nil
}

// scanTopSymbols fetches a market-wide spot ticker snapshot and takes the top
// n symbols sorted by 24-hour quote volume, descending.
func (s *Server) scanTopSymbols(ctx context.Context, n int) ([]string, error) {
	tickers, err := s.okxClient.ListTickers(ctx)
	if err != nil {
		return nil, err
	}
	return topSymbolsByVolume(tickers, n), nil
}

// topSymbolsByVolume is a pure function, making the sort/truncate logic easy
// to test in isolation, with no network dependency.
func topSymbolsByVolume(tickers []okx.Ticker, n int) []string {
	if n <= 0 || len(tickers) == 0 {
		return nil
	}
	sorted := make([]okx.Ticker, len(tickers))
	copy(sorted, tickers)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Vol24hQuote.GreaterThan(sorted[j].Vol24hQuote)
	})
	if n > len(sorted) {
		n = len(sorted)
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = sorted[i].Symbol
	}
	return out
}
