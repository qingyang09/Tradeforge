package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// symbolPattern 粗略校验路径参数是不是标的写法（如 "BTCUSDT"），拒绝明显不合理的
// 输入（空、太长、带特殊字符）。
var symbolPattern = regexp.MustCompile(`^[A-Za-z0-9]{2,20}$`)

func looksLikeSymbol(s string) bool { return symbolPattern.MatchString(s) }

type builderListData struct {
	Symbols []string
}

// handleBuilderList 跟 handleChartList 是同一个模式：挑一个已有策略用过的标的，
// 或者直接手输标的跳到画板页。
func (s *Server) handleBuilderList(w http.ResponseWriter, r *http.Request) {
	if symbol := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol"))); symbol != "" {
		http.Redirect(w, r, "/builder/"+symbol, http.StatusFound)
		return
	}

	userID, _ := currentUserID(r)
	all, err := s.store.ListStrategies(r.Context(), userID)
	if err != nil {
		s.serverError(w, err)
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

	s.renderPage(w, r, "可视化建策", "builder_list_content", builderListData{Symbols: symbols})
}

type builderViewData struct {
	Symbol string
	// Timeframe 是画板初始加载时选中的主周期。支持 ?timeframe= 深链（策略详情页
	// "去画板查看"带着该策略的周期跳过来时，画板要一打开就显示同一个周期的图，
	// 而不是永远落回写死的默认值）。
	Timeframe string
	// AgentReady 控制"用自己的话描述交易计划"这个自然语言入口是否显示——它是模块/
	// 拖拽这条不依赖 LLM 的主路径之外的增强，未配置模型时隐藏，不影响画板本身可用。
	AgentReady bool
	// InitialConfigJSON 支持 ?strategy=<id> 深链（策略详情页"去画板查看"带着策略 ID
	// 跳过来）：非空时是那条策略当前配置的线格式 JSON，形状跟 wizardConfirmData.
	// ConfigJSON 完全一样，画板页的 JS 用同一个 applyStrategyConfig 把它画到图上——
	// 模块变成卡片、止损止盈变成可拖拽的线、support_resistance 这类没有固定点位的
	// 模块会用它保存的参数重新触发一次实时预览。查不到（策略不存在/不属于当前用户/
	// 没带这个参数）时留空，画板照旧退回空白画布，不阻塞"单纯看图"这个用途。
	InitialConfigJSON string
}

// handleBuilderView 渲染画板页：独立深色沉浸式文档。画板同时承担"单纯看图"和
// "建策"两个用途——不加任何模块、不描述任何规则，落地页就是一张能拖拽缩放的
// 真实 K 线图（历史上这曾经是 /chart 页单独用 TradingView 组件做的事，两个页面
// 各自维护一份"输入标的看图"的入口没有意义，已合并成画板一个入口）。
//
// ?strategy=<id> 打破"不加任何模块"这条默认规则，但只在明确带着这个参数、且这个
// 策略确实属于当前用户时才生效——策略详情页"去画板查看"从阶段 7 起一直只带
// timeframe，用户点进去看不到当初画出来的线，是个体验缺口而不是有意为之。
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
				s.logger.Warn("序列化策略配置失败，画板退回空白画布", "strategy_id", id, "err", err)
			}
		case errors.Is(err, storage.ErrNotFound):
			// 策略不存在，或者不属于当前用户（两者返回同一个 ErrNotFound）：
			// 链接可能是别人分享的、也可能策略已被删除，静默退回空白画布，
			// 不是真正的错误，不值得报错打断"至少还能看图"这个体验。
		default:
			s.logger.Warn("查询画板初始配置失败，退回空白画布", "strategy_id", id, "err", err)
		}
	}

	s.renderStandalone(w, "builder_view_page", builderViewData{
		Symbol:            symbol,
		Timeframe:         string(tf),
		AgentReady:        ag != nil,
		InitialConfigJSON: initialConfigJSON,
	})
}

const maxBuilderConfigBytes = 1 << 20 // 1MiB，画板配置不可能真的这么大，纯粹是防呆

// handleBuilderDescribe 把画板拖拽/调参后组装出的 JSON 转成 StrategyConfig，校验，
// 生成复述文字，然后渲染跟文字向导完全相同的确认片段——两条建策路径殊途同归，
// 从这一步开始复用同一套保存/审计逻辑（见 handlers_wizard.go 的 confirmProposal
// 与 handleWizardConfirm）。
func (s *Server) handleBuilderDescribe(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBuilderConfigBytes+1))
	if err != nil {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "读取请求体失败：" + err.Error()})
		return
	}
	if len(body) > maxBuilderConfigBytes {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "配置体积超出限制。"})
		return
	}

	cfg, err := agent.DecodeStrategyConfigJSON(body)
	if err != nil {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "配置格式有误：" + err.Error()})
		return
	}

	// 跟 confirmProposal 用的是同一份注册表——用户在这一步能看到的错误，
	// 跟真正保存时会跑的校验完全一致，不会出现"这里过了、保存时又被拒"的落差。
	if _, err := strategy.Validate(cfg, s.registry); err != nil {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "配置未通过校验：" + err.Error()})
		return
	}

	userID, _ := currentUserID(r)
	restatement := s.describeConfig(r.Context(), userID, cfg)

	p := &agent.Proposal{
		Outcome:         agent.OutcomeConfig,
		Restatement:     restatement,
		Config:          &cfg,
		SourceUtterance: "可视化建策",
	}
	s.renderProposal(w, p, nil, "")
}

// handleBuilderTranslate 是可视化建策接入自然语言翻译层的入口：用户不需要理解
// "volume_breakout"、"WEIGHTED" 这些术语，直接用大白话描述交易计划。Agent 翻译出配置后，
// 走的是跟文字向导完全相同的确认链路（renderProposal 复用 wizard_confirm_fragment /
// wizard_clarify_fragment，歧义时一样会追问而不是替用户瞎猜），唯一区别是确认片段里嵌了
// 一份配置的 JSON（wizardConfirmData.ConfigJSON），画板页的 JS 读到它之后会把模块、参数、
// 止损止盈实际画到图上——用户要的是"不要停留在抽象文字，尽量体现在图表上"，靠的就是这份
// JSON，不是新写一套翻译逻辑。
//
// 标的强制锁定成画板当前的 SYMBOL（ForceSymbol，见 wizardState），不依赖 LLM 从用户描述里
// 猜对：用户在这个标的的画板上说话，规则就该落在这个标的上，这个约束会跟着可能出现的
// 澄清轮次一起传递下去，不会因为多问一轮就失效。
func (s *Server) handleBuilderTranslate(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	ag, _ := s.userAgent(r.Context(), userID)
	if ag == nil {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "Agent 翻译层未就绪，请先在设置页面配置模型。"})
		return
	}
	symbol := strings.ToUpper(r.PathValue("symbol"))
	if !looksLikeSymbol(symbol) {
		http.NotFound(w, r)
		return
	}
	utterance := strings.TrimSpace(r.FormValue("utterance"))
	if utterance == "" {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "交易计划描述不能为空。"})
		return
	}

	// 把标的context 交代给模型，不是为了约束它（Config.Symbol 会在 renderProposal 里
	// 被强制覆盖），而是让复述文字读起来跟画板实际生效的标的一致——不这样做的话，
	// 用户完全没提标的时模型可能编一个不相关的标的写进复述里，跟画板强制生效的标的对不上。
	contextualUtterance := fmt.Sprintf(
		"（当前正在 %s 的可视化建策画板上操作，除非用户明确说了别的标的，否则这条规则默认就是针对 %s 的）%s",
		symbol, symbol, utterance)

	// i18n.DefaultLang (Chinese) for now, matching this app's current
	// Chinese-only behavior exactly -- see render.go's msg template func doc
	// comment for why, and the plan's Phase 4 for the per-request fix.
	p, err := ag.Translate(r.Context(), contextualUtterance, nil, i18n.DefaultLang)
	if err != nil {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "翻译失败：" + err.Error()})
		return
	}
	// 这里刻意不去掉注入的标的上下文前缀（跟早期版本不一样）：p.SourceUtterance 不只是
	// 拿来显示的，handleWizardClarify 重建对话历史时会把它当成"用户说的第一轮话"重新
	// 回灌给模型（见那边的 newHistory 拼接）。之前在这里把前缀去掉、只留用户原话，
	// 结果是澄清问答一旦进入第二轮，模型就不再知道"标的已经定死是这个"，
	// 于是每一轮都重新追问一遍标的——真实用 DeepSeek 复现过这个循环。
	// 代价是最终存库的 source_utterance 会带上这段注入文字，但这如实反映了送给模型的
	// 完整输入，审计意义上不算坏事。
	s.renderProposal(w, p, nil, symbol)
}

// describeConfig 优先用 Agent 生成复述；Agent 未就绪（没配 LLM key）时退回确定性
// 兜底复述——可视化建策的配置本来就没有需要模型理解的歧义，兜底版本一样能用。
func (s *Server) describeConfig(ctx context.Context, userID string, cfg types.StrategyConfig) string {
	// i18n.DefaultLang (Chinese) for now, matching this app's current
	// Chinese-only behavior exactly -- see render.go's msg template func doc
	// comment for why, and the plan's Phase 4 for the per-request fix.
	lang := i18n.DefaultLang
	ag, _ := s.userAgent(ctx, userID)
	if ag == nil {
		return describePlain(lang, cfg)
	}
	text, err := ag.Describe(ctx, cfg, lang)
	if err != nil {
		s.logger.Warn("Agent 复述失败，改用确定性兜底复述", "err", err)
		return describePlain(lang, cfg)
	}
	return text
}
