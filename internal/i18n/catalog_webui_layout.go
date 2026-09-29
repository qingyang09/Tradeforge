package i18n

// Catalog entries for internal/webui/templates/layout.html -- the shared
// header/nav chrome wrapping the dashboard, wizard, settings, and strategy
// detail pages. Class A: resolved fresh per request from the tf_lang
// cookie (see internal/webui/lang.go), same as every other webui template.
func init() {
	register(LangEN, map[string]string{
		"webui.nav.dashboard":    "Dashboard",
		"webui.nav.new_strategy": "New Strategy",
		"webui.nav.batch_scan":   "Batch Scan",
		"webui.nav.builder":      "Chart / Builder",
		"webui.nav.settings":     "Settings",
		"webui.nav.logout":       "Log Out",
	})
	register(LangZH, map[string]string{
		"webui.nav.dashboard":    "状态看板",
		"webui.nav.new_strategy": "新建策略",
		"webui.nav.batch_scan":   "批量扫描",
		"webui.nav.builder":      "K 线图 / 建策",
		"webui.nav.settings":     "设置",
		"webui.nav.logout":       "退出登录",
	})
}
