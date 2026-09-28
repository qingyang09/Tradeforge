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

// EmailSender is the minimal interface for the email channel, letting
// cmd/notifier's dispatch logic swap in a fake implementation in tests —
// the same "minimal interface for test injection" pattern as
// brokerCredentialStore/promotionStore.
type EmailSender interface {
	SendEmail(ctx context.Context, to, subject, body string) error
}

// EmailConfig is the plaintext shape packed into the
// notification_channels.encrypted_config ciphertext when kind="email".
type EmailConfig struct {
	Address string `json:"address"`
}

const defaultSMTPDialTimeout = 10 * time.Second

// SMTPEmailSender sends mail with the standard library's net/smtp — the
// project has no SMTP library dependency, and there's no precedent to
// deviate from the "use the stdlib unless there's a reason not to" default;
// sending email is simple enough not to need one.
//
// net/smtp doesn't support context cancellation — that's a stdlib
// limitation, not laziness here: okx.Client uses net/http, which supports
// context natively, but this falls back to net.DialTimeout to put a hard
// timeout on connection establishment. Once ctx is cancelled, a call that's
// already mid-send can still run out that timeout before returning — not
// fully cancellable, but the best a stdlib-only approach can do.
type SMTPEmailSender struct {
	host, port, username, password, from string
	dialTimeout                          time.Duration
}

// NewSMTPEmailSender builds a sender from deployment-level SMTP config. An
// empty host makes SendEmail fail outright rather than blocking process
// startup — a deployment can perfectly well skip the email channel and use
// only the other three.
func NewSMTPEmailSender(cfg config.SMTPConfig) *SMTPEmailSender {
	return &SMTPEmailSender{
		host: cfg.Host, port: cfg.Port, username: cfg.Username, password: cfg.Password,
		from: cfg.From, dialTimeout: defaultSMTPDialTimeout,
	}
}

func (s *SMTPEmailSender) SendEmail(_ context.Context, to, subject, body string) error {
	if s.host == "" {
		return fmt.Errorf("SMTP not configured (TF_SMTP_HOST), email channel unavailable")
	}

	addr := net.JoinHostPort(s.host, s.port)
	conn, err := net.DialTimeout("tcp", addr, s.dialTimeout)
	if err != nil {
		return fmt.Errorf("failed to connect to SMTP server: %w", err)
	}
	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		return fmt.Errorf("failed to construct SMTP client: %w", err)
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.host}); err != nil {
			return fmt.Errorf("STARTTLS failed: %w", err)
		}
	}
	if s.username != "" {
		auth := smtp.PlainAuth("", s.username, s.password, s.host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP auth failed: %w", err)
		}
	}

	if err := client.Mail(s.from); err != nil {
		return fmt.Errorf("MAIL FROM failed: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("RCPT TO failed: %w", err)
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA failed: %w", err)
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		s.from, to, subject, body)
	if _, err := wc.Write([]byte(msg)); err != nil {
		return fmt.Errorf("failed to write email content: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("failed to finish writing email: %w", err)
	}
	return client.Quit()
}
