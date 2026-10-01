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

// handleUnlockLive is the UI's one and only manual transition entry point: LIVE_ELIGIBLE → LIVE.
//
// Structurally a direct copy of cmd/executor/promote.go's checkPromotions:
// re-fetch the current state from the database (never trust any "current
// state" the form might carry), write only once validation passes, and
// every failure echoes back as a factual on-page notice, not a 500 -- a
// wrong state or a missing operator ID are ordinary user-input problems.
func (s *Server) handleUnlockLive(w http.ResponseWriter, r *http.Request) {
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

	banner, bannerErr := s.doUnlockLive(ctx, userID, sc, actorID, reason)

	data, err := s.loadStrategyDetail(ctx, userID, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data.Banner, data.BannerErr = banner, bannerErr
	s.renderPage(w, r, data.Strategy.Name, "strategy_detail_content", data)
}

func (s *Server) doUnlockLive(ctx context.Context, userID string, sc types.StrategyConfig, actorID, reason string) (banner types.Message, isErr bool) {
	evidence, err := strategy.CheckTransition(strategy.TransitionRequest{
		From: sc.State, To: types.StateLive,
		Actor: strategy.ActorUser, ActorID: actorID, Reason: reason,
	}, s.gate)
	if err != nil {
		return transitionErrorMessage(err), true
	}

	if err := s.store.UpdateStrategyState(ctx, userID, storage.Transition{
		StrategyID: sc.ID, From: sc.State, To: types.StateLive,
		Actor: "user:" + actorID, Reason: reason, Evidence: evidence,
	}); err != nil {
		return types.Msg("webui.strategy_detail.banner.update_state_failed", "err", err.Error()), true
	}
	return types.Msg("webui.strategy_detail.banner.unlock_live_success"), false
}
