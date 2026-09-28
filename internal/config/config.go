// Package config centralizes configuration loading for all services.
//
// Every setting can be overridden by an environment variable; the local
// development defaults point at the services started by docker-compose.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the platform's complete configuration.
type Config struct {
	Postgres       PostgresConfig
	Redis          RedisConfig
	Kafka          KafkaConfig
	Agent          AgentConfig
	Engine         EngineConfig
	HTTP           HTTPConfig
	Security       SecurityConfig
	BacktestRunner BacktestRunnerConfig
	Notification   NotificationConfig
}

// PostgresConfig is the Postgres connection configuration.
type PostgresConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
	SSLMode  string
}

// DSN returns a connection string usable by pgx.
func (p PostgresConfig) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		p.User, p.Password, p.Host, p.Port, p.Database, p.SSLMode)
}

// RedisConfig is the Redis connection configuration.
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

// KafkaConfig is the Kafka connection configuration.
type KafkaConfig struct {
	Brokers []string
	// DecisionTopic carries the final decisions produced by the combination engine.
	DecisionTopic string
	// SignalTopic carries each module's raw signals (for debugging and replay).
	SignalTopic string
}

// AgentConfig is the AI Agent translation layer configuration.
type AgentConfig struct {
	// APIKey is the LLM API key. When empty, the Agent can only run in
	// offline/stub mode.
	APIKey string
	// Model is the model ID to use.
	Model string
	// BaseURL allows pointing at a self-hosted gateway.
	BaseURL string
	// Timeout is the timeout for a single translation request.
	Timeout time.Duration
	// MaxRetries is how many retries are allowed after a schema validation
	// failure. A retry means "have the model regenerate it," never
	// "patch up the model's output for it."
	MaxRetries int
}

// EngineConfig is the combination engine configuration.
type EngineConfig struct {
	// ModuleTimeout is the timeout for a single module's Evaluate; on
	// timeout, that module degrades to a neutral signal.
	ModuleTimeout time.Duration
}

// BacktestRunnerConfig configures how cmd/webui's "run backtest" button
// shells out to the Python matching-engine subprocess.
type BacktestRunnerConfig struct {
	// PythonExe is the python interpreter's name/path; some systems need
	// this overridden to "python3".
	PythonExe string
	// PythonDir is the path to python/backtest, used as the subprocess's working directory.
	PythonDir string
	// DefaultLookback is how many candles to pull by default when the UI
	// doesn't specify a lookback count.
	DefaultLookback int
	// Timeout is the subprocess's maximum run time.
	Timeout time.Duration
}

// HTTPConfig is the web interface's (cmd/webui) configuration.
type HTTPConfig struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
}

// SecurityConfig is the server-side master key configuration used to
// encrypt/decrypt every user's credentials. After the multi-user SaaS
// rework, login auth is each user's own email+password (bcrypt hash, see
// internal/webui/handlers_auth.go) rather than the single shared password
// configured here — MasterKey is purely keying material for
// internal/secretcrypto and has nothing to do with any user's login
// password; see the Server.masterKey comment in internal/webui/server.go.
type SecurityConfig struct {
	// MasterKey has no default: losing it permanently locks every user out
	// of their stored exchange/LLM credentials, a consequence far more
	// severe than "forgot a one-time login password," so unlike the old
	// AdminPassword it can't just "generate a random one if unset." The
	// startup paths in cmd/webui/main.go and cmd/executor/main.go all
	// require it to be set explicitly and be long enough, or they refuse to start.
	MasterKey string
}

// NotificationConfig is the "deployment-level" shared portion of the alert
// channel configuration — the SMTP server, Telegram bot token, and VAPID
// key pair are each shared by the whole deployment (not a per-user key),
// the same category of thing as TF_MASTER_KEY: each user's own part
// (recipient address/chat_id/webhook URL/push subscription) is stored in
// the notification_channels table, see internal/storage/notification_channels.go.
type NotificationConfig struct {
	SMTP     SMTPConfig
	Telegram TelegramConfig
	WebPush  WebPushConfig
}

// SMTPConfig is the server-side SMTP configuration used to send alert emails.
type SMTPConfig struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
}

// TelegramConfig is the deployment-level configuration for the Telegram
// channel — BotToken is this deployment's single Telegram bot, shared by
// all users, whose identities are distinguished by their own chat_id
// stored in the notification_channels table.
type TelegramConfig struct {
	BotToken string
}

// WebPushConfig is the web push channel's VAPID (RFC 8292) key pair,
// generated once per deployment and kept stable long-term — browsers trust
// pushes from this deployment based on this key pair. VAPIDSubject is the
// contact info RFC 8292 requires, of the form "mailto:ops@example.com",
// used by push services to reach the deployment operator in case of abuse.
type WebPushConfig struct {
	VAPIDPublicKey  string
	VAPIDPrivateKey string
	VAPIDSubject    string
}

// Load reads configuration from environment variables, falling back to
// local development defaults for anything missing.
func Load() Config {
	return Config{
		Postgres: PostgresConfig{
			Host:     env("TF_PG_HOST", "localhost"),
			Port:     envInt("TF_PG_PORT", 55432),
			User:     env("TF_PG_USER", "tradeforge"),
			Password: env("TF_PG_PASSWORD", "tradeforge"),
			Database: env("TF_PG_DATABASE", "tradeforge"),
			SSLMode:  env("TF_PG_SSLMODE", "disable"),
		},
		Redis: RedisConfig{
			Addr:     env("TF_REDIS_ADDR", "localhost:56379"),
			Password: env("TF_REDIS_PASSWORD", ""),
			DB:       envInt("TF_REDIS_DB", 0),
		},
		Kafka: KafkaConfig{
			Brokers:       strings.Split(env("TF_KAFKA_BROKERS", "localhost:59200"), ","),
			DecisionTopic: env("TF_KAFKA_DECISION_TOPIC", "tradeforge.decisions"),
			SignalTopic:   env("TF_KAFKA_SIGNAL_TOPIC", "tradeforge.signals"),
		},
		Agent: AgentConfig{
			APIKey:     env("ANTHROPIC_API_KEY", ""),
			Model:      env("TF_AGENT_MODEL", "claude-opus-5"),
			BaseURL:    env("TF_AGENT_BASE_URL", "https://api.anthropic.com"),
			Timeout:    envDuration("TF_AGENT_TIMEOUT", 60*time.Second),
			MaxRetries: envInt("TF_AGENT_MAX_RETRIES", 2),
		},
		Engine: EngineConfig{
			ModuleTimeout: envDuration("TF_MODULE_TIMEOUT", 3*time.Second),
		},
		HTTP: HTTPConfig{
			Addr: env("TF_HTTP_ADDR", ":8080"),
		},
		Security: SecurityConfig{
			MasterKey: env("TF_MASTER_KEY", ""),
		},
		BacktestRunner: BacktestRunnerConfig{
			PythonExe:       env("TF_BACKTEST_PYTHON", "python"),
			PythonDir:       env("TF_BACKTEST_PYTHON_DIR", "python/backtest"),
			DefaultLookback: envInt("TF_BACKTEST_LOOKBACK", 1000),
			Timeout:         envDuration("TF_BACKTEST_TIMEOUT", 2*time.Minute),
		},
		Notification: NotificationConfig{
			SMTP: SMTPConfig{
				Host:     env("TF_SMTP_HOST", ""),
				Port:     env("TF_SMTP_PORT", "587"),
				Username: env("TF_SMTP_USERNAME", ""),
				Password: env("TF_SMTP_PASSWORD", ""),
				From:     env("TF_SMTP_FROM", ""),
			},
			Telegram: TelegramConfig{
				BotToken: env("TF_TELEGRAM_BOT_TOKEN", ""),
			},
			WebPush: WebPushConfig{
				VAPIDPublicKey:  env("TF_VAPID_PUBLIC_KEY", ""),
				VAPIDPrivateKey: env("TF_VAPID_PRIVATE_KEY", ""),
				VAPIDSubject:    env("TF_VAPID_SUBJECT", ""),
			},
		},
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envDuration(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}
