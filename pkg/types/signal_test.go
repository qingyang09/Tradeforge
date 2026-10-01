package types

import (
	"errors"
	"testing"
	"time"
)

// structuredErr is a test double implementing messageReasoner, mirroring
// the pattern real error types (ComplianceError, cvdorderflow.DataSourceError,
// etc.) use throughout this codebase.
type structuredErr struct{ msg Message }

func (e *structuredErr) Error() string   { return e.msg.RenderFallback() }
func (e *structuredErr) Reason() Message { return e.msg }

func TestDegradedSignalPrefersStructuredReason(t *testing.T) {
	want := MsgF("test.structured", "a structured reason")
	s := DegradedSignal("mod", "BTCUSDT", &structuredErr{msg: want}, time.Now())
	if !s.Degraded {
		t.Fatal("expected Degraded to be true")
	}
	if s.Err.Key != want.Key {
		t.Errorf("Err.Key = %q, want %q (structured reason should be used verbatim, not flattened into Literal)", s.Err.Key, want.Key)
	}
}

func TestDegradedSignalFallsBackToLiteralForPlainErrors(t *testing.T) {
	s := DegradedSignal("mod", "BTCUSDT", errors.New("boom"), time.Now())
	if !s.Degraded {
		t.Fatal("expected Degraded to be true")
	}
	if s.Err.Literal != "boom" {
		t.Errorf("Err.Literal = %q, want %q", s.Err.Literal, "boom")
	}
	if s.Err.Key != "" {
		t.Errorf("a plain error should produce a Literal-only Message, got Key = %q", s.Err.Key)
	}
}

func TestDegradedSignalNoErrorLeavesErrZero(t *testing.T) {
	s := DegradedSignal("mod", "BTCUSDT", nil, time.Now())
	if !s.Err.IsZero() {
		t.Errorf("Err should be zero when no error is given, got %+v", s.Err)
	}
}
