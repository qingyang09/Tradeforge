package webui

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// handleConfirmBacktest 是 DRAFT → BACKTESTED 的手动触发入口。
//
// 回测结果本身由 python/backtest 的 CLI 写库（Go 侧没有写路径，见
// internal/storage/backtests.go 顶部注释），但落库之后没有任何东西会自动把
// 策略状态往前推——阶段 5 要求的"强制流程"如果没有这一步，新策略会永远卡在
// DRAFT，模拟盘/实盘门槛形同虚设。这里补上这个手动确认动作：读取最新回测结果，
// 过一遍 strategy.CheckTransition 的样本外门槛，通过才落库。
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
			http.Error(w, "策略不存在", http.StatusNotFound)
			return
		}
		s.serverError(w, err)
		return
	}

	actorID := strings.TrimSpace(r.FormValue("actor_id"))
	reason := strings.TrimSpace(r.FormValue("reason"))

	banner, bannerErr := s.doConfirmBacktest(ctx, userID, sc, actorID, reason)

	data, err := s.loadStrategyDetail(ctx, userID, id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data.Banner, data.BannerErr = banner, bannerErr
	s.renderPage(w, r, data.Strategy.Name, "strategy_detail_content", data)
}

func (s *Server) doConfirmBacktest(ctx context.Context, userID string, sc types.StrategyConfig, actorID, reason string) (banner string, isErr bool) {
	bt, err := s.store.LatestBacktestResult(ctx, userID, sc.ID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return "尚无回测结果，无法确认。请先跑一遍 backtest-runner + tradeforge_backtest 写入结果。", true
		}
		return "读取回测结果失败：" + err.Error(), true
	}

	evidence, err := strategy.CheckTransition(strategy.TransitionRequest{
		From: sc.State, To: types.StateBacktested,
		Actor: strategy.ActorUser, ActorID: actorID, Reason: reason,
		Backtest: &bt,
	}, s.gate)
	if err != nil {
		return err.Error(), true
	}

	if err := s.store.UpdateStrategyState(ctx, userID, storage.Transition{
		StrategyID: sc.ID, From: sc.State, To: types.StateBacktested,
		Actor: "user:" + actorID, Reason: reason, Evidence: evidence,
	}); err != nil {
		return "推进失败：" + err.Error(), true
	}
	return "回测结果已确认，策略进入 BACKTESTED。", false
}

// handleStartPaperTrading 是 BACKTESTED → PAPER_TRADING 的手动触发入口。
//
// 这一步状态机本身没有数据门槛（见 statemachine.go 的默认分支），但同样需要
// 一个真实存在的触发动作——cmd/executor 只会去加载已经处于 PAPER_TRADING 的
// 策略，如果没有东西把策略从 BACKTESTED 推过去，执行层永远看不到它。
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
			http.Error(w, "策略不存在", http.StatusNotFound)
			return
		}
		s.serverError(w, err)
		return
	}

	actorID := strings.TrimSpace(r.FormValue("actor_id"))
	reason := strings.TrimSpace(r.FormValue("reason"))

	banner, bannerErr := s.doStartPaperTrading(ctx, userID, sc, actorID, reason)

	data, err := s.loadStrategyDetail(ctx, userID, id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data.Banner, data.BannerErr = banner, bannerErr
	s.renderPage(w, r, data.Strategy.Name, "strategy_detail_content", data)
}

func (s *Server) doStartPaperTrading(ctx context.Context, userID string, sc types.StrategyConfig, actorID, reason string) (banner string, isErr bool) {
	evidence, err := strategy.CheckTransition(strategy.TransitionRequest{
		From: sc.State, To: types.StatePaperTrading,
		Actor: strategy.ActorUser, ActorID: actorID, Reason: reason,
	}, s.gate)
	if err != nil {
		return err.Error(), true
	}

	if err := s.store.UpdateStrategyState(ctx, userID, storage.Transition{
		StrategyID: sc.ID, From: sc.State, To: types.StatePaperTrading,
		Actor: "user:" + actorID, Reason: reason, Evidence: evidence,
	}); err != nil {
		return "推进失败：" + err.Error(), true
	}
	return "已进入模拟盘。下一次启动 cmd/executor 会加载这个策略。", false
}

// handleDeleteStrategy 永久删除一条策略。
//
// 刻意只允许 DRAFT 状态：一旦进入 BACKTESTED 及以后，策略就带上了真实的回测/
// 模拟盘/实盘历史，删除会级联删掉这些记录（schema 里的 ON DELETE CASCADE），
// 而"任何一笔决策都要能追溯"是这个平台的一贯要求，不能因为一次误删就断掉审计链。
// DRAFT 阶段还没有这些历史，删掉的只是一份"还没定稿的草稿"。
//
// 没有走 actor_id/reason 这套审计字段——那套是给"状态推进"这种事后还查得到记录
// 的操作用的，删除之后连这条策略本身都不存在了，记一个理由没有地方能查，纯粹是
// 走个形式；真正的防误删手段是确认按钮本身要求一次浏览器原生确认（见模板）。
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
			http.Error(w, "策略不存在", http.StatusNotFound)
			return
		}
		s.serverError(w, err)
		return
	}

	if sc.State != types.StateDraft {
		data, err := s.loadStrategyDetail(ctx, userID, id)
		if err != nil {
			s.serverError(w, err)
			return
		}
		data.Banner = "只能删除 DRAFT 状态的策略，当前状态是 " + string(sc.State) + "。"
		data.BannerErr = true
		s.renderPage(w, r, data.Strategy.Name, "strategy_detail_content", data)
		return
	}

	if err := s.store.DeleteStrategy(ctx, userID, id); err != nil {
		s.serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}
