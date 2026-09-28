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
		t.Fatalf("expected success using defaults, got error: %v", err)
	}
	if v := MustInt(got, "lookback"); v != 100 {
		t.Errorf("lookback = %d, expected 100", v)
	}
	if v := MustFloat(got, "tolerance"); v != 0.002 {
		t.Errorf("tolerance = %v, expected 0.002", v)
	}
	if v := MustString(got, "timeframe"); v != "1h" {
		t.Errorf("timeframe = %q, expected \"1h\"", v)
	}
	if MustBool(got, "strict") {
		t.Error("strict expected false")
	}
}

// After JSON decoding, integers become float64; normalization must restore them
// to int rather than leaving them as float64.
func TestResolveParamsCoercesJSONNumbers(t *testing.T) {
	var given map[string]any
	if err := json.Unmarshal([]byte(`{"lookback": 200, "tolerance": 0.01}`), &given); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveParams("m", testSpecs(), given)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := got["lookback"].(int); !ok {
		t.Fatalf("lookback type is %T, expected int", got["lookback"])
	}
	if MustInt(got, "lookback") != 200 {
		t.Errorf("lookback = %d, expected 200", MustInt(got, "lookback"))
	}
}

// A value like 20.7 must error out rather than being truncated to 20 — silent
// repair is explicitly forbidden on this platform.
func TestResolveParamsRejectsNonIntegerForIntParam(t *testing.T) {
	_, err := ResolveParams("m", testSpecs(), map[string]any{"lookback": 20.7})
	if err == nil {
		t.Fatal("expected non-integer int param to be rejected, but it passed")
	}
	var pe *ParamError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ParamError, got %T", err)
	}
	if pe.Param != "lookback" {
		t.Errorf("ParamError.Param = %q, expected \"lookback\"", pe.Param)
	}
}

func TestResolveParamsRangeChecks(t *testing.T) {
	cases := []struct {
		name  string
		given map[string]any
	}{
		{"below minimum", map[string]any{"lookback": 5}},
		{"above maximum", map[string]any{"lookback": 5000}},
		{"float out of range", map[string]any{"tolerance": 0.9}},
		{"value outside enum", map[string]any{"timeframe": "3s"}},
		{"type mismatch", map[string]any{"strict": "yes"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ResolveParams("m", testSpecs(), tc.given); err == nil {
				t.Fatalf("expected %v to be rejected, but it passed", tc.given)
			}
		})
	}
}

// An LLM-hallucinated parameter name must be surfaced explicitly, not silently ignored.
func TestResolveParamsRejectsUnknownParam(t *testing.T) {
	_, err := ResolveParams("support_resistance", testSpecs(), map[string]any{"magic_factor": 3})
	if err == nil {
		t.Fatal("expected unknown param to be rejected, but it passed")
	}
	var pe *ParamError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ParamError, got %T", err)
	}
	if pe.Param != "magic_factor" {
		t.Errorf("ParamError.Param = %q, expected \"magic_factor\"", pe.Param)
	}
	// The error must carry the list of allowed params so the Agent can relay it verbatim.
	if pe.Allowed == "" {
		t.Error("expected Allowed to list the module's valid parameter names")
	}
}

func TestResolveParamsRequiredMissing(t *testing.T) {
	specs := []ParamSpec{{Name: "symbol", Type: ParamString, Required: true}}
	if _, err := ResolveParams("m", specs, map[string]any{}); err == nil {
		t.Fatal("expected an error when a required parameter is missing")
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
		t.Errorf("serialized = %s, expected the string form of the duration", b)
	}

	var h holder
	if err := json.Unmarshal([]byte(`{"d":"90m"}`), &h); err != nil {
		t.Fatal(err)
	}
	if h.D.Std() != 90*time.Minute {
		t.Errorf("deserialized = %v, expected 90m", h.D)
	}
}

func TestDurationRejectsGarbage(t *testing.T) {
	var d Duration
	if err := json.Unmarshal([]byte(`"7 days"`), &d); err == nil {
		t.Fatal("expected an unparsable duration string to be rejected")
	}
}

func TestMarketDataTail(t *testing.T) {
	md := MarketData{Candles: make([]Candle, 10)}
	for i := range md.Candles {
		md.Candles[i].OpenTime = time.Unix(int64(i)*60, 0)
	}
	if got := len(md.Tail(3)); got != 3 {
		t.Errorf("len(Tail(3)) = %d, expected 3", got)
	}
	if got := len(md.Tail(50)); got != 10 {
		t.Errorf("len(Tail(50)) = %d, expected it to fall back to all 10 candles", got)
	}
	if got := md.Tail(0); got != nil {
		t.Errorf("Tail(0) = %v, expected nil", got)
	}
	if md.Tail(3)[0].OpenTime != time.Unix(7*60, 0) {
		t.Error("Tail should return the last n candles, not the first n")
	}
}

func TestDirectionOpposite(t *testing.T) {
	if DirectionLong.Opposite() != DirectionShort {
		t.Error("the opposite of LONG should be SHORT")
	}
	if DirectionNeutral.Opposite() != DirectionNeutral {
		t.Error("the opposite of NEUTRAL should still be NEUTRAL")
	}
}
