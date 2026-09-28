package webui

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"tradeforge/internal/agent"
)

// wizardState 是向导多轮对话在两次 HTTP 请求之间的全部状态。
//
// 服务端不维护会话：agent.Proposal 与 agent.Turn 都已经是 JSON 可序列化的普通结构，
// 把它们编码进隐藏表单字段、下一次 POST 再解码回来，比引入 session 存储简单得多，
// 也让服务重启不会丢失正在进行的向导会话。
//
// 隐藏字段不签名/不加密是刻意的：这是本地单操作者工具，能 POST 到这个 server 的人
// 本来就对同一台机器上的数据库有直接访问权限；而且每次写入前 agent.Confirm 都会
// 用真实模块注册表重跑 strategy.Validate，篡改这个字段最多导致校验被拒绝，
// 不会绕过 schema 或合规检查。
type wizardState struct {
	History  []agent.Turn    `json:"history,omitempty"`
	Proposal *agent.Proposal `json:"proposal,omitempty"`
	// ForceSymbol 非空时，表示这轮对话锁死在某个标的上（可视化建策发起的翻译请求，
	// 见 handleBuilderTranslate）——用户在这个标的的画板上说话，规则就该落在这个
	// 标的上，不依赖 LLM 每一轮都恰好猜对。renderProposal 在编码新状态前会用它覆盖
	// 掉提案里的 Symbol，且这个值会跟着 History 一起在澄清轮次之间传递下去，
	// 不会因为多问一轮就失效。文字向导（handleWizardTranslate）不设置它，
	// 行为跟以前完全一样。
	ForceSymbol string `json:"force_symbol,omitempty"`
	// BatchSymbols 非空时表示这是一次批量扫描确认（见 handlers_batch.go）：翻译出的
	// 配置模板要分别套用到这些标的上，各自生成一份独立的 DRAFT——跟 ForceSymbol（覆盖成
	// 单个标的）是同一个"状态里带着标的约束跨请求传递"的机制，只是这里是一组标的、且
	// 套用发生在确认落库那一步，不是翻译阶段（翻译阶段这些标的还不存在于 Config 里，
	// Config.Symbol 只是一个会被丢弃的占位符）。扫描结果在整个多轮澄清会话里保持不变，
	// 不会因为多问一轮就重新扫描一次。
	BatchSymbols []string `json:"batch_symbols,omitempty"`
}

// encodeState 把状态编码成一段可以放进 <input type=hidden> 的文本。
func encodeState(ws wizardState) (string, error) {
	blob, err := json.Marshal(ws)
	if err != nil {
		return "", fmt.Errorf("序列化向导状态失败：%w", err)
	}
	return base64.URLEncoding.EncodeToString(blob), nil
}

// decodeState 是 encodeState 的逆过程。失败时返回的错误应当被渲染成
// "会话已失效，请重新开始"这类事实性提示，而不是 500。
func decodeState(s string) (wizardState, error) {
	blob, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return wizardState{}, fmt.Errorf("向导状态已损坏：%w", err)
	}
	var ws wizardState
	if err := json.Unmarshal(blob, &ws); err != nil {
		return wizardState{}, fmt.Errorf("向导状态已损坏：%w", err)
	}
	return ws, nil
}
