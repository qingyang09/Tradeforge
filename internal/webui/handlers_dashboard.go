package webui

import (
	"context"
	"net/http"

	"tradeforge/internal/i18n"
	"tradeforge/pkg/types"
)

type dashboardGroup struct {
	State      types.StrategyState
	Strategies []types.StrategyConfig
}

type dashboardData struct {
	Groups []dashboardGroup
	Total  int
	// Banner is the result feedback from a bulk operation (currently only
	// bulk delete); empty on an ordinary GET request.
	Banner    types.Message
	BannerErr bool
}

func (s *Server) loadDashboardData(ctx context.Context, userID string) (dashboardData, error) {
	all, err := s.store.ListStrategies(ctx, userID)
	if err != nil {
		return dashboardData{}, err
	}

	byState := make(map[types.StrategyState][]types.StrategyConfig)
	for _, sc := range all {
		byState[sc.State] = append(byState[sc.State], sc)
	}

	data := dashboardData{Total: len(all)}
	for _, st := range types.AllStates {
		data.Groups = append(data.Groups, dashboardGroup{State: st, Strategies: byState[st]})
	}
	return data, nil
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	// "GET /" is net/http's ServeMux catch-all pattern: any GET request
	// with no more specific match (including a write-operation path hit
	// with the wrong method) falls through to here, so non-root paths
	// must be explicitly excluded, or a bad request would be silently
	// rendered as the dashboard with a 200 instead of a 404.
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	userID, _ := currentUserID(r)
	data, err := s.loadDashboardData(r.Context(), userID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPage(w, r, i18n.T(resolveLang(r), "webui.dashboard.title"), "dashboard_content", data)
}

// handleBulkDeleteStrategies deletes every checked strategy on the
// dashboard -- checkboxes only ever appear in the DRAFT group (see
// dashboard.html), but this still validates each one's state individually
// rather than trusting the frontend: the same "DRAFT only" rule as single
// delete (handleDeleteStrategy), checked twice over, so nobody can bypass
// the UI by directly submitting a non-DRAFT id.
func (s *Server) handleBulkDeleteStrategies(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.serverError(w, r, err)
		return
	}
	userID, _ := currentUserID(r)
	lang := resolveLang(r)
	ids := r.Form["ids"]
	ctx := r.Context()

	deleted, skipped := 0, 0
	var firstSkipReason string
	for _, id := range ids {
		sc, err := s.store.GetStrategy(ctx, userID, id)
		if err != nil {
			skipped++
			if firstSkipReason == "" {
				firstSkipReason = i18n.T(lang, "webui.dashboard.skip_reason_not_found", "id", id)
			}
			continue
		}
		if sc.State != types.StateDraft {
			skipped++
			if firstSkipReason == "" {
				firstSkipReason = i18n.T(lang, "webui.dashboard.skip_reason_not_draft", "name", sc.Name, "state", string(sc.State))
			}
			continue
		}
		if err := s.store.DeleteStrategy(ctx, userID, id); err != nil {
			s.serverError(w, r, err)
			return
		}
		deleted++
	}

	data, err := s.loadDashboardData(ctx, userID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	switch {
	case len(ids) == 0:
		data.Banner, data.BannerErr = types.Msg("webui.dashboard.bulk_delete_none_selected"), true
	case skipped == 0:
		data.Banner = types.Msg("webui.dashboard.bulk_delete_success", "deleted", deleted)
	default:
		etc := ""
		if skipped > 1 {
			etc = i18n.T(lang, "webui.dashboard.bulk_delete_etc_suffix")
		}
		data.Banner = types.Msg("webui.dashboard.bulk_delete_partial",
			"deleted", deleted, "skipped", skipped, "reason", firstSkipReason, "etc", etc)
		data.BannerErr = deleted == 0
	}
	s.renderPage(w, r, i18n.T(lang, "webui.dashboard.title"), "dashboard_content", data)
}
