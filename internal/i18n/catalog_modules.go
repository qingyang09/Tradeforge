package i18n

// Catalog entries for internal/modules/*'s cross-field ParamError checks
// (the kind that can't be expressed as a simple per-parameter min/max, so
// they're raised directly by a module's Evaluate rather than by
// pkg/types.ParamSpec.Coerce).
func init() {
	register(LangEN, map[string]string{
		"modules.macdrsi.slow_period_too_small": "must be greater than fast_period ({fast_period})",
		"modules.macdrsi.oversold_too_large":    "must be less than rsi_overbought ({rsi_overbought})",
	})
	register(LangZH, map[string]string{
		"modules.macdrsi.slow_period_too_small": "必须大于 fast_period（{fast_period}）",
		"modules.macdrsi.oversold_too_large":    "必须小于 rsi_overbought（{rsi_overbought}）",
	})
}
