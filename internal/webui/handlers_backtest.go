package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"tradeforge/internal/backtest"
	"tradeforge/internal/i18n"
	"tradeforge/internal/marketdata"
	"tradeforge/internal/storage"
	"tradeforge/pkg/types"
)

// BacktestRunnerConfig 配置"运行回测"按钮怎么去拉真实历史数据、去哪找 Python
// 撮合引擎子进程。
//
// 默认值假设 cmd/webui 是从项目根目录启动的——这个项目全程都是这么跑的
// （go run ./cmd/webui），PythonDir 的相对路径因此总能对上 python/backtest。
type BacktestRunnerConfig struct {
	// PythonExe 是 python 解释器名字/路径，默认 "python"（某些系统上是 "python3"）。
	PythonExe string
	// PythonDir 是 python/backtest 的路径（相对或绝对）——子进程以它为工作目录
	// 运行 "python -m tradeforge_backtest.cli"，python -m 会把当前工作目录加进
	// sys.path，不需要先 pip install 这个包。
	PythonDir string
	// DefaultLookback 是没指定回看根数时，往回拉多少根K线。
	DefaultLookback int
	// Timeout 是子进程的最长运行时间，超时视为失败——不能让一次回测卡死整个
	// webui 进程的某个请求 goroutine 无限期挂着。
	Timeout time.Duration
}

// DefaultBacktestRunnerConfig 是开发环境的默认配置。
func DefaultBacktestRunnerConfig() BacktestRunnerConfig {
	return BacktestRunnerConfig{
		PythonExe:       "python",
		PythonDir:       "python/backtest",
		DefaultLookback: 1000,
		Timeout:         2 * time.Minute,
	}
}

// SetBacktestRunnerConfig 覆盖"运行回测"按钮的配置。不调用则使用
// DefaultBacktestRunnerConfig()。
func (s *Server) SetBacktestRunnerConfig(cfg BacktestRunnerConfig) {
	s.backtestCfg = cfg
}

// maxCandlesPerOKXRequest 跟 internal/marketdata/okx 的单次请求上限保持一致——
// 拉取超过这个数量的历史需要翻页，见 fetchCandleHistory。
const maxCandlesPerOKXRequest = 300

// fetchCandleHistory 拉取最近 limit 根K线，超过单次请求上限时自动翻页——
// 跟画板页图表左拖到底自动补历史用的是同一套翻页机制（GET /api/candles 的
// before 参数），这里是服务端内部直接调用 okxClient，不经过那个 HTTP 接口。
func (s *Server) fetchCandleHistory(ctx context.Context, symbol string, tf types.Timeframe, limit int) ([]types.Candle, error) {
	if limit <= 0 {
		limit = 1
	}
	page := limit
	if page > maxCandlesPerOKXRequest {
		page = maxCandlesPerOKXRequest
	}
	candles, err := s.okxClient.FetchCandles(ctx, symbol, tf, page)
	if err != nil {
		return nil, fmt.Errorf("拉取 %s 的 %s 周期历史K线失败：%w", symbol, tf, err)
	}
	for len(candles) < limit {
		earliest := candles[0].OpenTime
		more, err := s.okxClient.FetchCandlesBefore(ctx, symbol, tf, maxCandlesPerOKXRequest, earliest)
		if err != nil {
			return nil, fmt.Errorf("翻页拉取 %s 的 %s 周期历史K线失败：%w", symbol, tf, err)
		}
		more = filterBefore(more, earliest)
		if len(more) == 0 {
			break // 已经拉到交易所能提供的最早历史，不再是错误
		}
		candles = append(more, candles...)
	}
	if len(candles) > limit {
		candles = candles[len(candles)-limit:]
	}
	return candles, nil
}

func filterBefore(candles []types.Candle, cutoff time.Time) []types.Candle {
	out := candles[:0:0]
	for _, c := range candles {
		if c.OpenTime.Before(cutoff) {
			out = append(out, c)
		}
	}
	return out
}

// handleRunBacktest 是"运行回测"按钮的入口：拉真实历史数据、在内存里跑一遍
// internal/backtest.Replay、把候选数据交给 Python 撮合引擎子进程算出完整结果并
// 写库——全程不需要用户去开终端手动跑 CLI 工具。
//
// 跟 handleConfirmBacktest（DRAFT → BACKTESTED 的状态推进）是两个独立的按钮：
// 这个按钮只负责"产出一份新的回测结果"，产出后是否要据此推进状态仍然需要用户
// 另外点"确认回测"——回测结果本身不自动导致状态推进，这个决定本来就该由人做。
func (s *Server) handleRunBacktest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !looksLikeUUID(id) {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	userID, _ := currentUserID(r)

	sc, err := s.store.GetStrategy(ctx, userID, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, i18n.T(resolveLang(r), "webui.strategy_detail.not_found"), http.StatusNotFound)
			return
		}
		s.serverError(w, err)
		return
	}

	lookback := s.backtestCfg.DefaultLookback
	if raw := strings.TrimSpace(r.FormValue("lookback")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			lookback = n
		}
	}

	banner, bannerErr := s.doRunBacktest(ctx, sc, lookback)

	data, err := s.loadStrategyDetail(ctx, userID, id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	data.Banner, data.BannerErr = banner, bannerErr
	s.renderPage(w, r, data.Strategy.Name, "strategy_detail_content", data)
}

func (s *Server) doRunBacktest(ctx context.Context, sc types.StrategyConfig, lookback int) (banner types.Message, isErr bool) {
	md := types.MarketData{Symbol: sc.Symbol, Timeframe: sc.Timeframe}
	candles, err := s.fetchCandleHistory(ctx, sc.Symbol, sc.Timeframe, lookback)
	if err != nil {
		return types.Msg("webui.strategy_detail.banner.fetch_candles_failed", "err", err.Error()), true
	}
	md.Candles = candles

	contextFeeds := make(map[types.Timeframe]types.MarketData)
	for _, tf := range sc.RequiredTimeframes() {
		if tf == sc.Timeframe {
			continue
		}
		ctxCandles, err := s.fetchCandleHistory(ctx, sc.Symbol, tf, lookback)
		if err != nil {
			return types.Msg("webui.strategy_detail.banner.fetch_candles_failed", "err", err.Error()), true
		}
		contextFeeds[tf] = types.MarketData{Symbol: sc.Symbol, Timeframe: tf, Candles: ctxCandles}
	}

	meta, decisions, err := backtest.Replay(ctx, sc, md, contextFeeds, backtest.DefaultWindow, s.logger)
	if err != nil {
		return types.Msg("webui.strategy_detail.banner.replay_failed", "err", err.Error()), true
	}

	dir, err := os.MkdirTemp("", "tf-run-backtest-*")
	if err != nil {
		return types.Msg("webui.strategy_detail.banner.tmpdir_failed", "err", err.Error()), true
	}
	defer os.RemoveAll(dir)

	stratPath := filepath.Join(dir, "strategy.json")
	stratBlob, err := json.Marshal(sc)
	if err != nil {
		return types.Msg("webui.strategy_detail.banner.marshal_strategy_failed", "err", err.Error()), true
	}
	if err := os.WriteFile(stratPath, stratBlob, 0o600); err != nil {
		return types.Msg("webui.strategy_detail.banner.write_strategy_failed", "err", err.Error()), true
	}

	candlesPath := filepath.Join(dir, "candles.csv")
	candlesFile, err := os.Create(candlesPath)
	if err != nil {
		return types.Msg("webui.strategy_detail.banner.create_candles_file_failed", "err", err.Error()), true
	}
	writeErr := marketdata.WriteCSV(candlesFile, md)
	candlesFile.Close()
	if writeErr != nil {
		return types.Msg("webui.strategy_detail.banner.write_candles_failed", "err", writeErr.Error()), true
	}

	decisionsPath := filepath.Join(dir, "decisions.jsonl")
	decisionsFile, err := os.Create(decisionsPath)
	if err != nil {
		return types.Msg("webui.strategy_detail.banner.create_decisions_file_failed", "err", err.Error()), true
	}
	writeErr = backtest.WriteJSONL(decisionsFile, meta, decisions)
	decisionsFile.Close()
	if writeErr != nil {
		return types.Msg("webui.strategy_detail.banner.write_decisions_failed", "err", writeErr.Error()), true
	}

	pyCtx, cancel := context.WithTimeout(ctx, s.backtestCfg.Timeout)
	defer cancel()
	args := []string{s.backtestCfg.PythonExe, "-m", "tradeforge_backtest.cli",
		"--strategy", stratPath, "--candles", candlesPath, "--decisions", decisionsPath}
	stdout, stderr, err := s.runPython(pyCtx, args, s.backtestCfg.PythonDir)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(stdout))
		}
		if msg == "" {
			msg = err.Error()
		}
		return types.Msg("webui.strategy_detail.banner.python_engine_failed", "msg", msg), true
	}

	tradeCount := 0
	for _, d := range decisions {
		if d.Triggered {
			tradeCount++
		}
	}
	return types.Msg("webui.strategy_detail.banner.run_backtest_success",
		"count", len(candles), "timeframe", sc.Timeframe, "triggers", tradeCount), false
}

// runPythonSubprocess 是 Server.runPython 的默认实现，真的拉起一个 python 子进程。
// 测试用假实现替换这个字段，不需要真的装 Python 就能测编排逻辑（拉数据、写临时
// 文件、组装参数、处理错误）。
func runPythonSubprocess(ctx context.Context, args []string, dir string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}
