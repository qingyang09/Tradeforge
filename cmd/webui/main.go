// Command webui is the minimal usable interface from Stage 7: account
// registration/login, strategy config wizard, status board, strategy detail
// view. After the multi-tenant SaaS rework, each user registers and manages
// their own strategies and exchange credentials.
//
// Usage:
//
//	go run ./cmd/webui                 # listens on TF_HTTP_ADDR (default :8080)
//	go run ./cmd/webui -addr :9000     # override the listen address
//
// TF_MASTER_KEY must be set (at least 32 characters) — this is the
// server-side master key used to encrypt every user's exchange/LLM
// credentials. It is not anyone's login password, and losing it makes saved
// credentials permanently undecryptable, so an unset or too-short key
// refuses to start outright rather than falling back to a randomly
// generated admin password like older versions did.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"tradeforge/internal/agent"
	"tradeforge/internal/config"
	"tradeforge/internal/modules"
	"tradeforge/internal/storage"
	"tradeforge/internal/strategy"
	"tradeforge/internal/webui"
)

// minMasterKeyLen is the minimum required length for TF_MASTER_KEY — it only
// blocks obviously-too-weak values (e.g. typing "test" during development),
// it's not a real key-strength assessment.
const minMasterKeyLen = 32

func main() {
	addr := flag.String("addr", "", "HTTP listen address; falls back to TF_HTTP_ADDR or the default when empty")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := config.Load()
	if *addr != "" {
		cfg.HTTP.Addr = *addr
	}

	if len(cfg.Security.MasterKey) < minMasterKeyLen {
		fatal("TF_MASTER_KEY must be set (at least %d characters) to start — this is the "+
			"server-side master key that encrypts every user's credentials; losing it makes "+
			"saved exchange/LLM credentials permanently undecryptable, so one can't just be generated on the fly", minMasterKeyLen)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		fatal("failed to connect to the database: %v", err)
	}
	defer store.Close()

	// ag doesn't belong to any specific user — it's the shared team default
	// for users who haven't configured their own model under /settings; see
	// the envAgent field comment in internal/webui/server.go. Leaving it
	// unconfigured shouldn't block the whole interface: the status board and
	// strategy detail pages don't depend on the Agent, only the wizard does.
	reg := modules.NewDefaultRegistry()
	var ag *agent.Agent
	if llm, err := agent.NewAnthropicLLM(cfg.Agent); err != nil {
		logger.Warn("Agent not ready; the wizard is unavailable for users without their own model configured", "err", err)
	} else {
		ag = agent.New(llm, reg, cfg.Agent.MaxRetries)
	}

	srv, err := webui.New(store, ag, strategy.DefaultGate(), logger)
	if err != nil {
		fatal("failed to initialize the web interface: %v", err)
	}
	srv.SetBacktestRunnerConfig(webui.BacktestRunnerConfig{
		PythonExe:       cfg.BacktestRunner.PythonExe,
		PythonDir:       cfg.BacktestRunner.PythonDir,
		DefaultLookback: cfg.BacktestRunner.DefaultLookback,
		Timeout:         cfg.BacktestRunner.Timeout,
	})
	srv.SetMasterKey(cfg.Security.MasterKey)
	srv.SetVAPIDPublicKey(cfg.Notification.WebPush.VAPIDPublicKey)

	httpSrv := &http.Server{Addr: cfg.HTTP.Addr, Handler: srv.Routes()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("failed to shut down HTTP server", "err", err)
		}
	}()

	logger.Info("web interface started", "addr", cfg.HTTP.Addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("HTTP server exited unexpectedly: %v", err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
