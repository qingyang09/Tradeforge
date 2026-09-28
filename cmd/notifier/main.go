// Command notifier 是信号提醒层的入口。
//
// 它从 Kafka 消费组合引擎产出的决策，跟 cmd/executor 是同一个 topic 的独立消费组——
// 两者互不知晓对方存在，互不干扰（见 internal/messaging.NewDecisionReader 的
// "groupID 相同的实例之间会分摊分区"注释：不同 groupID 是各自完整的一份消费）。
//
// 跟 cmd/executor 的关键差异：执行层只关心 LIVE（且只在注册时检查一次），提醒层
// 同时服务 PAPER_TRADING（预览/验证体验）和 LIVE（真实提醒）——这是产品决策：
// 模拟盘阶段也要提醒，帮用户在真金白银之前建立对策略的信任感。LIVE_ELIGIBLE/
// SUSPENDED/DRAFT/BACKTESTED 都不提醒。
//
// 用法：
//
//	go run ./cmd/notifier                                    # 多用户模式：服务全部用户
//	go run ./cmd/notifier -owner-email you@example.com        # 单用户模式（较少用，主要为部署对称性保留）
//	go run ./cmd/notifier -dry-run                            # 只打印将要发送的内容，不真正调用任何外部接口
//
// 部署级配置（跟 TF_MASTER_KEY 同一类：整个部署共用一份，不是每用户各自的）：
// TF_SMTP_HOST/TF_SMTP_PORT/TF_SMTP_USERNAME/TF_SMTP_PASSWORD/TF_SMTP_FROM（邮件渠道）、
// TF_TELEGRAM_BOT_TOKEN（Telegram 渠道）、TF_VAPID_PUBLIC_KEY/TF_VAPID_PRIVATE_KEY/
// TF_VAPID_SUBJECT（web push 渠道）。任何一组缺失只会让对应渠道在发送时报错并记入
// notification_deliveries 的 failed 记录，不会阻止进程启动或影响其它渠道——
// 一个部署完全可以只配邮件+webhook，不配 Telegram/push。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"tradeforge/internal/config"
	"tradeforge/internal/messaging"
	"tradeforge/internal/notify"
	"tradeforge/internal/storage"
)

func main() {
	ownerEmail := flag.String("owner-email", "",
		"单用户模式：只服务这一个用户（可选，该用户的邮箱）；留空则是多用户模式，服务全部用户的决策")
	group := flag.String("group", "tradeforge-notifier", "Kafka 消费组 ID")
	dryRun := flag.Bool("dry-run", false, "只打印将要发送的内容，不真正调用邮件/Telegram/webhook/推送接口")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		fatal("连接数据库失败：%v", err)
	}
	defer store.Close()

	// 留空 -owner-email = 多用户模式：ownerUserID 保持空字符串，handleDecision 不做
	// 任何按用户过滤，服务全部用户的决策。
	ownerUserID := ""
	if trimmed := strings.TrimSpace(*ownerEmail); trimmed != "" {
		owner, err := store.GetUserByEmail(ctx, trimmed)
		if err != nil {
			fatal("找不到 -owner-email 指定的用户 %q：%v", trimmed, err)
		}
		ownerUserID = owner.ID
	}

	warnIfUnconfigured(cfg.Notification, logger)
	senders := buildSenders(cfg, store, *dryRun, logger)

	if err := messaging.EnsureTopics(ctx, cfg.Kafka); err != nil {
		logger.Warn("创建 Kafka topic 失败，将依赖自动创建", "err", err)
	}
	reader := messaging.NewDecisionReader(cfg.Kafka, *group)
	defer reader.Close()

	logger.Info("开始消费决策用于提醒",
		"topic", cfg.Kafka.DecisionTopic, "group", *group, "multi_tenant", ownerUserID == "", "dry_run", *dryRun)
	consume(ctx, reader, store, senders, ownerUserID, logger)

	logger.Info("收到停止信号，正在关闭提醒层")
}

// warnIfUnconfigured 对每一组缺失的部署级配置发一条警告，不阻止启动——一个部署
// 完全可以只配置部分渠道。跟 cmd/webui/main.go 对未配置 Agent 时的处理是同一个
// "缺配置只降级、不拒绝启动"原则，但比它更进一步：这里连拒绝启动都不做，因为
// 四个渠道里任意子集缺失都不影响其它渠道正常工作。
func warnIfUnconfigured(n config.NotificationConfig, logger *slog.Logger) {
	if n.SMTP.Host == "" {
		logger.Warn("未配置 SMTP（TF_SMTP_HOST 等），邮件渠道不可用")
	}
	if n.Telegram.BotToken == "" {
		logger.Warn("未配置 Telegram bot token（TF_TELEGRAM_BOT_TOKEN），Telegram 渠道不可用")
	}
	if n.WebPush.VAPIDPrivateKey == "" {
		logger.Warn("未配置 VAPID 密钥（TF_VAPID_PRIVATE_KEY 等），web push 渠道不可用")
	}
}

func buildSenders(cfg config.Config, store *storage.Store, dryRun bool, logger *slog.Logger) channelSenders {
	return channelSenders{
		masterKey: cfg.Security.MasterKey,
		dryRun:    dryRun,
		logger:    logger,
		email:     notify.NewSMTPEmailSender(cfg.Notification.SMTP),
		telegram:  notify.NewTelegramClient(cfg.Notification.Telegram.BotToken),
		webhook:   notify.NewHTTPWebhookSender(http.DefaultClient),
		webpush:   notify.NewVAPIDWebPushSender(cfg.Notification.WebPush),
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
