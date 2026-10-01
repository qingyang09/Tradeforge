package webui

import (
	"context"
	"errors"
	"fmt"
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

// defaultBatchScanCount/maxBatchScanCount 决定批量扫描一次最多处理多少个标的——
// 不设上限的话，用户一次填个很大的数字会打一堆没人关心的冷门标的的回测、也会让确认页面
// 的标的列表长到没法看。50 是一个足够覆盖"成交量前几十名"这个需求、又不至于失控的上限，
// 仿 internal/marketdata/okx/client.go 的 maxCandlesPerRequest 的 clamp-不报错风格。
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

// handleBatchScanTranslate 是批量扫描的入口：先按 24 小时成交量圈定一批标的，再把用户
// 描述的规则翻译成配置模板（只调一次 LLM，不是对每个标的分别问一次）——具体落到每个标的
// 各自一份 DRAFT，要等用户在确认页面看过完整标的列表、点了"确认"才会发生
// （handleBatchScanConfirm），翻译这一步本身不写库。
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

	p, err := ag.Translate(r.Context(), batchContextualUtterance(len(symbols), utterance), nil, lang)
	if err != nil {
		s.renderFragment(w, r, "wizard_error_fragment", wizardErrorData{Message: i18n.T(lang, "webui.wizard.err.translate_failed", "err", err.Error())})
		return
	}
	s.renderBatchProposal(w, r, p, nil, symbols)
}

// batchContextualUtterance 给用户的原始描述前面拼一句上下文提示，告诉模型这条规则会
// 套用到多个不同标的、标的字段不用操心——不这样做的话，prompt.go 里"没说清楚交易哪个
// 标的"这条规则会让模型对着一句故意不提标的的话一直追问。写法照抄
// handlers_builder.go 的 handleBuilderTranslate 给可视化建策注入画板标的上下文的模式。
func batchContextualUtterance(symbolCount int, utterance string) string {
	return fmt.Sprintf(
		"（这条规则将被套用到扫描出的 %d 个不同标的上，不需要你决定具体是哪个标的，"+
			"标的字段填一个交易所格式的占位符即可（实际会被替换成每个标的各自的代码）——"+
			"只需要正常理解其它信息：周期、模块、参数、止损止盈、风控）%s",
		symbolCount, utterance)
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
	// 扫描出的标的列表在整个多轮会话里保持不变，不重新扫描一次——跟 ForceSymbol
	// 跨澄清轮次不失效是同一个道理（见 wizardState.BatchSymbols 的注释）。
	s.renderBatchProposal(w, r, p, newHistory, ws.BatchSymbols)
}

// renderBatchProposal 是 renderProposal 的批量版本：NeedsClarification 时渲染批量专属的
// 澄清片段（posts 到 /wizard/batch/clarify），否则渲染带完整标的列表的确认片段，不展示
// 单个 Config.Symbol——那只是翻译阶段的占位符，在批量场景里没有意义。
func (s *Server) renderBatchProposal(w http.ResponseWriter, r *http.Request, p *agent.Proposal, history []agent.Turn, symbols []string) {
	state, err := encodeState(wizardState{History: history, Proposal: p, BatchSymbols: symbols})
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p.NeedsClarification() {
		s.renderFragment(w, r, "batch_clarify_fragment", wizardClarifyData{Questions: p.Questions, State: state})
		return
	}
	s.renderFragment(w, r, "batch_confirm_fragment",
		batchConfirmData{Restatement: p.Restatement, Config: *p.Config, Symbols: symbols, State: state})
}

// handleBatchScanConfirm 把确认过的配置模板分别套用到每个扫描出的标的上，各自生成一份
// 独立的 DRAFT StrategyConfig——一个标的校验失败不能拖累其它标的（仿
// handleBulkDeleteStrategies 的隔离+tally 写法），落库之后的路径（回测/模拟盘/推进/
// 手动解锁实盘）跟手动建的策略完全一样，不做任何特殊处理。
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
		// 浅拷贝：Modules 里的 Params 是 map，跟其它 N-1 份克隆共享底层数据，但循环里
		// 全程只读不写，不会互相污染——不要在这个循环里改 cfg.Modules[i].Params。
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
			// 审计记录写入失败不撤销已经保存的策略——理由跟 handleWizardConfirm 一致。
			s.logger.Error("写入审计记录失败", "strategy_id", prepared.ID, "symbol", symbol, "err", err)
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

// parseBatchCount 解析并夹逼用户填的扫描数量——非法输入直接报错，超出上限静默夹逼到
// 上限（仿 okx 包 FetchCandles 对 limit 的处理方式），不是报错拒绝整个请求。
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

// scanTopSymbols 拉取全市场现货行情快照，按 24 小时成交额降序取前 n 个标的。
func (s *Server) scanTopSymbols(ctx context.Context, n int) ([]string, error) {
	tickers, err := s.okxClient.ListTickers(ctx)
	if err != nil {
		return nil, err
	}
	return topSymbolsByVolume(tickers, n), nil
}

// topSymbolsByVolume 是纯函数，方便单独测试排序/截断逻辑，不依赖网络。
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
