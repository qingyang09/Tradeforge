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

// BacktestRunnerConfig configures how the "run backtest" button fetches real
// historical data and where it finds the Python matching-engine subprocess.
//
// The defaults assume cmd/webui is started from the project root -- that's
// how this project is run throughout (go run ./cmd/webui), so PythonDir's
// relative path always resolves to python/backtest.
type BacktestRunnerConfig struct {
	// PythonExe is the Python interpreter's name/path, default "python" (some
	// systems use "python3").
	PythonExe string
	// PythonDir is python/backtest's path (relative or absolute) -- the
	// subprocess runs "python -m tradeforge_backtest.cli" with this as its
	// working directory; python -m adds the current working directory to
	// sys.path, so the package doesn't need to be pip-installed first.
	PythonDir string
	// DefaultLookback is how many candles to pull when no lookback count is
	// specified.
	DefaultLookback int
	// Timeout is the subprocess's maximum run time; exceeding it counts as a
	// failure -- a backtest must never be allowed to hang some request
	// goroutine in the webui process indefinitely.
	Timeout time.Duration
}

// DefaultBacktestRunnerConfig is the development-environment default.
func DefaultBacktestRunnerConfig() BacktestRunnerConfig {
	return BacktestRunnerConfig{
		PythonExe:       "python",
		PythonDir:       "python/backtest",
		DefaultLookback: 1000,
		Timeout:         2 * time.Minute,
	}
}

// SetBacktestRunnerConfig overrides the "run backtest" button's
// configuration. If not called, DefaultBacktestRunnerConfig() is used.
func (s *Server) SetBacktestRunnerConfig(cfg BacktestRunnerConfig) {
	s.backtestCfg = cfg
}

// maxCandlesPerOKXRequest matches internal/marketdata/okx's per-request limit
// -- fetching more history than this needs paging, see fetchCandleHistory.
const maxCandlesPerOKXRequest = 300

// fetchCandleHistory fetches the most recent limit candles, automatically
// paging when that exceeds the per-request limit -- the same paging
// mechanism the builder page's chart uses when dragging left to auto-load
// more history (GET /api/candles's before param), just called directly
// against okxClient server-side here instead of going through that HTTP
// endpoint.
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
		return nil, fmt.Errorf("failed to fetch %s historical candles for %s: %w", tf, symbol, err)
	}
	for len(candles) < limit {
		earliest := candles[0].OpenTime
		more, err := s.okxClient.FetchCandlesBefore(ctx, symbol, tf, maxCandlesPerOKXRequest, earliest)
		if err != nil {
			return nil, fmt.Errorf("failed to page back further %s historical candles for %s: %w", tf, symbol, err)
		}
		more = filterBefore(more, earliest)
		if len(more) == 0 {
			break // already reached the earliest history the exchange can provide, not an error
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

// handleRunBacktest is the "run backtest" button's entry point: fetch real
// historical data, run internal/backtest.Replay in memory, hand the
// candidate data to the Python matching-engine subprocess to compute the
// full result and persist it -- all without the user needing to open a
// terminal and run the CLI tool by hand.
//
// This is a separate button from handleConfirmBacktest (the DRAFT ->
// BACKTESTED state advance): this one is only responsible for "producing a
// new backtest result" -- whether to advance the state based on it still
// requires the user to separately click "confirm backtest." A backtest
// result by itself never auto-advances the state; that decision should
// always be a human's to make.
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
		s.serverError(w, r, err)
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
		s.serverError(w, r, err)
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

// runPythonSubprocess is Server.runPython's default implementation, which
// really does spawn a Python subprocess. Tests replace this field with a
// fake implementation so the orchestration logic (fetching data, writing
// temp files, assembling args, handling errors) can be tested without
// actually having Python installed.
func runPythonSubprocess(ctx context.Context, args []string, dir string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = dir
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return outBuf.Bytes(), errBuf.Bytes(), err
}
