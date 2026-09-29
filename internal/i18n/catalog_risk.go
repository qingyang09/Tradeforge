package i18n

// Catalog entries for internal/execution's risk-control layer
// (RiskDecision.Reason) and the order-provenance annotation attached to
// every order placed by a Worker (OrderProvenance.Note). Both are Class B:
// computed once by a headless Worker goroutine with no concept of the
// eventual viewer's language, then persisted (risk_events.detail /
// orders.provenance JSONB) for display later.
func init() {
	register(LangEN, map[string]string{
		"execution.risk.halted":                     "Symbol {symbol} has been suspended by risk control (rule {rule}); no new positions",
		"execution.risk.max_position_size":          "Requested position notional {notional} exceeds the per-trade cap {limit}",
		"execution.risk.max_daily_loss_open":        "Today's realized loss {loss} has reached the cap {limit}; no new positions today",
		"execution.risk.max_daily_loss_force_close": "Today's loss (realized {realized} + unrealized {unrealized}) has reached the cap {limit}; forcing a close and suspending this symbol",
		"execution.risk.stop_loss_triggered":        "Stop-loss triggered: entry {entry}, current {price}, stop-loss price {stop_loss_price}",
		"execution.risk.take_profit_triggered":      "Take-profit triggered: entry {entry}, current {price}, take-profit price {take_profit_price}",
		"execution.risk.max_holding_triggered":      "Holding period has reached {held}, exceeding the cap {limit}; forcing a close",
		"execution.provenance.open":                 "Open position",
		"execution.provenance.close.signal":         "Close position: opposing signal",
		"execution.provenance.close.stop_loss":      "Close position: stop-loss",
		"execution.provenance.close.take_profit":    "Close position: take-profit",
		"execution.provenance.close.max_daily_loss": "Close position: daily loss cap reached",
		"execution.provenance.close.max_holding":    "Close position: max holding period reached",
		"execution.provenance.close.other":          "Close position: {rule}",
	})
	register(LangZH, map[string]string{
		"execution.risk.halted":                     "标的 {symbol} 已被风控暂停（规则 {rule}），不再开新仓",
		"execution.risk.max_position_size":          "拟开仓名义金额 {notional} 超过单笔上限 {limit}",
		"execution.risk.max_daily_loss_open":        "当日已实现亏损 {loss} 达到上限 {limit}，当日不再开新仓",
		"execution.risk.max_daily_loss_force_close": "当日亏损（已实现 {realized} + 浮亏 {unrealized}）达到上限 {limit}，强制平仓并暂停该标的",
		"execution.risk.stop_loss_triggered":        "触发止损：入场 {entry}，当前 {price}，止损价 {stop_loss_price}",
		"execution.risk.take_profit_triggered":      "触发止盈：入场 {entry}，当前 {price}，止盈价 {take_profit_price}",
		"execution.risk.max_holding_triggered":      "持仓已达 {held}，超过上限 {limit}，强制平仓",
		"execution.provenance.open":                 "开仓",
		"execution.provenance.close.signal":         "平仓：反向信号",
		"execution.provenance.close.stop_loss":      "平仓：止损",
		"execution.provenance.close.take_profit":    "平仓：止盈",
		"execution.provenance.close.max_daily_loss": "平仓：当日亏损达到上限",
		"execution.provenance.close.max_holding":    "平仓：持仓超过上限",
		"execution.provenance.close.other":          "平仓：{rule}",
	})
}
