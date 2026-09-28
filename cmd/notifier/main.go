// Command notifier is the entry point for the signal alert layer.
//
// It consumes decisions produced by the composition engine from Kafka, as
// an independent consumer group from cmd/executor's, on the same topic —
// the two are unaware of each other and don't interfere (see the
// "instances sharing the same groupID split partitions between them"
// comment on internal/messaging.NewDecisionReader: different groupIDs each
// get their own full copy of the stream).
//
// The key difference from cmd/executor: the execution layer only cares
// about LIVE (and only checks once, at registration time), while the alert
// layer serves both PAPER_TRADING (preview/validation experience) and LIVE
// (real alerts) — this is a product decision: alerts should fire during
// paper trading too, to help users build trust in a strategy before
// risking real money. LIVE_ELIGIBLE/SUSPENDED/DRAFT/BACKTESTED never alert.
//
// Usage:
//
//	go run ./cmd/notifier                                    # multi-tenant mode: serves all users
//	go run ./cmd/notifier -owner-email you@example.com        # single-tenant mode (less common, kept mainly for deployment symmetry)
//	go run ./cmd/notifier -dry-run                            # only print what would be sent, without calling any external API
//
// Deployment-level config (same category as TF_MASTER_KEY: one shared value
// per deployment, not per user): TF_SMTP_HOST/TF_SMTP_PORT/
// TF_SMTP_USERNAME/TF_SMTP_PASSWORD/TF_SMTP_FROM (email channel),
// TF_TELEGRAM_BOT_TOKEN (Telegram channel), TF_VAPID_PUBLIC_KEY/
// TF_VAPID_PRIVATE_KEY/TF_VAPID_SUBJECT (web push channel). Any missing
// group just makes the corresponding channel error at send time and get
// recorded as a failed row in notification_deliveries — it never blocks
// startup or affects other channels; a deployment can perfectly well
// configure only email+webhook and skip Telegram/push.
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
		"single-tenant mode: serve only this one user (optional, that user's email); "+
			"empty means multi-tenant mode, serving all users' decisions")
	group := flag.String("group", "tradeforge-notifier", "Kafka consumer group ID")
	dryRun := flag.Bool("dry-run", false, "only print what would be sent, without actually calling email/Telegram/webhook/push APIs")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		fatal("failed to connect to the database: %v", err)
	}
	defer store.Close()

	// Empty -owner-email = multi-tenant mode: ownerUserID stays an empty
	// string, and handleDecision does no per-user filtering, serving
	// decisions for all users.
	ownerUserID := ""
	if trimmed := strings.TrimSpace(*ownerEmail); trimmed != "" {
		owner, err := store.GetUserByEmail(ctx, trimmed)
		if err != nil {
			fatal("could not find the user specified by -owner-email %q: %v", trimmed, err)
		}
		ownerUserID = owner.ID
	}

	warnIfUnconfigured(cfg.Notification, logger)
	senders := buildSenders(cfg, store, *dryRun, logger)

	if err := messaging.EnsureTopics(ctx, cfg.Kafka); err != nil {
		logger.Warn("failed to create Kafka topics, relying on auto-creation", "err", err)
	}
	reader := messaging.NewDecisionReader(cfg.Kafka, *group)
	defer reader.Close()

	logger.Info("started consuming decisions for alerts",
		"topic", cfg.Kafka.DecisionTopic, "group", *group, "multi_tenant", ownerUserID == "", "dry_run", *dryRun)
	consume(ctx, reader, store, senders, ownerUserID, logger)

	logger.Info("received stop signal, shutting down the alert layer")
}

// warnIfUnconfigured logs a warning for each missing group of
// deployment-level config, without blocking startup — a deployment can
// perfectly well configure only some channels. This is the same "missing
// config only degrades, never refuses to start" principle cmd/webui/main.go
// applies when the Agent isn't configured, but taken a step further: it
// doesn't even refuse to start here, because any subset of the four
// channels being missing has no effect on the others working normally.
func warnIfUnconfigured(n config.NotificationConfig, logger *slog.Logger) {
	if n.SMTP.Host == "" {
		logger.Warn("SMTP not configured (TF_SMTP_HOST etc.), email channel unavailable")
	}
	if n.Telegram.BotToken == "" {
		logger.Warn("Telegram bot token not configured (TF_TELEGRAM_BOT_TOKEN), Telegram channel unavailable")
	}
	if n.WebPush.VAPIDPrivateKey == "" {
		logger.Warn("VAPID keys not configured (TF_VAPID_PRIVATE_KEY etc.), web push channel unavailable")
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
