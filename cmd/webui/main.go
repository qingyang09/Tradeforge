// Command webui 是阶段 7 的最小可用界面：账户注册登录、策略配置向导、状态看板、
// 策略详情。多用户 SaaS 改造之后每个用户自己注册、自己管理策略和交易所凭据。
//
// 用法：
//
//	go run ./cmd/webui                 # 监听 TF_HTTP_ADDR（默认 :8080）
//	go run ./cmd/webui -addr :9000     # 覆盖监听地址
//
// 必须设置 TF_MASTER_KEY（至少 32 位）——这是给所有用户的交易所/LLM 凭据加密用的
// 服务端主密钥，不是任何人的登录密码，丢失它会让已保存的凭据永久解不开，所以
// 未设置或太短直接拒绝启动，不会像旧版本的管理密码那样随机生成一个将就用。
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

// minMasterKeyLen 是 TF_MASTER_KEY 的最低长度要求——只挡明显太弱的值（比如开发时
// 手滑填了 "test" 这种），不是在做真正的密钥强度评估。
const minMasterKeyLen = 32

func main() {
	addr := flag.String("addr", "", "HTTP 监听地址，留空则使用 TF_HTTP_ADDR 或默认值")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := config.Load()
	if *addr != "" {
		cfg.HTTP.Addr = *addr
	}

	if len(cfg.Security.MasterKey) < minMasterKeyLen {
		fatal("必须设置 TF_MASTER_KEY（至少 %d 位）才能启动——这是加密全部用户凭据用的"+
			"服务端主密钥，丢失它会让已保存的交易所/LLM 凭据永久解不开，不能随便生成一个", minMasterKeyLen)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := storage.Open(ctx, cfg.Postgres)
	if err != nil {
		fatal("连接数据库失败：%v", err)
	}
	defer store.Close()

	// ag 不属于任何具体用户，是没有单独在 /settings 配置模型的用户共用的团队默认值，
	// 见 internal/webui/server.go 的 envAgent 字段注释。未配置不应阻塞整个界面：
	// 看板与策略详情页不依赖 Agent，只有向导页面需要。
	reg := modules.NewDefaultRegistry()
	var ag *agent.Agent
	if llm, err := agent.NewAnthropicLLM(cfg.Agent); err != nil {
		logger.Warn("Agent 未就绪，向导页面对未单独配置模型的用户不可用", "err", err)
	} else {
		ag = agent.New(llm, reg, cfg.Agent.MaxRetries)
	}

	srv, err := webui.New(store, ag, strategy.DefaultGate(), logger)
	if err != nil {
		fatal("初始化 Web 界面失败：%v", err)
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
			logger.Error("关闭 HTTP 服务失败", "err", err)
		}
	}()

	logger.Info("Web 界面已启动", "addr", cfg.HTTP.Addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal("HTTP 服务异常退出：%v", err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
