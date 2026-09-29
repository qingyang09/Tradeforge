package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"tradeforge/internal/agent"
	"tradeforge/internal/i18n"
	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// maxClarificationRounds 镜像 cmd/agent-service 的 CLI 向导：澄清轮次太多说明
// 描述本身不够完整，与其无限循环不如提示用户重新描述。
const maxClarificationRounds = 5

type wizardStartData struct {
	AgentReady bool
}

func (s *Server) handleWizardStart(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	ag, _ := s.userAgent(r.Context(), userID)
	s.renderPage(w, r, "新建策略", "wizard_start_content", wizardStartData{AgentReady: ag != nil})
}

type wizardClarifyData struct {
	Questions []string
	State     string
}

type wizardConfirmData struct {
	Restatement string
	Config      types.StrategyConfig
	// ConfigJSON 是 Config 的线格式 JSON（跟 DecodeStrategyConfigJSON 认得的形状一样），
	// 嵌进确认片段供画板页的 JS 读取（见 wizard_confirm.html），把这份还没入库的配置
	// 实际画到图上——模块参数变成可拖拽的线、止损止盈变成价位，而不是停留在这张表格里
	// 的抽象文字。文字向导页面不读这个字段，多出来的内容对它没有影响。
	ConfigJSON string
	State      string
}

type wizardErrorData struct {
	Message string
}

func (s *Server) handleWizardTranslate(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	ag, _ := s.userAgent(r.Context(), userID)
	if ag == nil {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "Agent 翻译层未就绪，请先在设置页面配置模型。"})
		return
	}
	utterance := strings.TrimSpace(r.FormValue("utterance"))
	if utterance == "" {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "交易规则不能为空。"})
		return
	}

	// i18n.DefaultLang (Chinese) for now, matching this app's current
	// Chinese-only behavior exactly -- see render.go's msg template func doc
	// comment for why, and the plan's Phase 4 for the per-request fix.
	p, err := ag.Translate(r.Context(), utterance, nil, i18n.DefaultLang)
	if err != nil {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "翻译失败：" + err.Error()})
		return
	}
	s.renderProposal(w, p, nil, "")
}

func (s *Server) handleWizardClarify(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	ag, _ := s.userAgent(r.Context(), userID)
	if ag == nil {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "Agent 翻译层未就绪，请先在设置页面配置模型。"})
		return
	}
	ws, err := decodeState(r.FormValue("state"))
	if err != nil || ws.Proposal == nil {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "会话已失效，请重新开始。"})
		return
	}
	answer := strings.TrimSpace(r.FormValue("answer"))
	if answer == "" {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "回答不能为空。"})
		return
	}

	// 把上一轮的问题当成"assistant 说的话"接回对话历史，跟 CLI 的 runClarificationLoop
	// 拼历史的方式完全一致。
	newHistory := append(append([]agent.Turn{}, ws.History...),
		agent.Turn{Role: "user", Text: ws.Proposal.SourceUtterance},
		agent.Turn{Role: "assistant", Text: strings.Join(ws.Proposal.Questions, "\n")})

	if len(newHistory)/2 >= maxClarificationRounds {
		s.renderFragment(w, "wizard_error_fragment",
			wizardErrorData{Message: "澄清轮次过多，请把规则描述得更完整一些后重试。"})
		return
	}

	// i18n.DefaultLang (Chinese) for now -- see the comment on the Translate
	// call above.
	p, err := ag.Translate(r.Context(), answer, newHistory, i18n.DefaultLang)
	if err != nil {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "翻译失败：" + err.Error()})
		return
	}
	s.renderProposal(w, p, newHistory, ws.ForceSymbol)
}

// renderProposal 按 Outcome 分支渲染澄清问题或复述确认，translate/clarify 两个 handler 共用。
//
// forceSymbol 非空时会覆盖提案里的标的（见 wizardState.ForceSymbol 的注释），
// 文字向导传空字符串，行为不变；可视化建策传当前画板的标的。
func (s *Server) renderProposal(w http.ResponseWriter, p *agent.Proposal, history []agent.Turn, forceSymbol string) {
	if forceSymbol != "" && p.Outcome == agent.OutcomeConfig && p.Config != nil {
		p.Config.Symbol = forceSymbol
	}
	state, err := encodeState(wizardState{History: history, Proposal: p, ForceSymbol: forceSymbol})
	if err != nil {
		s.serverError(w, err)
		return
	}
	if p.NeedsClarification() {
		s.renderFragment(w, "wizard_clarify_fragment", wizardClarifyData{Questions: p.Questions, State: state})
		return
	}
	configJSON, err := json.Marshal(p.Config)
	if err != nil {
		s.serverError(w, err)
		return
	}
	s.renderFragment(w, "wizard_confirm_fragment",
		wizardConfirmData{Restatement: p.Restatement, Config: *p.Config, ConfigJSON: string(configJSON), State: state})
}

type wizardResultData struct {
	Success    bool
	Message    string
	StrategyID string
}

// handleWizardConfirm 是两条建策路径（文字向导 + 可视化建策，见 handlers_builder.go）
// 共用的保存入口：只要拿到一个 Outcome 为 config 的 Proposal，就能走完"重新校验一遍 →
// 落库 → 记审计"这一步，不需要 Agent/LLM 参与。之前这里是通过 ag.Confirm 间接拿到
// 模块注册表来校验的，导致这个本不需要 LLM 的保存步骤，被"Agent 是否就绪"卡住——
// 可视化建策要求不依赖 LLM key 也能用，所以把这里改成直接用 s.registry 校验。
func (s *Server) handleWizardConfirm(w http.ResponseWriter, r *http.Request) {
	ws, err := decodeState(r.FormValue("state"))
	if err != nil || ws.Proposal == nil {
		s.renderFragment(w, "wizard_error_fragment", wizardErrorData{Message: "会话已失效，请重新开始。"})
		return
	}

	if r.FormValue("decision") != "confirm" {
		s.renderFragment(w, "wizard_result_fragment",
			wizardResultData{Success: false, Message: "已取消，配置未写入系统。"})
		return
	}

	cfg, err := s.confirmProposal(ws.Proposal)
	if err != nil {
		s.renderFragment(w, "wizard_result_fragment",
			wizardResultData{Success: false, Message: "确认失败：" + err.Error()})
		return
	}
	cfg.ID = idgen.NewUUID()
	cfg.UserID, _ = currentUserID(r)

	ctx := r.Context()
	if err := s.store.SaveStrategy(ctx, cfg); err != nil {
		s.renderFragment(w, "wizard_result_fragment",
			wizardResultData{Success: false, Message: "写入数据库失败：" + err.Error()})
		return
	}
	if err := s.store.RecordTransition(ctx, storage.Transition{
		StrategyID: cfg.ID, From: "", To: types.StateDraft,
		Actor: "user:web", Reason: "用户确认了策略配置",
		Evidence: map[string]any{"source_utterance": cfg.SourceUtterance, "attempts": ws.Proposal.Attempts},
	}); err != nil {
		// 审计记录写入失败不撤销已经保存的策略——策略本身是权威数据，
		// 缺一条审计记录好过丢一个用户已确认的策略。
		s.logger.Error("写入审计记录失败", "strategy_id", cfg.ID, "err", err)
	}

	s.renderFragment(w, "wizard_result_fragment", wizardResultData{Success: true, StrategyID: cfg.ID})
}

// confirmProposal 重新校验一份提案并把它标记为可入库的状态。逻辑照抄
// agent.Confirm（internal/agent/agent.go），唯一区别是用 s.registry 而不是通过
// *agent.Agent 拿校验用的模块注册表——这样这一步就不需要 LLM 参与。
func (s *Server) confirmProposal(p *agent.Proposal) (types.StrategyConfig, error) {
	if p == nil {
		return types.StrategyConfig{}, errors.New("提案为空")
	}
	if p.NeedsClarification() {
		return types.StrategyConfig{}, errors.New("该提案仍在等待用户澄清，不能直接确认")
	}
	if p.Config == nil {
		return types.StrategyConfig{}, errors.New("提案中没有配置")
	}
	return s.prepareDraftConfig(*p.Config, p.SourceUtterance)
}

// prepareDraftConfig 把一份配置整理成可以直接落库的 DRAFT 状态：重新校验、盖时间戳。
// 单个策略确认（confirmProposal）和批量扫描确认（handlers_batch.go 的
// handleBatchScanConfirm，一次循环里对每个标的的副本分别调用）共用这段逻辑，不维护
// 两份"校验+状态+时间戳"的代码。
//
// 隐藏字段哪怕被篡改，重新校验最多导致这里被拒绝，不会绕过 schema 或合规检查。
func (s *Server) prepareDraftConfig(cfg types.StrategyConfig, sourceUtterance string) (types.StrategyConfig, error) {
	if _, err := strategy.Validate(cfg, s.registry); err != nil {
		return types.StrategyConfig{}, fmt.Errorf("确认时校验未通过：%w", err)
	}
	cfg.SourceUtterance = sourceUtterance
	cfg.State = types.StateDraft
	now := time.Now().UTC()
	cfg.CreatedAt, cfg.UpdatedAt = now, now
	return cfg, nil
}
