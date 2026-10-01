package webui

import (
	"context"
	"errors"
	"net/http"

	"tradeforge/internal/i18n"
	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

type strategyDetailData struct {
	Strategy       types.StrategyConfig
	Transitions    []storage.Transition
	Decisions      []types.Decision
	Orders         []types.Order
	Backtest       *types.BacktestResult
	Chart          equityChart
	MonthlyReturns []monthlyReturn
	PaperStats     *strategy.PaperStats
	// GateProgress is the data the "what's still needed for the next step"
	// card renders -- it lists strategy.Gate's criteria one by one with
	// current value/required value/pass-or-not, rather than waiting until
	// the user clicks "enter paper trading"/"unlock live" to report what
	// failed after the fact in a Banner.
	GateProgress gateProgress
	// CanConfirmBacktest/CanStartPaperTrading/CanUnlockLive only reflect
	// "the current state allows attempting this step," not that the data
	// gate is necessarily already cleared -- gate validation happens on
	// submit, and a failure surfaces its specific reason as a Banner, the
	// same UX as "unlock live."
	CanConfirmBacktest   bool
	CanStartPaperTrading bool
	CanUnlockLive        bool
	// Banner is action feedback (e.g. the result of unlocking live); empty on
	// an ordinary GET request.
	Banner    types.Message
	BannerErr bool
}

func (s *Server) handleStrategyDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !looksLikeUUID(id) {
		http.NotFound(w, r)
		return
	}
	userID, _ := currentUserID(r)
	data, err := s.loadStrategyDetail(r.Context(), userID, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, i18n.T(resolveLang(r), "webui.strategy_detail.not_found"), http.StatusNotFound)
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.renderPage(w, r, data.Strategy.Name, "strategy_detail_content", data)
}

// loadStrategyDetail gathers all the data the detail page needs.
//
// A missing backtest result, or never having entered paper trading, are both
// not errors -- each is represented by a nil field meaning "doesn't exist
// yet." An error only propagates up when the strategy itself doesn't exist
// (or doesn't belong to the current user, both returning the same
// ErrNotFound).
func (s *Server) loadStrategyDetail(ctx context.Context, userID, id string) (strategyDetailData, error) {
	sc, err := s.store.GetStrategy(ctx, userID, id)
	if err != nil {
		return strategyDetailData{}, err
	}

	transitions, err := s.store.ListTransitions(ctx, userID, id)
	if err != nil {
		return strategyDetailData{}, err
	}
	decisions, err := s.store.ListDecisions(ctx, userID, id, 50)
	if err != nil {
		return strategyDetailData{}, err
	}
	orders, err := s.store.ListOrders(ctx, userID, id, 50)
	if err != nil {
		return strategyDetailData{}, err
	}

	data := strategyDetailData{
		Strategy:             sc,
		Transitions:          transitions,
		Decisions:            decisions,
		Orders:               orders,
		CanConfirmBacktest:   sc.State == types.StateDraft,
		CanStartPaperTrading: sc.State == types.StateBacktested,
		CanUnlockLive:        sc.State == types.StateLiveEligible,
	}

	switch bt, err := s.store.LatestBacktestResult(ctx, userID, id); {
	case err == nil:
		data.Backtest = &bt
		data.Chart = buildEquityChart(bt)
		data.MonthlyReturns = buildMonthlyReturns(bt.EquityCurve)
	case errors.Is(err, storage.ErrNotFound):
		// Never backtested yet; the template itself shows "no backtest result yet."
	default:
		return strategyDetailData{}, err
	}

	switch ps, err := s.store.PaperStats(ctx, userID, id); {
	case err == nil:
		data.PaperStats = &ps
	case errors.Is(err, storage.ErrNotFound):
		// Never entered paper trading; the template simply skips this section.
	default:
		return strategyDetailData{}, err
	}

	data.GateProgress = buildGateProgress(s.gate, sc.State, data.Backtest, data.PaperStats)

	return data, nil
}

// gateProgressItem is one row in the "what's still needed for the next step"
// table.
//
// Label/Current/Required are types.Message (key+args), not ready-made
// strings -- strategy.GateCriterion was changed to a translatable structured
// message, so the template has to render it through a call like
// {{msg .Label}}, not {{.Label}} directly anymore. render.go's msg func
// renders in whatever language this request's tf_lang cookie resolves to
// (see lang.go's resolveLang); this struct itself needs no further changes.
type gateProgressItem struct {
	Label    types.Message
	Current  types.Message
	Required types.Message
	Pass     bool
}

// gateProgress is the data the strategy detail page's "what's still needed
// for the next step" card renders.
type gateProgress struct {
	// TargetLabel describes the target this step would advance to. A zero
	// value (IsZero) means the current state has no "next step" at all (LIVE
	// can only be manually paused, SUSPENDED has to go back to paper trading
	// first), and the template decides whether to render the whole card
	// based on that.
	TargetLabel types.Message
	// Items is non-empty only when there's concrete gate data to compare
	// item-by-item; when empty, look at Note instead.
	Items   []gateProgressItem
	AllPass bool
	// Note covers two cases: this step has no data gate at all (e.g.
	// BACKTESTED -> PAPER_TRADING); or a gate exists but there's no data yet
	// to evaluate it against (e.g. no backtest has run yet).
	Note types.Message
}

// buildGateProgress translates strategy.Gate's criteria into the "current
// value vs. required value" list the interface can render directly -- it
// uses the exact same EvaluateBacktestGate/EvaluatePaperGate computation that
// strategy.CheckTransition actually uses to allow/reject, so the interface
// can never say "all criteria met" only for the real transition attempt to
// get rejected.
func buildGateProgress(gate strategy.Gate, state types.StrategyState, bt *types.BacktestResult, ps *strategy.PaperStats) gateProgress {
	// The copy here deliberately doesn't repeat the action forms' titles
	// below verbatim ("enter paper trading"/"unlock live"/"confirm backtest
	// result"), describing only "what condition must be met" objectively --
	// this avoids a future test using strings.Contains to check whether some
	// action form is present on the page accidentally matching this card's
	// descriptive text and falsely concluding "the form showed up."
	switch state {
	case types.StateDraft:
		if bt == nil {
			return gateProgress{
				TargetLabel: types.Msg("webui.strategy_detail.gate.target.backtest"),
				Note:        types.Msg("webui.strategy_detail.gate.note.no_backtest_yet"),
			}
		}
		return criteriaToProgress(types.Msg("webui.strategy_detail.gate.target.backtest"), strategy.EvaluateBacktestGate(bt, gate))

	case types.StatePaperTrading:
		if ps == nil {
			return gateProgress{
				TargetLabel: types.Msg("webui.strategy_detail.gate.target.paper"),
				Note:        types.Msg("webui.strategy_detail.gate.note.no_paper_stats_yet"),
			}
		}
		return criteriaToProgress(types.Msg("webui.strategy_detail.gate.target.paper"), strategy.EvaluatePaperGate(ps, gate))

	case types.StateBacktested:
		return gateProgress{
			TargetLabel: types.Msg("webui.strategy_detail.gate.target.start_paper"),
			Note:        types.Msg("webui.strategy_detail.gate.note.no_gate_start_paper"),
		}

	case types.StateLiveEligible:
		return gateProgress{
			TargetLabel: types.Msg("webui.strategy_detail.gate.target.live"),
			Note:        types.Msg("webui.strategy_detail.gate.note.no_gate_live"),
		}

	default:
		// LIVE: can only be manually paused, no "next step." SUSPENDED: must
		// go back to paper trading to re-validate first, which is the same
		// gate as PAPER_TRADING, so it isn't shown again separately.
		return gateProgress{}
	}
}

func criteriaToProgress(targetLabel types.Message, criteria []strategy.GateCriterion) gateProgress {
	items := make([]gateProgressItem, len(criteria))
	allPass := true
	for i, c := range criteria {
		items[i] = gateProgressItem{Label: c.Label, Current: c.Current, Required: c.Required, Pass: c.Pass}
		if !c.Pass {
			allPass = false
		}
	}
	return gateProgress{TargetLabel: targetLabel, Items: items, AllPass: allPass}
}
