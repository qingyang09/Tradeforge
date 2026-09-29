package i18n

// Catalog entry for pkg/types/signal.go's DegradedSignal.
func init() {
	register(LangEN, map[string]string{
		"types.signal.degraded": "module produced no signal; degraded to neutral",
	})
	register(LangZH, map[string]string{
		"types.signal.degraded": "模块未产生信号；降级为中性",
	})
}
