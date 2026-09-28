package okx

import (
	"fmt"
	"strings"

	"tradeforge/pkg/types"
)

// knownQuoteCurrencies 按检查顺序列出常见计价货币后缀，用于从项目内部的无分隔符写法
// （如 "BTCUSDT"）里切出 OKX 要求的带连字符写法（"BTC-USDT"）。
//
// 顺序有讲究：USDT 必须排在 USD 前面，否则 "BTCUSDT" 会被 USD 先匹配、切成
// "BTCUSDT"->"BTCUS"+"DT"这种错误结果的反面例子——先检查更长的后缀才不会被短后缀抢先命中。
var knownQuoteCurrencies = []string{"USDT", "USDC", "BTC", "ETH", "USD"}

// ToInstID 把项目内部的标的写法转换成 OKX 的 instId 写法。
//
// 匹配不上已知计价货币时直接报错，不做模糊猜测——错误的切分会让请求打到一个
// 语义上完全不对的 instId 上，而且不一定会报错（比如凑巧撞上另一个真实存在的交易对），
// 这种"看似能跑但是错的"比明确拒绝更危险。
func ToInstID(symbol string) (string, error) {
	up := strings.ToUpper(symbol)
	for _, quote := range knownQuoteCurrencies {
		if strings.HasSuffix(up, quote) && len(up) > len(quote) {
			base := up[:len(up)-len(quote)]
			return base + "-" + quote, nil
		}
	}
	return "", fmt.Errorf("无法从标的 %q 推断 OKX instId：请使用形如 BTCUSDT 的写法"+
		"（已知计价货币：%s）", symbol, strings.Join(knownQuoteCurrencies, "/"))
}

// barByTimeframe 是 types.Timeframe 到 OKX bar 参数的映射。
//
// 大小写是 OKX 接口本身的要求（已用真实请求验证过：分钟用小写 m，小时/天用大写 H/D，
// 传 "1h" 会被直接拒绝），不是这里随意选的风格。
var barByTimeframe = map[types.Timeframe]string{
	types.TF1m:  "1m",
	types.TF5m:  "5m",
	types.TF15m: "15m",
	types.TF1h:  "1H",
	types.TF4h:  "4H",
	types.TF1d:  "1D",
}

// toBar 把 types.Timeframe 转换成 OKX 的 bar 参数。
func toBar(tf types.Timeframe) (string, error) {
	bar, ok := barByTimeframe[tf]
	if !ok {
		return "", fmt.Errorf("OKX 数据源不支持周期 %q", tf)
	}
	return bar, nil
}
