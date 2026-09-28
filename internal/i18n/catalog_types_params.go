package i18n

// Catalog entries for pkg/types/params.go's ParamError.Reason. pkg/types
// can't register these itself (it can't import internal/i18n without an
// import cycle -- see pkg/types/i18n.go's EnglishFallback doc comment), so
// each key is also given an EnglishFallback at its construction site as a
// safety net; these entries are what actually renders once registered,
// taking priority over that fallback.
func init() {
	register(LangEN, map[string]string{
		"types.param.expected_int_type":    "expected an integer, got {type}",
		"types.param.expected_int_value":   "expected an integer, got {value}",
		"types.param.expected_number_type": "expected a number, got {type}",
		"types.param.must_be_finite":       "value must be finite",
		"types.param.expected_string_type": "expected a string, got {type}",
		"types.param.not_in_enum":          "{value} is not in the allowed set of values",
		"types.param.expected_bool_type":   "expected a bool, got {type}",
		"types.param.unknown_type":         "unknown parameter type {type}",
		"types.param.below_minimum":        "{value} is below the allowed minimum of {min}",
		"types.param.above_maximum":        "{value} is above the allowed maximum of {max}",
		"types.param.unknown_param":        "this module has no such parameter",
		"types.param.required_missing":     "missing required parameter",
		"types.param.no_default":           "parameter not provided and has no default",
	})
	register(LangZH, map[string]string{
		"types.param.expected_int_type":    "应该是整数，实际是 {type}",
		"types.param.expected_int_value":   "应该是整数，实际是 {value}",
		"types.param.expected_number_type": "应该是数字，实际是 {type}",
		"types.param.must_be_finite":       "数值必须是有限数",
		"types.param.expected_string_type": "应该是字符串，实际是 {type}",
		"types.param.not_in_enum":          "{value} 不在允许的取值范围内",
		"types.param.expected_bool_type":   "应该是布尔值，实际是 {type}",
		"types.param.unknown_type":         "未知的参数类型 {type}",
		"types.param.below_minimum":        "{value} 低于允许的最小值 {min}",
		"types.param.above_maximum":        "{value} 超过允许的最大值 {max}",
		"types.param.unknown_param":        "该模块没有这个参数",
		"types.param.required_missing":     "缺少必填参数",
		"types.param.no_default":           "未提供该参数，且没有默认值",
	})
}
