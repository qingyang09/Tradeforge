package i18n

// Catalog entry for internal/webui/render.go's generic 500 error body
// (serverError) -- the one piece of text every webui handler can produce
// through a single shared path, so it's resolved per request from the
// tf_lang cookie like everything else, even though in practice it's only
// ever seen when something has already gone wrong.
func init() {
	register(LangEN, map[string]string{
		"webui.render.internal_error": "Internal error: {error}",
	})
	register(LangZH, map[string]string{
		"webui.render.internal_error": "内部错误：{error}",
	})
}
