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

// staticFuncs are the template funcs whose behavior never depends on the
// viewer's language. Registered once at parse time.
func staticFuncs() template.FuncMap {
	return template.FuncMap{
		"pct":       formatPercent,
		"signedPct": formatSignedPercent,
		"htmxSrc":   func() string { return htmxCDN },
		"lower":     strings.ToLower,
		"duration":  holdingDuration,
		"feeDrag":   formatFeeDragRatio,
		// msg/t are placeholders satisfying html/template's parse-time name
		// check (it requires every func a template calls to already be
		// registered, even though it doesn't call it until execution) --
		// parseTemplatesByLang below immediately overrides both, per
		// language, via Clone()+Funcs(). Neither placeholder is ever
		// actually invoked.
		"msg": func(types.Message) string { return "" },
		"t":   func(string, ...any) string { return "" },
	}
}

// langFuncs are the template funcs that render text through internal/i18n
// in a specific language -- msg for a types.Message value already built by
// Go code (Signal.Reason, Decision.Reason, ...), t for a bare symbolic key
// (+ named args) used directly from a template for static page chrome.
func langFuncs(lang i18n.Lang) template.FuncMap {
	return template.FuncMap{
		"msg": func(m types.Message) string { return i18n.Render(lang, m) },
		"t":   func(key string, args ...any) string { return i18n.T(lang, key, args...) },
	}
}

// parseTemplatesByLang parses the template set once, then produces one
// fully-bound *template.Template per supported language via Clone()+Funcs()
// -- cheaper than cloning per request, and simpler than threading a Lang
// field through every content data struct. Every template file is written
// once; {{msg ...}}/{{t ...}} calls resolve to whichever language's clone
// executes them.
func parseTemplatesByLang() (map[i18n.Lang]*template.Template, error) {
	base, err := template.New("root").Funcs(staticFuncs()).ParseFS(templatesFS,
		"templates/*.html", "templates/partials/*.html")
	if err != nil {
		return nil, err
	}
	out := make(map[i18n.Lang]*template.Template, 2)
	for _, lang := range []i18n.Lang{i18n.LangEN, i18n.LangZH} {
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("clone templates for %s: %w", lang, err)
		}
		out[lang] = clone.Funcs(langFuncs(lang))
	}
	return out, nil
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
	Lang      i18n.Lang
}

// tmplFor returns the fully-bound template set for lang, falling back to
// i18n.DefaultLang if lang is somehow not one of the languages parsed at
// startup (defensive only -- resolveLang already only ever returns a known
// i18n.Lang).
func (s *Server) tmplFor(lang i18n.Lang) *template.Template {
	if t, ok := s.tmplByLang[lang]; ok {
		return t
	}
	return s.tmplByLang[i18n.DefaultLang]
}

// renderPage 先渲染 contentName 对应的内容模板，再套进 layout 输出整页。
//
// 需要 r 是因为 layout 的导航栏要显示当前登录用户的邮箱（多用户 SaaS 改造新增）——
// 邮箱在登录时已经缓存进 session（见 auth.go 的 sessionData），这里直接从 context
// 里取，不需要额外查一次数据库；同样也是从 r 上的 tf_lang cookie 解析出这次请求
// 该用哪种语言渲染。
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, title, contentName string, data any) {
	lang := resolveLang(r)
	tmpl := s.tmplFor(lang)
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, contentName, data); err != nil {
		s.serverError(w, fmt.Errorf("渲染内容 %s 失败：%w", contentName, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pd := pageData{Title: title, Body: template.HTML(buf.String()), UserEmail: currentUserEmail(r), Lang: lang}
	if err := tmpl.ExecuteTemplate(w, "layout", pd); err != nil {
		s.logger.Error("渲染布局失败", "err", err)
	}
}

// renderFragment 直接渲染一个 htmx 片段，不套 layout。
func (s *Server) renderFragment(w http.ResponseWriter, r *http.Request, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmplFor(resolveLang(r)).ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("渲染片段失败", "name", name, "err", err)
	}
}

// renderStandalone 渲染一个完整独立的页面，不套用共享的 layout（没有统一导航栏/浅色
// 后台配色）。给需要完全自主控制视觉呈现的页面用——比如全屏沉浸式的 K 线图，
// 套进后台仪表盘那套 max-width 980px 的卡片布局里只会显得局促，不像在看真实盘口。
func (s *Server) renderStandalone(w http.ResponseWriter, r *http.Request, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmplFor(resolveLang(r)).ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("渲染独立页面失败", "name", name, "err", err)
	}
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	s.logger.Error("处理请求失败", "err", err)
	http.Error(w, "内部错误："+err.Error(), http.StatusInternalServerError)
}
