package backtest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"tradeforge/pkg/types"
)

// python/backtest 的 loaders.py 靠 "type" 字段区分 meta/decision 行，
// 且逐个按名字取字段——这里锁死每一行的形状，防止字段名/结构漂移。
func TestWriteJSONLShapeMatchesPythonLoader(t *testing.T) {
	meta := Meta{
		Type: "meta", EngineVersion: "signal-replay/1.0.0", StrategyID: "s1",
		Symbol: "BTCUSDT", Timeframe: "1h", Combine: "ALL", BarCount: 1,
		DataStart: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		DataEnd:   time.Date(2025, 1, 1, 1, 0, 0, 0, time.UTC),
	}
	decisions := []DecisionLine{{
		Type: "decision", Index: 0, BarTime: meta.DataEnd, Direction: "LONG",
		Score: 0.8, Triggered: true, Price: "100.5", Reason: "test",
		Signals: []types.Signal{{Module: "volume_breakout", Direction: types.DirectionLong}},
	}}

	var buf bytes.Buffer
	if err := WriteJSONL(&buf, meta, decisions); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("行数 = %d，期望 2（1 行 meta + 1 行 decision）", len(lines))
	}

	var metaRow map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &metaRow); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "engine_version", "strategy_id", "symbol", "timeframe", "combine", "bar_count", "data_start", "data_end"} {
		if _, ok := metaRow[key]; !ok {
			t.Errorf("meta 行缺少字段 %q：%v", key, metaRow)
		}
	}
	if metaRow["type"] != "meta" {
		t.Errorf(`meta 行 type = %v，期望 "meta"`, metaRow["type"])
	}

	var decisionRow map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &decisionRow); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "index", "bar_time", "direction", "score", "triggered", "price", "reason", "signals"} {
		if _, ok := decisionRow[key]; !ok {
			t.Errorf("decision 行缺少字段 %q：%v", key, decisionRow)
		}
	}
	if decisionRow["type"] != "decision" {
		t.Errorf(`decision 行 type = %v，期望 "decision"`, decisionRow["type"])
	}
	if _, ok := decisionRow["price"].(string); !ok {
		t.Errorf("price 应该是字符串（decimal 精度），实际类型 %T", decisionRow["price"])
	}
}
