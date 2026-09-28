package types

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func testSpecs() []ParamSpec {
	return []ParamSpec{
		{Name: "lookback", Type: ParamInt, Default: 100, Min: F(20), Max: F(500)},
		{Name: "tolerance", Type: ParamFloat, Default: 0.002, Min: F(0.0001), Max: F(0.05)},
		{Name: "timeframe", Type: ParamString, Default: "1h", Enum: []string{"15m", "1h", "4h"}},
		{Name: "strict", Type: ParamBool, Default: false},
	}
}

func TestResolveParamsFillsDefaults(t *testing.T) {
	got, err := ResolveParams("m", testSpecs(), map[string]any{})
	if err != nil {
		t.Fatalf("期望使用默认值成功，得到错误：%v", err)
	}
	if v := MustInt(got, "lookback"); v != 100 {
		t.Errorf("lookback = %d，期望 100", v)
	}
	if v := MustFloat(got, "tolerance"); v != 0.002 {
		t.Errorf("tolerance = %v，期望 0.002", v)
	}
	if v := MustString(got, "timeframe"); v != "1h" {
		t.Errorf("timeframe = %q，期望 \"1h\"", v)
	}
	if MustBool(got, "strict") {
		t.Error("strict 期望 false")
	}
}

// JSON 解码后整数会变成 float64，规范化必须把它还原成 int 而不是留着 float64。
func TestResolveParamsCoercesJSONNumbers(t *testing.T) {
	var given map[string]any
	if err := json.Unmarshal([]byte(`{"lookback": 200, "tolerance": 0.01}`), &given); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveParams("m", testSpecs(), given)
	if err != nil {
		t.Fatalf("意外错误：%v", err)
	}
	if _, ok := got["lookback"].(int); !ok {
		t.Fatalf("lookback 类型为 %T，期望 int", got["lookback"])
	}
	if MustInt(got, "lookback") != 200 {
		t.Errorf("lookback = %d，期望 200", MustInt(got, "lookback"))
	}
}

// 20.7 这类值必须报错而不是被截断成 20——静默修复是平台明确禁止的行为。
func TestResolveParamsRejectsNonIntegerForIntParam(t *testing.T) {
	_, err := ResolveParams("m", testSpecs(), map[string]any{"lookback": 20.7})
	if err == nil {
		t.Fatal("期望拒绝非整数的 int 参数，实际通过了")
	}
	var pe *ParamError
	if !errors.As(err, &pe) {
		t.Fatalf("期望 *ParamError，得到 %T", err)
	}
	if pe.Param != "lookback" {
		t.Errorf("ParamError.Param = %q，期望 \"lookback\"", pe.Param)
	}
}

func TestResolveParamsRangeChecks(t *testing.T) {
	cases := []struct {
		name  string
		given map[string]any
	}{
		{"低于最小值", map[string]any{"lookback": 5}},
		{"高于最大值", map[string]any{"lookback": 5000}},
		{"浮点越界", map[string]any{"tolerance": 0.9}},
		{"枚举外取值", map[string]any{"timeframe": "3s"}},
		{"类型不匹配", map[string]any{"strict": "yes"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ResolveParams("m", testSpecs(), tc.given); err == nil {
				t.Fatalf("期望拒绝 %v，实际通过了", tc.given)
			}
		})
	}
}

// LLM 幻觉出的参数名必须显式暴露，不能被静默忽略。
func TestResolveParamsRejectsUnknownParam(t *testing.T) {
	_, err := ResolveParams("support_resistance", testSpecs(), map[string]any{"magic_factor": 3})
	if err == nil {
		t.Fatal("期望拒绝未知参数，实际通过了")
	}
	var pe *ParamError
	if !errors.As(err, &pe) {
		t.Fatalf("期望 *ParamError，得到 %T", err)
	}
	if pe.Param != "magic_factor" {
		t.Errorf("ParamError.Param = %q，期望 \"magic_factor\"", pe.Param)
	}
	// 错误信息要带上允许的参数列表，Agent 才能原样转述给用户。
	if pe.Allowed == "" {
		t.Error("期望 Allowed 列出该模块的合法参数名")
	}
}

func TestResolveParamsRequiredMissing(t *testing.T) {
	specs := []ParamSpec{{Name: "symbol", Type: ParamString, Required: true}}
	if _, err := ResolveParams("m", specs, map[string]any{}); err == nil {
		t.Fatal("期望缺少必填参数时报错")
	}
}

func TestDurationJSONRoundTrip(t *testing.T) {
	type holder struct {
		D Duration `json:"d"`
	}
	b, err := json.Marshal(holder{D: D(7 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"d":"168h0m0s"}` {
		t.Errorf("序列化结果 = %s，期望字符串形式的时长", b)
	}

	var h holder
	if err := json.Unmarshal([]byte(`{"d":"90m"}`), &h); err != nil {
		t.Fatal(err)
	}
	if h.D.Std() != 90*time.Minute {
		t.Errorf("反序列化 = %v，期望 90m", h.D)
	}
}

func TestDurationRejectsGarbage(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`"7 days"`), &d); err == nil {
		t.Fatal("期望拒绝无法解析的时长字符串")
	}
}

func TestMarketDataTail(t *testing.T) {
	md := MarketData{Candles: make([]Candle, 10)}
	for i := range md.Candles {
		md.Candles[i].OpenTime = time.Unix(int64(i)*60, 0)
	}
	if got := len(md.Tail(3)); got != 3 {
		t.Errorf("Tail(3) 长度 = %d，期望 3", got)
	}
	if got := len(md.Tail(50)); got != 10 {
		t.Errorf("Tail(50) 长度 = %d，期望回退为全部 10 根", got)
	}
	if got := md.Tail(0); got != nil {
		t.Errorf("Tail(0) = %v，期望 nil", got)
	}
	if md.Tail(3)[0].OpenTime != time.Unix(7*60, 0) {
		t.Error("Tail 应返回最后 n 根而不是最前 n 根")
	}
}

func TestDirectionOpposite(t *testing.T) {
	if DirectionLong.Opposite() != DirectionShort {
		t.Error("LONG 的相反应为 SHORT")
	}
	if DirectionNeutral.Opposite() != DirectionNeutral {
		t.Error("NEUTRAL 的相反应仍为 NEUTRAL")
	}
}
