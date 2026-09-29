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

// handleUnlockLive 是界面上唯一的手动流转入口：LIVE_ELIGIBLE → LIVE。
//
// 结构上直接照抄 cmd/executor/promote.go 的 checkPromotions：重新从库里取当前状态
// （不信任表单可能携带的任何"当前状态"），校验通过才落库，任何失败都回显成
// 页面上的事实性提示，不是 500——错误状态、缺操作者标识都是正常的用户输入问题。
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
		s.serverError(w, err)
		return
	}

	actorID := strings.TrimSpace(r.FormValue("actor_id"))
	reason := strings.TrimSpace(r.FormValue("reason"))

	banner, bannerErr := s.doUnlockLive(ctx, userID, sc, actorID, reason)

	data, err := s.loadStrategyDetail(ctx, userID, id)
	if err != nil {
		s.serverError(w, err)
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
