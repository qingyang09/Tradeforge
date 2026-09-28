package execution

import (
	"tradeforge/internal/i18n"
	"tradeforge/pkg/types"
)

// BrokerKind identifies an order channel the execution layer can connect to.
//
// Adding a new exchange only requires registering one entry in brokerSpecs
// and implementing the corresponding Broker — callers (cmd/executor, the
// settings page) depend only on this registry, so nobody needs to maintain
// their own separate list of exchanges.
type BrokerKind string

const (
	// BrokerKindPaper is a pure in-memory paper channel: no exchange
	// connection, no credentials needed.
	BrokerKindPaper BrokerKind = "paper"
	// BrokerKindBinanceTestnet is the Binance testnet.
	BrokerKindBinanceTestnet BrokerKind = "binance-testnet"
	// BrokerKindOKXDemo is OKX demo trading.
	BrokerKindOKXDemo BrokerKind = "okx-demo"
	// BrokerKindBybitTestnet is the Bybit testnet.
	BrokerKindBybitTestnet BrokerKind = "bybit-testnet"
	// BrokerKindBitgetDemo is Bitget demo trading.
	BrokerKindBitgetDemo BrokerKind = "bitget-demo"
)

// Kinds lists every order channel (including paper); the order is the order
// shown in CLI help text.
var Kinds = []BrokerKind{
	BrokerKindPaper, BrokerKindBinanceTestnet, BrokerKindOKXDemo,
	BrokerKindBybitTestnet, BrokerKindBitgetDemo,
}

// CredentialedKinds lists the order channels that need exchange credentials
// (excludes paper) — the dropdown for adding an exchange config on the
// settings page should only list these; paper has no key/secret to fill in.
var CredentialedKinds = []BrokerKind{
	BrokerKindBinanceTestnet, BrokerKindOKXDemo, BrokerKindBybitTestnet, BrokerKindBitgetDemo,
}

type brokerSpec struct {
	// labelKey is the internal/i18n catalog key for this channel's display
	// name (see catalog_execution.go).
	labelKey           string
	requiresPassphrase bool
}

var brokerSpecs = map[BrokerKind]brokerSpec{
	BrokerKindPaper:          {labelKey: "execution.broker.paper"},
	BrokerKindBinanceTestnet: {labelKey: "execution.broker.binance_testnet"},
	BrokerKindOKXDemo:        {labelKey: "execution.broker.okx_demo", requiresPassphrase: true},
	BrokerKindBybitTestnet:   {labelKey: "execution.broker.bybit_testnet"},
	BrokerKindBitgetDemo:     {labelKey: "execution.broker.bitget_demo", requiresPassphrase: true},
}

// BrokerConfigError means a broker channel couldn't be constructed from the
// credentials/config supplied (a missing key, a non-testnet address, an
// unsupported kind). This is the error a user sees immediately after saving
// exchange credentials on the settings page, so -- like the rest of this
// session's Class A conversions -- it carries a translatable Reason instead
// of a pre-rendered Chinese sentence. Error() renders it in English by
// default, for logs and Go error-handling code; webui should read Reason
// directly for a real bilingual render.
type BrokerConfigError struct {
	Reason types.Message
}

func (e *BrokerConfigError) Error() string {
	return i18n.Render(i18n.LangEN, e.Reason)
}

// Valid reports whether this is a supported order channel.
func (k BrokerKind) Valid() bool {
	_, ok := brokerSpecs[k]
	return ok
}

// Label returns the human-readable name, for display in the UI.
func (k BrokerKind) Label() types.Message {
	if s, ok := brokerSpecs[k]; ok {
		return types.Msg(s.labelKey)
	}
	return types.Message{Literal: string(k)}
}

// RequiresPassphrase reports whether this order channel needs a passphrase in
// addition to the API key/secret (currently only OKX: its auth is the
// key+secret+passphrase trio, while Binance only needs the first two).
func (k BrokerKind) RequiresPassphrase() bool {
	return brokerSpecs[k].requiresPassphrase
}

// NewBroker constructs the order channel for the given kind. paper needs no
// credentials at all; the other channels error when credentials are missing
// (see the specific rules in each New*Broker constructor).
func NewBroker(kind BrokerKind, apiKey, apiSecret, passphrase string) (Broker, error) {
	switch kind {
	case BrokerKindPaper:
		return NewPaperBroker(), nil
	case BrokerKindBinanceTestnet:
		return NewBinanceTestnetBroker(BinanceConfig{APIKey: apiKey, APISecret: apiSecret})
	case BrokerKindOKXDemo:
		return NewOKXDemoBroker(OKXConfig{APIKey: apiKey, APISecret: apiSecret, Passphrase: passphrase})
	case BrokerKindBybitTestnet:
		return NewBybitTestnetBroker(BybitConfig{APIKey: apiKey, APISecret: apiSecret})
	case BrokerKindBitgetDemo:
		return NewBitgetDemoBroker(BitgetConfig{APIKey: apiKey, APISecret: apiSecret, Passphrase: passphrase})
	default:
		return nil, &BrokerConfigError{types.Msg("execution.broker.unsupported_kind", "kind", string(kind))}
	}
}
