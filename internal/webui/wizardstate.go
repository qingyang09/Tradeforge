package webui

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"tradeforge/internal/agent"
)

// wizardState is the wizard's entire multi-turn conversation state, carried
// between two HTTP requests.
//
// The server keeps no session: agent.Proposal and agent.Turn are both
// already plain, JSON-serializable structs, so encoding them into a hidden
// form field and decoding them back on the next POST is much simpler than
// introducing session storage, and it also means a server restart never
// loses an in-progress wizard conversation.
//
// The hidden field is deliberately unsigned/unencrypted: this is a local,
// single-operator tool, and anyone who can POST to this server already has
// direct access to the database on the same machine; also, agent.Confirm
// re-runs strategy.Validate against the real module registry before every
// write, so tampering with this field can at most get validation rejected,
// never bypass the schema or compliance checks.
type wizardState struct {
	History  []agent.Turn    `json:"history,omitempty"`
	Proposal *agent.Proposal `json:"proposal,omitempty"`
	// ForceSymbol, when non-empty, means this conversation round is locked
	// to a specific symbol (a translation request originating from the
	// visual builder, see handleBuilderTranslate) -- the user is talking on
	// this symbol's builder page, so the rule should land on this symbol,
	// without relying on the LLM happening to guess correctly every single
	// round. renderProposal uses it to override the proposal's Symbol
	// before encoding the new state, and this value travels along with
	// History across clarification rounds, never expiring just because one
	// more question got asked. The text wizard (handleWizardTranslate)
	// never sets it, with behavior completely unchanged from before.
	ForceSymbol string `json:"force_symbol,omitempty"`
	// BatchSymbols, when non-empty, means this is a batch-scan confirmation
	// (see handlers_batch.go): the translated config template gets applied
	// separately to each of these symbols, each producing its own
	// independent DRAFT -- the same "state carries a symbol constraint
	// across requests" mechanism as ForceSymbol (which overrides to a
	// single symbol), just for a group of symbols, and the application
	// happens at the confirm-and-save step rather than the translation
	// step (during translation these symbols don't exist in Config yet;
	// Config.Symbol there is just a placeholder that gets discarded). The
	// scan results stay fixed for the entire multi-round clarification
	// conversation, never re-scanning just because one more question got
	// asked.
	BatchSymbols []string `json:"batch_symbols,omitempty"`
}

// encodeState encodes the state into text suitable for an <input type=hidden>.
func encodeState(ws wizardState) (string, error) {
	blob, err := json.Marshal(ws)
	if err != nil {
		return "", fmt.Errorf("failed to serialize wizard state: %w", err)
	}
	return base64.URLEncoding.EncodeToString(blob), nil
}

// decodeState is encodeState's inverse. An error it returns should be
// rendered as a factual notice like "session expired, please start over,"
// not a 500.
func decodeState(s string) (wizardState, error) {
	blob, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return wizardState{}, fmt.Errorf("wizard state is corrupted: %w", err)
	}
	var ws wizardState
	if err := json.Unmarshal(blob, &ws); err != nil {
		return wizardState{}, fmt.Errorf("wizard state is corrupted: %w", err)
	}
	return ws, nil
}
