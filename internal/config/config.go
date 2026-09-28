// Package config 集中管理各服务的配置读取。
//
// 所有配置都可以被环境变量覆盖，本地开发时的默认值指向 docker-compose 起的服务。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config 是平台的完整配置。
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

// PostgresConfig 是 Postgres 连接配置。
type PostgresConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
	SSLMode  string
}

// DSN 返回 pgx 可用的连接串。
func (p PostgresConfig) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		p.User, p.Password, p.Host, p.Port, p.Database, p.SSLMode)
}

// RedisConfig 是 Redis 连接配置。
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

// KafkaConfig 是 Kafka 连接配置。
type KafkaConfig struct {
	Brokers []string
	// DecisionTopic 承载组合引擎输出的最终决策。
	DecisionTopic string
	// SignalTopic 承载各模块的原始信号（用于调试与回放）。
	SignalTopic string
}

// AgentConfig 是 AI Agent 翻译层配置。
type AgentConfig struct {
	// APIKey 是 LLM API 密钥。为空时 Agent 只能以离线/桩模式运行。
	APIKey string
	// Model 是使用的模型 ID。
	Model string
	// BaseURL 允许指向自建网关。
	BaseURL string
	// Timeout 是单次翻译请求的超时。
	Timeout time.Duration
	// MaxRetries 是 schema 校验失败后允许重试的次数。
	// 重试是"让模型重新生成"，不是"帮模型修补输出"。
	MaxRetries int
}

// EngineConfig 是组合引擎配置。
type EngineConfig struct {
	// ModuleTimeout 是单个模块 Evaluate 的超时，超时后该模块降级为中性信号。
	ModuleTimeout time.Duration
}

// BacktestRunnerConfig 配置 cmd/webui"运行回测"按钮怎么拉起 Python 撮合引擎子进程。
type BacktestRunnerConfig struct {
	// PythonExe 是 python 解释器名字/路径，某些系统上需要覆盖成 "python3"。
	PythonExe string
	// PythonDir 是 python/backtest 的路径，子进程以它为工作目录。
	PythonDir string
	// DefaultLookback 是没在界面上指定回看根数时，默认往回拉多少根K线。
	DefaultLookback int
	// Timeout 是子进程的最长运行时间。
	Timeout time.Duration
}

// HTTPConfig 是 Web 界面（cmd/webui）的配置。
type HTTPConfig struct {
	// Addr 是监听地址，如 ":8080"。
	Addr string
}

// SecurityConfig 是加解密全部用户凭据用的服务端主密钥配置。多用户 SaaS 改造之后
// 登录鉴权是每个用户自己的邮箱+密码（bcrypt 哈希，见 internal/webui/handlers_auth.go），
// 不再是这里配的一个共享密码——MasterKey 纯粹是 internal/secretcrypto 的加密材料，
// 跟任何用户的登录口令无关，见 internal/webui/server.go 的 Server.masterKey 注释。
type SecurityConfig struct {
	// MasterKey 没有默认值：丢失它等于永久锁死全部用户已存的交易所/LLM 凭据，
	// 后果跟"忘了一次性登录密码"完全不对等，不能像旧的 AdminPassword 那样
	// "没配就随机生成一个"，cmd/webui/main.go、cmd/executor/main.go 的启动路径
	// 都要求它显式设置且足够长，否则直接拒绝启动。
	MasterKey string
}

// NotificationConfig 是提醒渠道里"部署级"共享的那部分配置——SMTP 服务器、Telegram
// bot token、VAPID 密钥对都是整个部署共用一份（不是每用户各自的密钥），跟
// TF_MASTER_KEY 是同一类东西：用户各自的部分（收件地址/chat_id/webhook URL/推送
// 订阅）存在 notification_channels 表里，见 internal/storage/notification_channels.go。
type NotificationConfig struct {
	SMTP     SMTPConfig
	Telegram TelegramConfig
	WebPush  WebPushConfig
}

// SMTPConfig 是发送提醒邮件用的服务端 SMTP 配置。
type SMTPConfig struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
}

// TelegramConfig 是 Telegram 渠道的部署级配置——BotToken 是这个部署唯一的一个
// Telegram bot，所有用户共用同一个 bot，各自的 chat_id 存在
// notification_channels 表里区分身份。
type TelegramConfig struct {
	BotToken string
}

// WebPushConfig 是 web push 渠道的 VAPID（RFC 8292）密钥对，整个部署生成一次、
// 长期不变——浏览器按这对密钥信任来自这个部署的推送。VAPIDSubject 是 RFC 8292
// 要求的联系方式，形如 "mailto:ops@example.com"，推送服务用它在滥用时联系部署方。
type WebPushConfig struct {
	VAPIDPublicKey  string
	VAPIDPrivateKey string
	VAPIDSubject    string
}

// Load 从环境变量读取配置，缺失项使用本地开发默认值。
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
