package webui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	"tradeforge/internal/i18n"
	"tradeforge/pkg/types"
)

//go:embed templates
var templatesFS embed.FS

// htmxCDN 是 htmx 的公开 CDN 地址。项目一贯"能用标准库就不引第三方依赖"，
// 但这里没有本地依赖可言——不引入构建步骤/前端框架的唯一代价就是这一行 <script>。
const htmxCDN = "https://unpkg.com/htmx.org@2.0.4"

func parseTemplates() (*template.Template, error) {
	funcs := template.FuncMap{
		"pct":       formatPercent,
		"signedPct": formatSignedPercent,
		"htmxSrc":   func() string { return htmxCDN },
		"lower":     strings.ToLower,
		"duration":  holdingDuration,
		"feeDrag":   formatFeeDragRatio,
		// msg renders a types.Message (see pkg/types/i18n.go) -- a symbolic
		// key + args produced by a package that doesn't know the viewer's
		// language (internal/strategy's gate checks, at the moment).
		// Fixed to i18n.DefaultLang (Chinese) for now, matching this app's
		// current Chinese-only behavior exactly; Phase 4 of the bilingual
		// webui work replaces this with a per-request closure bound to the
		// viewer's actual chosen language via template.Clone()+Funcs(),
		// without needing to change any template that already calls {{msg ...}}.
		"msg": func(m types.Message) string { return i18n.Render(i18n.DefaultLang, m) },
	}
	return template.New("root").Funcs(funcs).ParseFS(templatesFS,
		"templates/*.html", "templates/partials/*.html")
}

func formatPercent(f float64) string       { return fmt.Sprintf("%.2f%%", f*100) }
func formatSignedPercent(f float64) string { return fmt.Sprintf("%+.2f%%", f*100) }

// formatFeeDragRatio 显示"手续费占毛利润的比例"。
//
// 毛利润非正（还没扣手续费就已经在亏钱，或者压根没有交易）时这个比例没有明确
// 含义，展示成"—"而不是硬凑一个误导性的数字——跟 types.PerformanceMetrics.
// FeeDragRatio 的 ok=false 是同一个判断，这里只是把它转成界面文案。
func formatFeeDragRatio(m types.PerformanceMetrics) string {
	ratio, ok := m.FeeDragRatio()
	if !ok {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", ratio*100)
}

// pageData 是套进 layout 的公共外壳：内容页先各自渲染成 HTML 片段，
// 再由 layout 统一包一层导航/样式，避免每个页面模板重复整套 <html> 骨架。
type pageData struct {
	Title     string
	Body      template.HTML
	UserEmail string
}

// renderPage 先渲染 contentName 对应的内容模板，再套进 layout 输出整页。
//
// 需要 r 是因为 layout 的导航栏要显示当前登录用户的邮箱（多用户 SaaS 改造新增）——
// 邮箱在登录时已经缓存进 session（见 auth.go 的 sessionData），这里直接从 context
// 里取，不需要额外查一次数据库。
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, title, contentName string, data any) {
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, contentName, data); err != nil {
		s.serverError(w, fmt.Errorf("渲染内容 %s 失败：%w", contentName, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pd := pageData{Title: title, Body: template.HTML(buf.String()), UserEmail: currentUserEmail(r)}
	if err := s.tmpl.ExecuteTemplate(w, "layout", pd); err != nil {
		s.logger.Error("渲染布局失败", "err", err)
	}
}

// renderFragment 直接渲染一个 htmx 片段，不套 layout。
func (s *Server) renderFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("渲染片段失败", "name", name, "err", err)
	}
}

// renderStandalone 渲染一个完整独立的页面，不套用共享的 layout（没有统一导航栏/浅色
// 后台配色）。给需要完全自主控制视觉呈现的页面用——比如全屏沉浸式的 K 线图，
// 套进后台仪表盘那套 max-width 980px 的卡片布局里只会显得局促，不像在看真实盘口。
func (s *Server) renderStandalone(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("渲染独立页面失败", "name", name, "err", err)
	}
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.logger.Error("处理请求失败", "err", err)
	http.Error(w, "内部错误："+err.Error(), http.StatusInternalServerError)
}
