// Package notify 实现信号提醒的四个投递渠道（邮件/Telegram/Webhook/浏览器推送）
// 和统一的消息格式化。cmd/notifier 是这个包唯一的调用方。
package notify

import (
	"fmt"
	"time"

	"tradeforge/pkg/types"
)

// Mode 标注一条提醒对应的策略是模拟盘预览还是真实实盘，所有渠道的文案都要
// 清楚标注这一点——不能让用户把模拟盘的提醒误当成真实成交的提醒，反之亦然。
type Mode string

const (
	ModePreview Mode = "PAPER_TRADING" // 模拟盘：预览/验证体验
	ModeLive    Mode = "LIVE"          // 实盘：真实提醒
)

// Message 是格式化后、可以直接喂给任意渠道的提醒内容。
//
// 合规红线（见项目 claude.md）：Title/Body 的每一处措辞只能事实性描述"信号计算出了
// 什么"（方向、强度、价格、触发原因、哪些模块参与），绝不能出现"建议""推荐""这样
// 更好"之类暗示投资建议的措辞——这条红线在 Agent 系统提示词里已经是强制要求，这里
// 是它在通知文案上的延伸，不是重新发明。message_test.go 用一条 grep 式测试把这条
// 约束变成机械检查，不只靠人工审查。
type Message struct {
	Title   string
	Subject string // 邮件用，通常等于 Title
	Body    string // 多行正文，邮件/Telegram/webhook 都用得到
}

// PlainText 是 Telegram/web push 用的单块文本。
func (m Message) PlainText() string { return m.Title + "\n\n" + m.Body }

// WebhookPayload 是 webhook 渠道的结构化 JSON 载荷——除了给人看的 Title/Body，
// 还原样带上决策的全部关键字段，供接收方自己的系统消费，不用反过来解析 Body 里的
// 自然语言文本。
type WebhookPayload struct {
	Title      string    `json:"title"`
	Body       string    `json:"body"`
	Mode       Mode      `json:"mode"`
	StrategyID string    `json:"strategy_id"`
	Symbol     string    `json:"symbol"`
	Direction  string    `json:"direction"`
	Score      float64   `json:"score"`
	Price      string    `json:"price"`
	Reason     string    `json:"reason"`
	Timestamp  time.Time `json:"timestamp"`
}

// ToWebhookPayload 把 Message 和产生它的 Decision、Mode 一起打包成 webhook 载荷。
func (m Message) ToWebhookPayload(d types.Decision, mode Mode) WebhookPayload {
	return WebhookPayload{
		Title: m.Title, Body: m.Body, Mode: mode, StrategyID: d.StrategyID, Symbol: d.Symbol,
		Direction: string(d.Direction), Score: d.Score, Price: d.Price.String(),
		Reason: d.Reason, Timestamp: d.Timestamp,
	}
}

// complianceDisclaimer 是固定追加在每条提醒正文末尾的合规声明——这句话本身必然
// 包含"建议"二字（作为"不构成投资建议"这个否定短语的一部分），这是唯一允许出现
// 该词的地方。message_test.go 的机械检查会先把这句话从 Body 里去掉，再检查剩余
// 部分有没有出现"建议"/"推荐"，不要因为这句话本身触发检查就把它删掉或改写。
const complianceDisclaimer = "此提醒只描述系统按你设定的规则计算出的结果，不构成投资建议。"

// BuildMessage 把一条已触发的决策格式化成提醒文案，事实性描述，不做任何投资建议
// 措辞。sc 只用它的 Name/Symbol，不暴露完整策略配置（风控参数等不需要外泄给邮件/
// Telegram/webhook 接收方）。
func BuildMessage(sc types.StrategyConfig, d types.Decision, mode Mode) Message {
	modeLabel := "模拟盘预览"
	if mode == ModeLive {
		modeLabel = "实盘"
	}
	title := fmt.Sprintf("[%s] %s 触发：%s %s", modeLabel, sc.Name, d.Symbol, directionLabel(d.Direction))
	body := fmt.Sprintf(
		"策略：%s\n标的：%s\n方向：%s\n强度：%.2f\n价格：%s\n原因：%s\n触发时间：%s\n\n%s",
		sc.Name, d.Symbol, directionLabel(d.Direction), d.Score, d.Price.String(), d.Reason,
		d.Timestamp.Format(time.RFC3339), complianceDisclaimer)
	return Message{Title: title, Subject: title, Body: body}
}

func directionLabel(dir types.Direction) string {
	switch dir {
	case types.DirectionLong:
		return "做多"
	case types.DirectionShort:
		return "做空"
	default:
		return "中性"
	}
}
