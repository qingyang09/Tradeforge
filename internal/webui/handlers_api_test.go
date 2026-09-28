package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tradeforge/internal/marketdata/okx"
	"tradeforge/internal/modules"
)

// realCandlesResponseJSON 复用 internal/marketdata/okx 测试里已经验证过的真实响应形状，
// 不重新编造一份可能跟真实接口对不上的假数据。
const realCandlesResponseJSON = `{"code":"0","msg":"","data":[` +
	`["1787184000000","69337.2","69599.4","69319.9","69514.5","111.39520843","7743007.753348444","7743007.753348444","0"],` +
	`["1787180400000","69264.7","69462.5","69160.6","69337.1","333.0511483","23087731.217311397","23087731.217311397","1"],` +
	`["1787176800000","69718.5","69846.6","69000","69264.5","509.18666667","35301481.318071814","35301481.318071814","1"]]}`

func fakeOKXServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestServerWithOKX(t *testing.T, srv *httptest.Server) *Server {
	t.Helper()
	s := newTestServer(t, newFakeStore())
	s.okxClient = okx.NewClient(okx.WithRESTBaseURL(srv.URL))
	return s
}

func TestHandleAPICandlesReturnsFormattedCandles(t *testing.T) {
	okxSrv := fakeOKXServer(t, realCandlesResponseJSON)
	srv := newTestServerWithOKX(t, okxSrv)

	w := getPage(srv, "/api/candles/BTCUSDT?timeframe=1h&limit=3")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	var out []apiCandle
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if len(out) != 3 {
		t.Fatalf("candle 数量 = %d，期望 3", len(out))
	}
	if out[2].Close != 69514.5 {
		t.Errorf("最后一根收盘价 = %v，期望 69514.5", out[2].Close)
	}
	if out[0].Time >= out[2].Time {
		t.Errorf("应按时间升序排列，实际 out[0].Time=%d out[2].Time=%d", out[0].Time, out[2].Time)
	}
}

func TestHandleAPICandlesForwardsBeforeAsOKXAfterParam(t *testing.T) {
	var gotAfter string
	okxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAfter = r.URL.Query().Get("after")
		w.Write([]byte(realCandlesResponseJSON))
	}))
	t.Cleanup(okxSrv.Close)
	srv := newTestServerWithOKX(t, okxSrv)

	w := getPage(srv, "/api/candles/BTCUSDT?timeframe=1h&limit=3&before=1787176800")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if gotAfter != "1787176800000" {
		t.Errorf("转发给 OKX 的 after 参数 = %s，期望把 before 的 Unix 秒换算成毫秒 1787176800000", gotAfter)
	}
}

func TestHandleAPICandlesRejectsMalformedBefore(t *testing.T) {
	srv := newTestServerWithOKX(t, fakeOKXServer(t, realCandlesResponseJSON))
	w := getPage(srv, "/api/candles/BTCUSDT?timeframe=1h&before=not-a-timestamp")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", w.Code)
	}
}

func TestHandleAPICandlesRejectsMalformedSymbol(t *testing.T) {
	srv := newTestServerWithOKX(t, fakeOKXServer(t, realCandlesResponseJSON))
	w := getPage(srv, "/api/candles/BTC-USDT")
	if w.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d，期望 404", w.Code)
	}
}

func TestHandleAPICandlesRejectsBadTimeframe(t *testing.T) {
	srv := newTestServerWithOKX(t, fakeOKXServer(t, realCandlesResponseJSON))
	w := getPage(srv, "/api/candles/BTCUSDT?timeframe=3w")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", w.Code)
	}
}

func TestHandleAPIModulesListsAllRegisteredModules(t *testing.T) {
	srv := newTestServer(t, newFakeStore())
	w := getPage(srv, "/api/modules")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	var out []apiModule
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v", err)
	}
	want := modules.NewDefaultRegistry().Names()
	if len(out) != len(want) {
		t.Fatalf("模块数量 = %d，期望 %d", len(out), len(want))
	}
	for _, name := range want {
		found := false
		for _, m := range out {
			if m.Name == name {
				found = true
				if len(m.Params) == 0 {
					t.Errorf("模块 %s 应该带有参数规格", name)
				}
				break
			}
		}
		if !found {
			t.Errorf("模块目录应包含 %s", name)
		}
	}
}

func TestHandleAPIPreviewSupportResistanceReturnsLevels(t *testing.T) {
	// 构造一段震荡行情：反复触及同一个高点和低点，确保能聚出关键位。
	base := int64(1700000000)
	prices := []float64{100, 110, 100, 110, 100, 110, 100, 110, 100, 110, 100, 110, 100, 110, 100}
	rows := make([]string, len(prices))
	for i, p := range prices {
		ts := (base + int64(i)*3600) * 1000
		rows[len(prices)-1-i] = fmt.Sprintf(`["%d","%g","%g","%g","%g","100","1000","1000","1"]`, ts, p, p+1, p-1, p)
	}
	body := `{"code":"0","msg":"","data":[` + strings.Join(rows, ",") + `]}`

	srv := newTestServerWithOKX(t, fakeOKXServer(t, body))
	w := getPage(srv, "/api/preview/support-resistance/BTCUSDT?timeframe=1h&lookback=50&pivot_strength=1&min_touches=2")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	var out apiSupportResistancePreview
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	// 不强断言具体检测到几个关键位（那是 support_resistance 模块自己的测试覆盖范围），
	// 这里只验证预览接口把真实模块的输出原样透传出来了。
	if out.WindowStart == 0 {
		t.Error("window_start 应该带上，供画板标出分析窗口起点")
	}
}

func TestHandleAPIPreviewSupportResistanceRejectsBadParams(t *testing.T) {
	srv := newTestServerWithOKX(t, fakeOKXServer(t, realCandlesResponseJSON))
	w := getPage(srv, "/api/preview/support-resistance/BTCUSDT?lookback=not-a-number")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", w.Code)
	}
}

// candleRow 构造一行 OKX K 线数据（[ts, open, high, low, close, volume, ...confirmed]）。
func candleRow(ts int64, open, high, low, close, volume float64) string {
	return fmt.Sprintf(`["%d","%g","%g","%g","%g","%g","1000","1000","1"]`, ts*1000, open, high, low, close, volume)
}

// okxBody 把按时间正序给出的行反转成 OKX 真实响应的倒序（最新在前），
// 跟 FetchCandles 内部会再翻回正序的约定配套。
func okxBody(rowsChronological []string) string {
	rev := make([]string, len(rowsChronological))
	for i, r := range rowsChronological {
		rev[len(rowsChronological)-1-i] = r
	}
	return `{"code":"0","msg":"","data":[` + strings.Join(rev, ",") + `]}`
}

func TestHandleAPIPreviewFakeoutDetectsRealFakeout(t *testing.T) {
	base := int64(1700000000)
	var rows []string
	ts := base
	nextTS := func() int64 { v := ts; ts += 3600; return v }

	// 震荡出一个盘整区间（约 100~110），再接一根真突破（113）、一根收回（105）——
	// reversal_window=1 时正好落在扫描窗口里。
	prices := []float64{100, 110, 100, 110, 100, 110, 100, 110, 100, 110, 100, 110, 100, 110, 100}
	for _, p := range prices {
		rows = append(rows, candleRow(nextTS(), p, p+1, p-1, p, 100))
	}
	rows = append(rows, candleRow(nextTS(), 110, 114, 109, 113, 1500)) // 突破
	rows = append(rows, candleRow(nextTS(), 113, 113, 104, 105, 1500)) // 收回

	srv := newTestServerWithOKX(t, fakeOKXServer(t, okxBody(rows)))
	w := getPage(srv, "/api/preview/fakeout/BTCUSDT?timeframe=1h&min_range_bars=10&range_tightness=0.12&reversal_window=1")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	var out apiFakeoutPreview
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if out.Event != "fakeout_resistance" {
		t.Fatalf("event = %q，期望 fakeout_resistance；完整响应：%+v", out.Event, out)
	}
	if out.Direction != "SHORT" {
		t.Errorf("direction = %q，期望 SHORT", out.Direction)
	}
	if !out.RangeFound {
		t.Error("range_found 应为 true——即便检测到了假突破，仍应带上区间高低点供画板画常规参考线")
	}
	if out.RangeHigh <= out.RangeLow {
		t.Errorf("range_high(%v) 应大于 range_low(%v)", out.RangeHigh, out.RangeLow)
	}
	if out.WindowStart == 0 {
		t.Error("window_start 应该带上，供画板标出分析窗口起点")
	}
}

func TestHandleAPIPreviewFakeoutNoEventStillReturnsRange(t *testing.T) {
	base := int64(1700000000)
	var rows []string
	ts := base
	nextTS := func() int64 { v := ts; ts += 3600; return v }
	prices := []float64{100, 110, 100, 110, 100, 110, 100, 110, 100, 110, 100, 110, 100, 110, 100}
	for _, p := range prices {
		rows = append(rows, candleRow(nextTS(), p, p+1, p-1, p, 100))
	}

	srv := newTestServerWithOKX(t, fakeOKXServer(t, okxBody(rows)))
	w := getPage(srv, "/api/preview/fakeout/BTCUSDT?timeframe=1h&min_range_bars=10&range_tightness=0.12&reversal_window=1")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	var out apiFakeoutPreview
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if out.Event != "none" {
		t.Errorf("event = %q，期望 none（没有构造突破+收回）", out.Event)
	}
	if !out.RangeFound {
		t.Error("没有触发假突破时也应该带上正在监控的区间高低点，不能给画板一份空数据")
	}
}

func TestHandleAPIPreviewFakeoutRejectsBadParams(t *testing.T) {
	srv := newTestServerWithOKX(t, fakeOKXServer(t, realCandlesResponseJSON))
	w := getPage(srv, "/api/preview/fakeout/BTCUSDT?reversal_window=not-a-number")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", w.Code)
	}
}

func TestHandleAPIPreviewPOCReturnsRealPrice(t *testing.T) {
	base := int64(1700000000)
	var rows []string
	ts := base
	nextTS := func() int64 { v := ts; ts += 3600; return v }
	// 104 附近的成交量远大于其它价位，POC 应该落在那附近。
	rows = append(rows, candleRow(nextTS(), 100, 100, 100, 100, 100))
	rows = append(rows, candleRow(nextTS(), 102, 102, 102, 102, 100))
	rows = append(rows, candleRow(nextTS(), 104, 104, 104, 104, 100000))
	rows = append(rows, candleRow(nextTS(), 106, 106, 106, 106, 100))
	rows = append(rows, candleRow(nextTS(), 108, 108, 108, 108, 100))

	srv := newTestServerWithOKX(t, fakeOKXServer(t, okxBody(rows)))
	w := getPage(srv, "/api/preview/poc/BTCUSDT?timeframe=1h&bucket_count=8")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	var out apiPOCPreview
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if !out.Available {
		t.Fatal("Available 应为 true")
	}
	if !out.IsApproximate {
		t.Error("POC 必须标注 is_approximate")
	}
	if out.Price < 103.5 || out.Price > 105.5 {
		t.Errorf("price = %v，期望落在成交量密集的 104 附近", out.Price)
	}
	if out.WindowStart == 0 {
		t.Error("window_start 应该带上，供画板标出分析窗口起点")
	}
}

func TestHandleAPIPreviewPOCUnavailableWhenNoPriceMovement(t *testing.T) {
	base := int64(1700000000)
	rows := []string{
		candleRow(base, 100, 100, 100, 100, 1000),
		candleRow(base+3600, 100, 100, 100, 100, 1000),
	}
	srv := newTestServerWithOKX(t, fakeOKXServer(t, okxBody(rows)))
	w := getPage(srv, "/api/preview/poc/BTCUSDT?timeframe=1h")
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	var out apiPOCPreview
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON：%v，body=%s", err, w.Body.String())
	}
	if out.Available {
		t.Error("价格完全没有波动时 Available 应为 false，不能画一条价格为零的假线")
	}
}

func TestHandleAPIPreviewPOCRejectsBadParams(t *testing.T) {
	srv := newTestServerWithOKX(t, fakeOKXServer(t, realCandlesResponseJSON))
	w := getPage(srv, "/api/preview/poc/BTCUSDT?bucket_count=not-a-number")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d，期望 400", w.Code)
	}
}
