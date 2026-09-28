package webui

import (
	"encoding/base64"
	"testing"

	"tradeforge/internal/agent"
)

func TestEncodeDecodeStateRoundTrips(t *testing.T) {
	original := wizardState{
		History: []agent.Turn{{Role: "user", Text: "做多"}, {Role: "assistant", Text: "用哪个标的？"}},
		Proposal: &agent.Proposal{
			Outcome:         agent.OutcomeClarify,
			Restatement:     "我理解你想做多",
			Questions:       []string{"用哪个标的？"},
			SourceUtterance: "做多",
			Attempts:        1,
		},
	}

	encoded, err := encodeState(original)
	if err != nil {
		t.Fatalf("编码失败：%v", err)
	}

	decoded, err := decodeState(encoded)
	if err != nil {
		t.Fatalf("解码失败：%v", err)
	}
	if len(decoded.History) != 2 || decoded.History[0].Text != "做多" {
		t.Errorf("History 往返后不一致：%+v", decoded.History)
	}
	if decoded.Proposal == nil || decoded.Proposal.Restatement != original.Proposal.Restatement {
		t.Errorf("Proposal 往返后不一致：%+v", decoded.Proposal)
	}
	if decoded.Proposal.Attempts != 1 {
		t.Errorf("Attempts = %d，期望 1", decoded.Proposal.Attempts)
	}
}

func TestDecodeStateRejectsGarbage(t *testing.T) {
	if _, err := decodeState("不是合法的 base64 !!!"); err == nil {
		t.Error("非法输入应返回错误，而不是 panic 或静默返回零值")
	}
}

func TestDecodeStateRejectsValidBase64ButInvalidJSON(t *testing.T) {
	garbage := base64.URLEncoding.EncodeToString([]byte("not json"))
	if _, err := decodeState(garbage); err == nil {
		t.Error("非 JSON 内容应返回错误")
	}
}
