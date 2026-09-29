package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/i18n"
	"tradeforge/internal/marketdata/okx"
	"tradeforge/pkg/types"
)

// candlesPageJSON 拼一份 OKX /market/candles 的真实响应形状：n 根K线，从
// startMs 开始每根隔 1 小时，倒序排列（OKX 原始响应就是倒序，okx.Client 自己翻转）。
func candlesPageJSON(n int, startMs int64) string {
	var b strings.Builder
	b.WriteString(`{"code":"0","msg":"","data":[`)
	const hourMs = 3600_000
	for i := 0; i < n; i++ {
		ts := startMs + int64(n-1-i)*hourMs
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`["`)
		b.WriteString(itoa(ts))
		b.WriteString(`","100","101","99","100.5","1000","100000","100000","0"]`)
	}
	b.WriteString(`]}`)
	return b.String()
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func backtestTestStrategy(id string) types.StrategyConfig {
	return types.StrategyConfig{
		ID: id, UserID: testDefaultUserID, Name: "回测按钮测试策略", Symbol: "BTCUSDT", Timeframe: types.TF1h,
		Combine: types.CombineAll,
		Modules: []types.ModuleConfig{
			{Module: "volume_breakout", Params: map[string]any{"window": 20, "multiplier": 2.0}},
		},
		Risk:      types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State:     types.StateDraft,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

// ---------- fetchCandleHistory：单页 / 翻页 ----------

func TestFetchCandleHistoryReturnsExactCountWithinSinglePage(t *testing.T) {
	okxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(candlesPageJSON(200, 1700000000000)))
	}))
	t.Cleanup(okxSrv.Close)

	srv := newTestServer(t, newFakeStore())
	srv.okxClient = okx.NewClient(okx.WithRESTBaseURL(okxSrv.URL))

	candles, err := srv.fetchCandleHistory(context.Background(), "BTCUSDT", types.TF1h, 150)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(candles) != 150 {
		t.Fatalf("candles 数量 = %d，期望 150", len(candles))
	}
	for i := 1; i < len(candles); i++ {
		if !candles[i].OpenTime.After(candles[i-1].OpenTime) {
			t.Fatalf("candles 应按时间升序排列，第 %d 根不满足", i)
		}
	}
}

func TestFetchCandleHistoryPaginatesAcrossMultipleRequests(t *testing.T) {
	var gotAfterValues []string
	okxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after := r.URL.Query().Get("after")
		gotAfterValues = append(gotAfterValues, after)
		if after == "" {
			// 第一页：最近的 300 根，起点定一个足够大的时间戳。
			w.Write([]byte(candlesPageJSON(300, 1700000000000+300*3600_000)))
			return
		}
		// 翻页请求：随便返回更早的一批，只要严格早于 after 即可。
		w.Write([]byte(candlesPageJSON(300, 1700000000000)))
	}))
	t.Cleanup(okxSrv.Close)

	srv := newTestServer(t, newFakeStore())
	srv.okxClient = okx.NewClient(okx.WithRESTBaseURL(okxSrv.URL))

	candles, err := srv.fetchCandleHistory(context.Background(), "BTCUSDT", types.TF1h, 500)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(candles) != 500 {
		t.Fatalf("candles 数量 = %d，期望 500（跨了一次翻页）", len(candles))
	}
	if len(gotAfterValues) < 2 {
		t.Fatalf("请求次数 = %d，期望至少 2 次（触发了翻页）", len(gotAfterValues))
	}
	for i := 1; i < len(candles); i++ {
		if !candles[i].OpenTime.After(candles[i-1].OpenTime) {
			t.Fatalf("跨页拼接后仍应严格按时间升序排列，第 %d 根不满足", i)
		}
	}
}

func TestFetchCandleHistoryStopsGracefullyWhenHistoryExhausted(t *testing.T) {
	calls := 0
	okxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("after") == "" {
			w.Write([]byte(candlesPageJSON(100, 1700000000000)))
			return
		}
		// 交易所已经没有更早的历史了。
		w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
	}))
	t.Cleanup(okxSrv.Close)

	srv := newTestServer(t, newFakeStore())
	srv.okxClient = okx.NewClient(okx.WithRESTBaseURL(okxSrv.URL))

	candles, err := srv.fetchCandleHistory(context.Background(), "BTCUSDT", types.TF1h, 1000)
	if err != nil {
		t.Fatalf("历史不够时不应报错，应该老实返回能拿到的部分：%v", err)
	}
	if len(candles) != 100 {
		t.Fatalf("candles 数量 = %d，期望 100（交易所只有这么多）", len(candles))
	}
}

// ---------- doRunBacktest：编排逻辑（拉数据、写临时文件、调用子进程、清理） ----------

func newBacktestTestServer(t *testing.T, okxSrv *httptest.Server) (*Server, *fakeStore) {
	t.Helper()
	store := newFakeStore()
	srv := newTestServer(t, store)
	srv.okxClient = okx.NewClient(okx.WithRESTBaseURL(okxSrv.URL))
	return srv, store
}

func fakeOKXCandlesServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") != "" {
			w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
			return
		}
		w.Write([]byte(candlesPageJSON(120, 1700000000000)))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDoRunBacktestWritesTempFilesInvokesPythonAndCleansUp(t *testing.T) {
	srv, _ := newBacktestTestServer(t, fakeOKXCandlesServer(t))

	var capturedWorkDir string
	var capturedArgs []string
	var stratFileContent, candlesFileContent, decisionsFileContent []byte
	var tempDataDir string
	// runPython 的 dir 参数是子进程的工作目录（backtestCfg.PythonDir，比如
	// "python/backtest"），跟数据文件所在的临时目录是两码事——数据文件路径以
	// 绝对路径的形式出现在 args 里（--strategy/--candles/--decisions），要从
	// args 里解析出来，不能假设它们在 dir 底下。在临时目录被清理之前
	// （doRunBacktest 用 defer os.RemoveAll 清理）读出内容，验证写给 Python
	// 子进程的东西确实是对的。
	srv.runPython = func(ctx context.Context, args []string, dir string) ([]byte, []byte, error) {
		capturedWorkDir = dir
		capturedArgs = args
		stratPath := argValue(args, "--strategy")
		candlesPath := argValue(args, "--candles")
		decisionsPath := argValue(args, "--decisions")
		if stratPath == "" || candlesPath == "" || decisionsPath == "" {
			t.Fatalf("参数里缺少 --strategy/--candles/--decisions 的值：%v", args)
		}
		tempDataDir = filepath.Dir(stratPath)

		var err error
		stratFileContent, err = os.ReadFile(stratPath)
		if err != nil {
			t.Errorf("读取临时策略文件失败：%v", err)
		}
		candlesFileContent, err = os.ReadFile(candlesPath)
		if err != nil {
			t.Errorf("读取临时K线文件失败：%v", err)
		}
		decisionsFileContent, err = os.ReadFile(decisionsPath)
		if err != nil {
			t.Errorf("读取临时决策文件失败：%v", err)
		}
		return []byte("回测结果已落库，记录 ID：fake-id\n"), nil, nil
	}

	sc := backtestTestStrategy("bbbbbbbb-2222-4222-8222-222222222222")
	banner, isErr := srv.doRunBacktest(context.Background(), sc, 100)
	if isErr {
		t.Fatalf("不应报错，banner=%q", banner)
	}
	if !strings.Contains(i18n.Render(i18n.DefaultLang, banner), "回测已完成") {
		t.Errorf("成功时的 banner 应该说明已完成，实际：%q", banner)
	}

	joined := strings.Join(capturedArgs, " ")
	for _, want := range []string{"-m", "tradeforge_backtest.cli", "--strategy", "--candles", "--decisions"} {
		if !strings.Contains(joined, want) {
			t.Errorf("子进程参数缺少 %q，实际：%v", want, capturedArgs)
		}
	}
	if capturedWorkDir != srv.backtestCfg.PythonDir {
		t.Errorf("子进程工作目录 = %q，期望 %q", capturedWorkDir, srv.backtestCfg.PythonDir)
	}

	var gotStrategy types.StrategyConfig
	if err := json.Unmarshal(stratFileContent, &gotStrategy); err != nil {
		t.Fatalf("临时策略文件不是合法 JSON：%v", err)
	}
	if gotStrategy.ID != sc.ID || gotStrategy.Symbol != sc.Symbol {
		t.Errorf("临时策略文件内容不符：%+v", gotStrategy)
	}
	if !strings.Contains(string(candlesFileContent), "open_time") {
		t.Errorf("临时K线文件看起来不是CSV：%q", string(candlesFileContent))
	}
	if !strings.Contains(string(decisionsFileContent), `"type":"meta"`) {
		t.Errorf("临时决策文件缺少 meta 行：%q", string(decisionsFileContent))
	}

	// 临时目录用完必须清理干净，不能在系统临时目录里越攒越多。
	if tempDataDir == "" {
		t.Fatal("没能定位到临时数据目录")
	}
	if _, err := os.Stat(tempDataDir); !os.IsNotExist(err) {
		t.Errorf("临时目录 %q 应该在函数返回后被清理，实际还存在（err=%v）", tempDataDir, err)
	}
}

// argValue 从命令行参数切片里找到 "--flag" 紧跟着的那个值。
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestDoRunBacktestSurfacesPythonStderrOnFailure(t *testing.T) {
	srv, _ := newBacktestTestServer(t, fakeOKXCandlesServer(t))
	srv.runPython = func(ctx context.Context, args []string, dir string) ([]byte, []byte, error) {
		return nil, []byte("错误：决策数与K线数不一致"), errUnitTestPythonFailed
	}

	sc := backtestTestStrategy("cccccccc-3333-4333-8333-333333333333")
	banner, isErr := srv.doRunBacktest(context.Background(), sc, 100)
	if !isErr {
		t.Fatal("Python 子进程失败时应该报错")
	}
	if !strings.Contains(i18n.Render(i18n.DefaultLang, banner), "决策数与K线数不一致") {
		t.Errorf("banner 应该包含子进程的 stderr 内容，实际：%q", banner)
	}
}

func TestDoRunBacktestFailsCleanlyWhenOKXUnreachable(t *testing.T) {
	// 故意指向一个不会响应的地址，模拟拉行情失败。
	deadSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadSrv.Close() // 关掉之后这个地址必然连不上

	srv, _ := newBacktestTestServer(t, deadSrv)
	srv.runPython = func(ctx context.Context, args []string, dir string) ([]byte, []byte, error) {
		t.Fatal("拉行情失败时不应该走到调用 Python 这一步")
		return nil, nil, nil
	}

	sc := backtestTestStrategy("dddddddd-4444-4444-8444-444444444444")
	banner, isErr := srv.doRunBacktest(context.Background(), sc, 100)
	if !isErr {
		t.Fatal("拉不到行情时应该报错")
	}
	if !strings.Contains(i18n.Render(i18n.DefaultLang, banner), "拉取历史行情失败") {
		t.Errorf("banner 应说明是拉行情失败，实际：%q", banner)
	}
}

// errUnitTestPythonFailed 是测试用的哨兵错误，只是让 doRunBacktest 走进失败分支。
var errUnitTestPythonFailed = &testPythonError{}

type testPythonError struct{}

func (e *testPythonError) Error() string { return "exit status 2" }

// ---------- HTTP 层：路由能不能对上、结果有没有渲染出来 ----------

func TestHandleRunBacktestRendersSuccessBanner(t *testing.T) {
	okxSrv := fakeOKXCandlesServer(t)
	srv, store := newBacktestTestServer(t, okxSrv)
	id := "eeeeeeee-5555-4555-8555-555555555555"
	store.strategies[id] = backtestTestStrategy(id)
	srv.runPython = func(ctx context.Context, args []string, dir string) ([]byte, []byte, error) {
		return []byte("回测结果已落库，记录 ID：fake-id\n"), nil, nil
	}

	w := postForm(srv, "/strategies/"+id+"/run-backtest", url.Values{"lookback": {"100"}})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	// 注意：不能简单搜子串 "banner-err"——布局模板的 <style> 里始终定义着
	// .banner-err 这条CSS规则，不管本次有没有真的渲染出错误提示都会出现。
	// 要断言的是"没有 class="banner-err" 的 div 被渲染出来"。
	if !strings.Contains(w.Body.String(), "回测已完成") || strings.Contains(w.Body.String(), `class="banner-err"`) {
		t.Errorf("应渲染成功 banner，实际：%s", w.Body.String())
	}
}

func TestHandleRunBacktestReturns404ForUnknownStrategy(t *testing.T) {
	srv, _ := newBacktestTestServer(t, fakeOKXCandlesServer(t))
	w := postForm(srv, "/strategies/ffffffff-6666-4666-8666-666666666666/run-backtest", url.Values{})
	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", w.Code)
	}
}
