package webui

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"tradeforge/internal/i18n"
	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// transitionErrorMessage extracts the structured, translatable rejection
// reason from a strategy.CheckTransition error. CheckTransition never
// returns a bare error -- every rejection path in statemachine.go builds a
// *strategy.TransitionError, whose Reason field is already the types.Message
// meant to be shown to the user (see TransitionError's doc comment) -- so
// this is not a defensive "maybe", it just names that lookup once for the
// three handlers below instead of repeating the type assertion in each.
// The fallback only matters if that invariant is ever broken.
func transitionErrorMessage(err error) types.Message {
	var terr *strategy.TransitionError
	if errors.As(err, &terr) {
		return terr.Reason
	}
	return types.Msg("webui.strategy_detail.banner.transition_rejected", "err", err.Error())
}

// handleConfirmBacktest is the manual trigger for DRAFT -> BACKTESTED.
//
// The backtest result itself is written by python/backtest's CLI (there's no
// write path on the Go side, see internal/storage/backtests.go's top-of-file
// comment), but nothing automatically advances the strategy's state once
// that's persisted -- without this step, Stage 5's required "mandatory
// pipeline" would leave every new strategy stuck in DRAFT forever, making the
// paper/live gates meaningless. This supplies that manual confirmation
// action: read the latest backtest result, run it through
// strategy.CheckTransition's out-of-sample gate, and only persist on a pass.
func (s *Server) handleConfirmBacktest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !looksLikeUUID(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	userID, _ := currentUserID(r)

	sc, err := s.store.GetStrategy(ctx, userID, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, i18n.T(resolveLang(r), "webui.strategy_detail.not_found"), http.StatusNotFound)
			return
		}
		s.serverError(w, r, err)
		return
	}

	actorID := strings.TrimSpace(r.FormValue("actor_id"))
	reason := strings.TrimSpace(r.FormValue("reason"))

	banner, bannerErr := s.doConfirmBacktest(ctx, userID, sc, actorID, reason)

	data, err := s.loadStrategyDetail(ctx, userID, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data.Banner, data.BannerErr = banner, bannerErr
	s.renderPage(w, r, data.Strategy.Name, "strategy_detail_content", data)
}

func (s *Server) doConfirmBacktest(ctx context.Context, userID string, sc types.StrategyConfig, actorID, reason string) (banner types.Message, isErr bool) {
	bt, err := s.store.LatestBacktestResult(ctx, userID, sc.ID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return types.Msg("webui.strategy_detail.banner.no_backtest_result"), true
		}
		return types.Msg("webui.strategy_detail.banner.read_backtest_failed", "err", err.Error()), true
	}

	evidence, err := strategy.CheckTransition(strategy.TransitionRequest{
		From: sc.State, To: types.StateBacktested,
		Actor: strategy.ActorUser, ActorID: actorID, Reason: reason,
		Backtest: &bt,
	}, s.gate)
	if err != nil {
		return transitionErrorMessage(err), true
	}

	if err := s.store.UpdateStrategyState(ctx, userID, storage.Transition{
		StrategyID: sc.ID, From: sc.State, To: types.StateBacktested,
		Actor: "user:" + actorID, Reason: reason, Evidence: evidence,
	}); err != nil {
		return types.Msg("webui.strategy_detail.banner.update_state_failed", "err", err.Error()), true
	}
	return types.Msg("webui.strategy_detail.banner.confirm_backtest_success"), false
}

// handleStartPaperTrading is the manual trigger for BACKTESTED ->
// PAPER_TRADING.
//
// This step has no data gate in the state machine itself (see
// statemachine.go's default branch), but still needs a real, actual trigger
// action -- cmd/executor only loads strategies already in PAPER_TRADING, so
// without something to push a strategy from BACKTESTED across, the execution
// layer would never see it.
func (s *Server) handleStartPaperTrading(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !looksLikeUUID(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	userID, _ := currentUserID(r)

	sc, err := s.store.GetStrategy(ctx, userID, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, i18n.T(resolveLang(r), "webui.strategy_detail.not_found"), http.StatusNotFound)
			return
		}
		s.serverError(w, r, err)
		return
	}

	actorID := strings.TrimSpace(r.FormValue("actor_id"))
	reason := strings.TrimSpace(r.FormValue("reason"))

	banner, bannerErr := s.doStartPaperTrading(ctx, userID, sc, actorID, reason)

	data, err := s.loadStrategyDetail(ctx, userID, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data.Banner, data.BannerErr = banner, bannerErr
	s.renderPage(w, r, data.Strategy.Name, "strategy_detail_content", data)
}

func (s *Server) doStartPaperTrading(ctx context.Context, userID string, sc types.StrategyConfig, actorID, reason string) (banner types.Message, isErr bool) {
	evidence, err := strategy.CheckTransition(strategy.TransitionRequest{
		From: sc.State, To: types.StatePaperTrading,
		Actor: strategy.ActorUser, ActorID: actorID, Reason: reason,
	}, s.gate)
	if err != nil {
		return transitionErrorMessage(err), true
	}

	if err := s.store.UpdateStrategyState(ctx, userID, storage.Transition{
		StrategyID: sc.ID, From: sc.State, To: types.StatePaperTrading,
		Actor: "user:" + actorID, Reason: reason, Evidence: evidence,
	}); err != nil {
		return types.Msg("webui.strategy_detail.banner.update_state_failed", "err", err.Error()), true
	}
	return types.Msg("webui.strategy_detail.banner.start_paper_success"), false
}

// handleDeleteStrategy permanently deletes a strategy.
//
// Deliberately only allowed in the DRAFT state: once a strategy reaches
// BACKTESTED or beyond, it carries real backtest/paper/live history, and
// deleting it would cascade-delete those records (ON DELETE CASCADE in the
// schema) -- "every decision must stay traceable" is a standing requirement
// of this platform, and a single accidental delete must never be able to
// break that audit chain. DRAFT hasn't accumulated any of this history yet,
// so deleting it only removes a "draft that was never finalized."
//
// This doesn't go through the actor_id/reason audit fields -- those exist for
// operations like "state advance" where a record is still there to look up
// afterward; once deleted, the strategy itself no longer exists, so recording
// a reason would have nowhere to be looked up and would be purely
// decorative. The real protection against accidental deletion is that the
// confirm button itself requires a native browser confirmation (see the
// template).
func (s *Server) handleDeleteStrategy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !looksLikeUUID(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	userID, _ := currentUserID(r)

	sc, err := s.store.GetStrategy(ctx, userID, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, i18n.T(resolveLang(r), "webui.strategy_detail.not_found"), http.StatusNotFound)
			return
		}
		s.serverError(w, r, err)
		return
	}

	if sc.State != types.StateDraft {
		data, err := s.loadStrategyDetail(ctx, userID, id)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data.Banner = types.Msg("webui.strategy_detail.banner.delete_wrong_state", "state", string(sc.State))
		data.BannerErr = true
		s.renderPage(w, r, data.Strategy.Name, "strategy_detail_content", data)
		return
	}

	if err := s.store.DeleteStrategy(ctx, userID, id); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}
