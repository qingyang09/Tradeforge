package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// realCandlesResponseJSON 是从 GET /api/v5/market/candles?instId=BTC-USDT&bar=1H&limit=3
// 抓到的真实响应（倒序：最新的在前），字段顺序、精度都照抄，不是手写的示例。
const realCandlesResponseJSON = `{"code":"0","msg":"","data":[` +
	`["1787184000000","69337.2","69599.4","69319.9","69514.5","111.39520843","7743007.753348444","7743007.753348444","0"],` +
	`["1787180400000","69264.7","69462.5","69160.6","69337.1","333.0511483","23087731.217311397","23087731.217311397","1"],` +
	`["1787176800000","69718.5","69846.6","69000","69264.5","509.18666667","35301481.318071814","35301481.318071814","1"]]}`

// realErrorResponseJSON 是请求一个不存在的 instId 时抓到的真实响应。
const realErrorResponseJSON = `{"code":"51001","msg":"Instrument ID, Instrument ID code, or Spread ID doesn't exist.","data":[]}`

func fakeRESTServer(t *testing.T, body string, statusCode int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v5/market/candles") {
			t.Errorf("请求路径 = %s，期望 /api/v5/market/candles 前缀", r.URL.Path)
		}
		w.WriteHeader(statusCode)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchCandlesParsesRealResponseAndReverses(t *testing.T) {
	srv := fakeRESTServer(t, realCandlesResponseJSON, http.StatusOK)
	c := NewClient(WithRESTBaseURL(srv.URL))

	candles, err := c.FetchCandles(context.Background(), "BTCUSDT", types.TF1h, 3)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if len(candles) != 3 {
		t.Fatalf("candles 数量 = %d，期望 3", len(candles))
	}
	// OKX 原始响应是倒序（最新在前），FetchCandles 必须翻转成升序。
	if !candles[0].OpenTime.Before(candles[1].OpenTime) || !candles[1].OpenTime.Before(candles[2].OpenTime) {
		t.Errorf("candles 应按时间升序排列，实际：%v, %v, %v",
			candles[0].OpenTime, candles[1].OpenTime, candles[2].OpenTime)
	}
	if !candles[2].Close.Equal(dec("69514.5")) {
		t.Errorf("最后一根收盘价 = %s，期望 69514.5（对应原始响应里最新的那一根）", candles[2].Close)
	}
}

func TestFetchCandlesRejectsErrorResponse(t *testing.T) {
	srv := fakeRESTServer(t, realErrorResponseJSON, http.StatusOK)
	c := NewClient(WithRESTBaseURL(srv.URL))

	_, err := c.FetchCandles(context.Background(), "BTCUSDT", types.TF1h, 3)
	if err == nil {
		t.Fatal("OKX 返回错误 code 时应报错")
	}
	if !strings.Contains(err.Error(), "51001") {
		t.Errorf("错误信息应带上 OKX 的错误码，实际：%v", err)
	}
}

func TestFetchCandlesRejectsUnknownSymbol(t *testing.T) {
	c := NewClient()
	if _, err := c.FetchCandles(context.Background(), "NOTASYMBOL", types.TF1h, 3); err == nil {
		t.Fatal("无法识别计价货币的标的应在发请求前就被拒绝")
	}
}

func TestFetchCandlesClampsLimitToMax(t *testing.T) {
	var gotLimit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLimit = r.URL.Query().Get("limit")
		w.Write([]byte(`{"code":"0","msg":"","data":[]}`))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	if _, err := c.FetchCandles(context.Background(), "BTCUSDT", types.TF1h, 5000); err != nil {
		t.Fatal(err)
	}
	if gotLimit != "300" {
		t.Errorf("limit 参数 = %s，期望被裁剪到 300（OKX 单次请求上限）", gotLimit)
	}
}

func TestFetchCandlesBeforeSendsOKXAfterParam(t *testing.T) {
	var gotAfter string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAfter = r.URL.Query().Get("after")
		w.Write([]byte(realCandlesResponseJSON))
	}))
	t.Cleanup(srv.Close)

	c := NewClient(WithRESTBaseURL(srv.URL))
	before := time.UnixMilli(1787176800000)
	candles, err := c.FetchCandlesBefore(context.Background(), "BTCUSDT", types.TF1h, 3, before)
	if err != nil {
		t.Fatal(err)
	}
	if gotAfter != "1787176800000" {
		t.Errorf("OKX after 参数 = %s，期望等于传入的 before 毫秒时间戳 1787176800000", gotAfter)
	}
	if len(candles) != 3 {
		t.Fatalf("candles 数量 = %d，期望 3（跟普通 FetchCandles 一样翻转成升序）", len(candles))
	}
}

func TestFetchCandlesBeforeRejectsZeroTime(t *testing.T) {
	c := NewClient()
	if _, err := c.FetchCandlesBefore(context.Background(), "BTCUSDT", types.TF1h, 3, time.Time{}); err == nil {
		t.Fatal("before 为零值时应该报错，而不是悄悄发一个没有 after 参数的普通请求")
	}
}
