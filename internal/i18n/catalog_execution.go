package i18n

// Catalog entries for internal/execution's order-channel registry
// (BrokerKind.Label) and broker-construction errors (missing credentials,
// wrong network) -- these surface directly on the webui settings page when
// a user saves exchange credentials.
func init() {
	register(LangEN, map[string]string{
		"execution.broker.paper":                "In-memory paper trading (no exchange connection)",
		"execution.broker.binance_testnet":      "Binance testnet",
		"execution.broker.okx_demo":             "OKX demo trading",
		"execution.broker.bybit_testnet":        "Bybit testnet",
		"execution.broker.bitget_demo":          "Bitget demo trading",
		"execution.broker.unsupported_kind":     "Unsupported order channel {kind}",
		"execution.broker.not_testnet":          "BaseURL {base_url} is not a testnet address; this version only supports connecting to testnets",
		"execution.broker.binance.missing_keys": "Missing Binance testnet API credentials (TF_BINANCE_API_KEY / TF_BINANCE_API_SECRET)",
		"execution.broker.okx.missing_keys":     "Missing OKX demo trading API credentials (TF_OKX_API_KEY / TF_OKX_API_SECRET / TF_OKX_PASSPHRASE)",
		"execution.broker.bybit.missing_keys":   "Missing Bybit testnet API credentials (TF_BYBIT_API_KEY / TF_BYBIT_API_SECRET)",
		"execution.broker.bitget.missing_keys":  "Missing Bitget demo trading API credentials (TF_BITGET_API_KEY / TF_BITGET_API_SECRET / TF_BITGET_PASSPHRASE)",
	})
	register(LangZH, map[string]string{
		"execution.broker.paper":                "纯内存模拟盘（不接任何交易所）",
		"execution.broker.binance_testnet":      "Binance 测试网",
		"execution.broker.okx_demo":             "OKX 模拟盘",
		"execution.broker.bybit_testnet":        "Bybit 测试网",
		"execution.broker.bitget_demo":          "Bitget 模拟盘",
		"execution.broker.unsupported_kind":     "不支持的下单通道 {kind}",
		"execution.broker.not_testnet":          "BaseURL {base_url} 不是测试网地址；当前版本只允许对接测试网",
		"execution.broker.binance.missing_keys": "缺少币安测试网 API 密钥（TF_BINANCE_API_KEY / TF_BINANCE_API_SECRET）",
		"execution.broker.okx.missing_keys":     "缺少 OKX 模拟盘 API 密钥（TF_OKX_API_KEY / TF_OKX_API_SECRET / TF_OKX_PASSPHRASE）",
		"execution.broker.bybit.missing_keys":   "缺少 Bybit 测试网 API 密钥（TF_BYBIT_API_KEY / TF_BYBIT_API_SECRET）",
		"execution.broker.bitget.missing_keys":  "缺少 Bitget 模拟盘 API 密钥（TF_BITGET_API_KEY / TF_BITGET_API_SECRET / TF_BITGET_PASSPHRASE）",
	})
}
