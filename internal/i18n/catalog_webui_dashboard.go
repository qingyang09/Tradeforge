package i18n

// Catalog entries for internal/webui/templates/dashboard.html and the
// banner/reason text handlers_dashboard.go builds for it. Class A, same as
// catalog_webui_layout.go.
func init() {
	register(LangEN, map[string]string{
		"webui.dashboard.title":               "Strategy Status Dashboard",
		"webui.dashboard.subtitle":            "Every strategy, grouped by state-machine stage. Click through from here to see a strategy's detail.",
		"webui.dashboard.empty_pre":           "No strategies yet. ",
		"webui.dashboard.empty_link":          "Create one in a sentence",
		"webui.dashboard.empty_post":          ".",
		"webui.dashboard.group_count":         "({count})",
		"webui.dashboard.select_all_title":    "Select all / deselect all",
		"webui.dashboard.col_name":            "Name",
		"webui.dashboard.col_symbol":          "Symbol",
		"webui.dashboard.col_timeframe":       "Timeframe",
		"webui.dashboard.col_combine":         "Combination",
		"webui.dashboard.col_updated":         "Updated",
		"webui.dashboard.detail_link":         "Detail",
		"webui.dashboard.delete_selected":     "Delete Selected",
		"webui.dashboard.none":                "None.",
		"webui.dashboard.confirm_bulk_delete": "Permanently delete the selected strategies? This cannot be undone and also deletes their associated backtest/order/decision records.",

		"webui.dashboard.bulk_delete_none_selected": "No strategies were selected.",
		"webui.dashboard.bulk_delete_success":       "Deleted {deleted} strategies.",
		"webui.dashboard.bulk_delete_partial":       "Deleted {deleted} strategies, skipped {skipped} ({reason}{etc}).",
		"webui.dashboard.bulk_delete_etc_suffix":    " and more",
		"webui.dashboard.skip_reason_not_found":     "strategy {id} does not exist or was already deleted",
		"webui.dashboard.skip_reason_not_draft":     "\"{name}\" is currently {state}, not DRAFT, skipped",
	})
	register(LangZH, map[string]string{
		"webui.dashboard.title":               "策略状态看板",
		"webui.dashboard.subtitle":            "按状态机阶段分组展示全部策略。策略从这里点进去看详情。",
		"webui.dashboard.empty_pre":           "还没有任何策略。",
		"webui.dashboard.empty_link":          "用一句话新建一个",
		"webui.dashboard.empty_post":          "。",
		"webui.dashboard.group_count":         "（{count}）",
		"webui.dashboard.select_all_title":    "全选/取消全选",
		"webui.dashboard.col_name":            "名称",
		"webui.dashboard.col_symbol":          "标的",
		"webui.dashboard.col_timeframe":       "周期",
		"webui.dashboard.col_combine":         "组合方式",
		"webui.dashboard.col_updated":         "更新时间",
		"webui.dashboard.detail_link":         "详情",
		"webui.dashboard.delete_selected":     "删除选中",
		"webui.dashboard.none":                "暂无。",
		"webui.dashboard.confirm_bulk_delete": "确定要永久删除选中的策略吗？此操作不可撤销，会同时删除关联的回测/订单/决策记录。",

		"webui.dashboard.bulk_delete_none_selected": "没有选中任何策略。",
		"webui.dashboard.bulk_delete_success":       "已删除 {deleted} 条策略。",
		"webui.dashboard.bulk_delete_partial":       "已删除 {deleted} 条策略，跳过 {skipped} 条（{reason}{etc}）。",
		"webui.dashboard.bulk_delete_etc_suffix":    "等",
		"webui.dashboard.skip_reason_not_found":     "策略 {id} 不存在或已被删除",
		"webui.dashboard.skip_reason_not_draft":     "《{name}》当前是 {state} 状态，不是 DRAFT，跳过",
	})
}
