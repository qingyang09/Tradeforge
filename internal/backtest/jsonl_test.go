package backtest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"tradeforge/pkg/types"
)

// python/backtest's loaders.py distinguishes meta/decision lines by the
// "type" field and reads fields one by one by name — this pins down the
// shape of each line to prevent field-name/structure drift.
func TestWriteJSONLShapeMatchesPythonLoader(t *testing.T) {
	meta := Meta{
		Type: "meta", EngineVersion: "signal-replay/1.0.0", StrategyID: "s1",
		Symbol: "BTCUSDT", Timeframe: "1h", Combine: "ALL", BarCount: 1,
		DataStart: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		DataEnd:   time.Date(2025, 1, 1, 1, 0, 0, 0, time.UTC),
	}
	decisions := []DecisionLine{{
		Type: "decision", Index: 0, BarTime: meta.DataEnd, Direction: "LONG",
		Score: 0.8, Triggered: true, Price: "100.5", Reason: types.Message{Literal: "test"},
		Signals: []types.Signal{{Module: "volume_breakout", Direction: types.DirectionLong}},
	}}

	var buf bytes.Buffer
	if err := WriteJSONL(&buf, meta, decisions); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2 (1 meta line + 1 decision line)", len(lines))
	}

	var metaRow map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &metaRow); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "engine_version", "strategy_id", "symbol", "timeframe", "combine", "bar_count", "data_start", "data_end"} {
		if _, ok := metaRow[key]; !ok {
			t.Errorf("meta line missing field %q: %v", key, metaRow)
		}
	}
	if metaRow["type"] != "meta" {
		t.Errorf(`meta line type = %v, want "meta"`, metaRow["type"])
	}

	var decisionRow map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &decisionRow); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "index", "bar_time", "direction", "score", "triggered", "price", "reason", "signals"} {
		if _, ok := decisionRow[key]; !ok {
			t.Errorf("decision line missing field %q: %v", key, decisionRow)
		}
	}
	if decisionRow["type"] != "decision" {
		t.Errorf(`decision line type = %v, want "decision"`, decisionRow["type"])
	}
	if _, ok := decisionRow["price"].(string); !ok {
		t.Errorf("price should be a string (for decimal precision), got type %T", decisionRow["price"])
	}
}
