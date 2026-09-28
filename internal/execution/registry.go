package execution

import "fmt"

// BrokerKind 标识执行层可以对接的下单通道。
//
// 新增一个交易所只需要在 brokerSpecs 里登记一行、实现对应的 Broker——调用方
// （cmd/executor、设置页面）都只依赖这个注册表，不需要各自维护一份交易所列表。
type BrokerKind string

const (
	// BrokerKindPaper 是纯内存模拟盘，不接任何交易所，不需要任何凭据。
	BrokerKindPaper BrokerKind = "paper"
	// BrokerKindBinanceTestnet 是币安测试网。
	BrokerKindBinanceTestnet BrokerKind = "binance-testnet"
	// BrokerKindOKXDemo 是 OKX 模拟盘。
	BrokerKindOKXDemo BrokerKind = "okx-demo"
	// BrokerKindBybitTestnet 是 Bybit 测试网。
	BrokerKindBybitTestnet BrokerKind = "bybit-testnet"
	// BrokerKindBitgetDemo 是 Bitget 模拟盘。
	BrokerKindBitgetDemo BrokerKind = "bitget-demo"
)

// Kinds 列出全部下单通道（含 paper），顺序即命令行帮助文本的展示顺序。
var Kinds = []BrokerKind{
	BrokerKindPaper, BrokerKindBinanceTestnet, BrokerKindOKXDemo,
	BrokerKindBybitTestnet, BrokerKindBitgetDemo,
}

// CredentialedKinds 列出需要交易所凭据的下单通道（不含 paper）——设置页面新增
// 交易所配置时的下拉框只该列出这些，paper 没有 key/secret 可填。
var CredentialedKinds = []BrokerKind{
	BrokerKindBinanceTestnet, BrokerKindOKXDemo, BrokerKindBybitTestnet, BrokerKindBitgetDemo,
}

type brokerSpec struct {
	label              string
	requiresPassphrase bool
}

var brokerSpecs = map[BrokerKind]brokerSpec{
	BrokerKindPaper:          {label: "纯内存模拟盘（不接任何交易所）"},
	BrokerKindBinanceTestnet: {label: "Binance 测试网"},
	BrokerKindOKXDemo:        {label: "OKX 模拟盘", requiresPassphrase: true},
	BrokerKindBybitTestnet:   {label: "Bybit 测试网"},
	BrokerKindBitgetDemo:     {label: "Bitget 模拟盘", requiresPassphrase: true},
}

// Valid 报告是否为已支持的下单通道。
func (k BrokerKind) Valid() bool {
	_, ok := brokerSpecs[k]
	return ok
}

// Label 返回人类可读名称，供界面展示。
func (k BrokerKind) Label() string {
	if s, ok := brokerSpecs[k]; ok {
		return s.label
	}
	return string(k)
}

// RequiresPassphrase 报告该下单通道除 API key/secret 外是否还需要密码短语
// （目前只有 OKX：它的鉴权是 key + secret + passphrase 三件套，币安只要两件）。
func (k BrokerKind) RequiresPassphrase() bool {
	return brokerSpecs[k].requiresPassphrase
}

// NewBroker 按 kind 构造对应的下单通道。paper 不需要任何凭据；其余通道
// 缺凭据时报错（具体规则见各自的 New*Broker 构造函数）。
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
		return nil, fmt.Errorf("不支持的下单通道 %q", kind)
	}
}
