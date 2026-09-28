package types

import (
	"encoding/json"
	"testing"
)

func TestMessageJSONRoundTripsStructuredForm(t *testing.T) {
	m := Msg("risk.daily_loss_exceeded", "symbol", "BTCUSDT", "limit", "500")
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var got Message
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Key != m.Key {
		t.Errorf("Key = %q, want %q", got.Key, m.Key)
	}
	if got.Args["symbol"] != "BTCUSDT" || got.Args["limit"] != "500" {
		t.Errorf("Args round-tripped wrong: %+v", got.Args)
	}
	if got.Literal != "" {
		t.Errorf("a structured message should never round-trip a Literal, got %q", got.Literal)
	}
}

// This is the backward-compatibility guarantee the whole design depends on:
// a row written before types.Message existed is just a bare JSON string in
// the database (e.g. `"reason": "价格突破阻力位"`), and it must still decode
// cleanly into a Message that renders as exactly that text, in any language.
func TestMessageUnmarshalsLegacyBareStringAsLiteral(t *testing.T) {
	var m Message
	if err := json.Unmarshal([]byte(`"价格突破阻力位 12345.67"`), &m); err != nil {
		t.Fatal(err)
	}
	if m.Literal != "价格突破阻力位 12345.67" {
		t.Errorf("Literal = %q", m.Literal)
	}
	if m.Key != "" {
		t.Errorf("a legacy string should never populate Key, got %q", m.Key)
	}
}

// A Literal-only Message re-marshals back out as the same bare string it was
// read in as -- re-saving untouched legacy data shouldn't silently upgrade
// its shape or lose the original text.
func TestMessageMarshalsLiteralBackToBareString(t *testing.T) {
	m := Message{Literal: "旧记录"}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"旧记录"` {
		t.Errorf("Marshal(Literal) = %s, want a bare JSON string", b)
	}
}

func TestMessageUnmarshalsStructuredObjectForm(t *testing.T) {
	var m Message
	raw := `{"key":"strategy.delete.only_draft","args":{"state":"BACKTESTED"}}`
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	if m.Key != "strategy.delete.only_draft" {
		t.Errorf("Key = %q", m.Key)
	}
	if m.Args["state"] != "BACKTESTED" {
		t.Errorf("Args = %+v", m.Args)
	}
}

func TestMessageEmbeddedInStructRoundTripsBothShapesInTheSameJSONBColumn(t *testing.T) {
	// Simulates the real scenario: a JSONB column (e.g. decisions.signals)
	// holding a mix of old rows (bare string reason) and new rows
	// (structured reason), unmarshaled into the same Go type either way.
	type row struct {
		Reason Message `json:"reason"`
	}
	var legacy row
	if err := json.Unmarshal([]byte(`{"reason":"CVD 失衡达到阈值"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Reason.Literal != "CVD 失衡达到阈值" {
		t.Errorf("legacy row Literal = %q", legacy.Reason.Literal)
	}

	var current row
	if err := json.Unmarshal([]byte(`{"reason":{"key":"modules.cvd.imbalance","args":{"ratio":"0.72"}}}`), &current); err != nil {
		t.Fatal(err)
	}
	if current.Reason.Key != "modules.cvd.imbalance" || current.Reason.Args["ratio"] != "0.72" {
		t.Errorf("current row = %+v", current.Reason)
	}
}

func TestRenderFallbackUsesEnglishFallbackTemplate(t *testing.T) {
	m := MsgF("params.above_max", "{value} is above the allowed maximum of {max}", "value", "5000", "max", "1000")
	if got := m.RenderFallback(); got != "5000 is above the allowed maximum of 1000" {
		t.Errorf("RenderFallback() = %q", got)
	}
}

func TestRenderFallbackReturnsRawKeyWhenNoFallbackSet(t *testing.T) {
	m := Msg("some.key")
	if got := m.RenderFallback(); got != "some.key" {
		t.Errorf("RenderFallback() = %q, want the raw key", got)
	}
}

func TestRenderFallbackReturnsLiteralUnchanged(t *testing.T) {
	m := Message{Literal: "旧记录"}
	if got := m.RenderFallback(); got != "旧记录" {
		t.Errorf("RenderFallback() on a literal = %q", got)
	}
}

func TestInterpolateLeavesUnmatchedPlaceholderVisible(t *testing.T) {
	got := Interpolate("value is {missing}", nil)
	if got != "value is {missing}" {
		t.Errorf("Interpolate = %q", got)
	}
}

func TestMessageIsZero(t *testing.T) {
	if !(Message{}).IsZero() {
		t.Error("empty Message should be zero")
	}
	if (Msg("some.key")).IsZero() {
		t.Error("a keyed Message should not be zero")
	}
	if (Message{Literal: "x"}).IsZero() {
		t.Error("a literal Message should not be zero")
	}
}
