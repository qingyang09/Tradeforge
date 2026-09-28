package webui

import (
	"context"
	"fmt"
	"net/http"

	"tradeforge/pkg/types"
)

type dashboardGroup struct {
	State      types.StrategyState
	Strategies []types.StrategyConfig
}

type dashboardData struct {
	Groups []dashboardGroup
	Total  int
	// Banner 是批量操作（目前只有批量删除）的结果反馈，普通 GET 请求下为空。
	Banner    string
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
	// "GET /" 在 net/http 的 ServeMux 里是兜底模式：任何没有更具体匹配的 GET 请求
	// （包括方法用错的写操作路径）都会落到这里，必须显式排除非根路径，
	// 否则错误请求会被悄悄当成看板渲染成 200，而不是 404。
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	userID, _ := currentUserID(r)
	data, err := s.loadDashboardData(r.Context(), userID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderPage(w, r, "状态看板", "dashboard_content", data)
}

// handleBulkDeleteStrategies 批量删除看板上勾选的策略——只在 DRAFT 分组里才有
// 勾选框（见 dashboard.html），但这里仍然逐个校验状态，不信任前端：跟单个删除
// （handleDeleteStrategy）用的是同一条"只能删 DRAFT"规则，双重把关，避免有人
// 直接拼一个非 DRAFT 的 id 绕过界面提交。
func (s *Server) handleBulkDeleteStrategies(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.serverError(w, err)
		return
	}
	userID, _ := currentUserID(r)
	ids := r.Form["ids"]
	ctx := r.Context()

	deleted, skipped := 0, 0
	var firstSkipReason string
	for _, id := range ids {
		sc, err := s.store.GetStrategy(ctx, userID, id)
		if err != nil {
			skipped++
			if firstSkipReason == "" {
				firstSkipReason = fmt.Sprintf("策略 %s 不存在或已被删除", id)
			}
			continue
		}
		if sc.State != types.StateDraft {
			skipped++
			if firstSkipReason == "" {
				firstSkipReason = fmt.Sprintf("《%s》当前是 %s 状态，不是 DRAFT，跳过", sc.Name, sc.State)
			}
			continue
		}
		if err := s.store.DeleteStrategy(ctx, userID, id); err != nil {
			s.serverError(w, err)
			return
		}
		deleted++
	}

	data, err := s.loadDashboardData(ctx, userID)
	if err != nil {
		s.serverError(w, err)
		return
	}
	switch {
	case len(ids) == 0:
		data.Banner, data.BannerErr = "没有选中任何策略。", true
	case skipped == 0:
		data.Banner = fmt.Sprintf("已删除 %d 条策略。", deleted)
	default:
		data.Banner = fmt.Sprintf("已删除 %d 条策略，跳过 %d 条（%s%s）。",
			deleted, skipped, firstSkipReason, func() string {
				if skipped > 1 {
					return "等"
				}
				return ""
			}())
		data.BannerErr = deleted == 0
	}
	s.renderPage(w, r, "状态看板", "dashboard_content", data)
}
