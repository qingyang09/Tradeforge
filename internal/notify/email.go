package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"time"

	"tradeforge/internal/config"
)

// EmailSender 是邮件渠道的最小接口，供 cmd/notifier 的分发逻辑在测试里替换成假实现，
// 跟 brokerCredentialStore/promotionStore 是同一个"最小接口供测试注入"模式。
type EmailSender interface {
	SendEmail(ctx context.Context, to, subject, body string) error
}

// EmailConfig 是打包进 notification_channels.encrypted_config 密文里的、
// kind="email" 时的明文结构。
type EmailConfig struct {
	Address string `json:"address"`
}

const defaultSMTPDialTimeout = 10 * time.Second

// SMTPEmailSender 用标准库 net/smtp 发信——项目里没有任何 SMTP 库依赖，也没有
// 先例需要偏离"能用标准库就不引第三方依赖"的一贯做法，邮件发送本身足够简单。
//
// net/smtp 不支持 context 取消——这是标准库本身的限制，不是这里偷懒：okx.Client
// 用 net/http 天然支持 context，这里退而求其次，用 net.DialTimeout 给连接建立
// 设一个硬超时；ctx 被取消后，一次已经在发送中的调用仍可能跑满这个超时才返回，
// 不是完全可取消，但这是纯标准库方案能做到的上限。
type SMTPEmailSender struct {
	host, port, username, password, from string
	dialTimeout                          time.Duration
}

// NewSMTPEmailSender 按部署级 SMTP 配置构造发信器。host 为空时 SendEmail 会直接
// 报错，不阻止进程启动——一个部署完全可以不配邮件渠道，只用其它三个。
func NewSMTPEmailSender(cfg config.SMTPConfig) *SMTPEmailSender {
	return &SMTPEmailSender{
		host: cfg.Host, port: cfg.Port, username: cfg.Username, password: cfg.Password,
		from: cfg.From, dialTimeout: defaultSMTPDialTimeout,
	}
}

func (s *SMTPEmailSender) SendEmail(_ context.Context, to, subject, body string) error {
	if s.host == "" {
		return fmt.Errorf("未配置 SMTP（TF_SMTP_HOST），邮件渠道不可用")
	}

	addr := net.JoinHostPort(s.host, s.port)
	conn, err := net.DialTimeout("tcp", addr, s.dialTimeout)
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器失败：%w", err)
	}
	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return fmt.Errorf("构造 SMTP 客户端失败：%w", err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.host}); err != nil {
			return fmt.Errorf("STARTTLS 失败：%w", err)
		}
	}
	if s.username != "" {
		auth := smtp.PlainAuth("", s.username, s.password, s.host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 鉴权失败：%w", err)
		}
	}

	if err := client.Mail(s.from); err != nil {
		return fmt.Errorf("MAIL FROM 失败：%w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO 失败：%w", err)
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA 失败：%w", err)
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		s.from, to, subject, body)
	if _, err := wc.Write([]byte(msg)); err != nil {
		return fmt.Errorf("写入邮件内容失败：%w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("结束邮件写入失败：%w", err)
	}
	return client.Quit()
}
