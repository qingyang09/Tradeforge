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

// htmxCDN is htmx's public CDN address. This project consistently follows
// "no third-party dependency if the standard library will do," but there's
// no local dependency option here -- avoiding a build step/frontend
// framework costs nothing more than this one <script> line.
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

// formatFeeDragRatio displays "fees as a share of gross profit."
//
// When gross profit isn't positive (already losing money before fees, or
// there were no trades at all), this ratio has no well-defined meaning, so
// it's shown as "—" rather than forcing a misleading number -- the same
// judgment as types.PerformanceMetrics.FeeDragRatio's ok=false; this just
// turns that into page text.
func formatFeeDragRatio(m types.PerformanceMetrics) string {
	ratio, ok := m.FeeDragRatio()
	if !ok {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", ratio*100)
}

// pageData is the common shell wrapped in layout: a content page first
// renders itself into an HTML fragment, then layout wraps it once in a
// shared nav/style layer, so every page template doesn't have to repeat
// the whole <html> skeleton.
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

// renderPage first renders the content template named contentName, then
// wraps it in layout to output the whole page.
//
// r is needed because layout's nav bar shows the current logged-in user's
// email (added for the multi-tenant SaaS rework) -- the email is already
// cached in the session at login time (see auth.go's sessionData), read
// straight from the context here with no extra database query; r is also
// where this request's tf_lang cookie gets resolved to decide which
// language to render in.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, title, contentName string, data any) {
	lang := resolveLang(r)
	tmpl := s.tmplFor(lang)
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, contentName, data); err != nil {
		s.serverError(w, r, fmt.Errorf("failed to render content %s: %w", contentName, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pd := pageData{Title: title, Body: template.HTML(buf.String()), UserEmail: currentUserEmail(r), Lang: lang}
	if err := tmpl.ExecuteTemplate(w, "layout", pd); err != nil {
		s.logger.Error("failed to render layout", "err", err)
	}
}

// renderFragment renders an htmx fragment directly, without wrapping it in layout.
func (s *Server) renderFragment(w http.ResponseWriter, r *http.Request, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmplFor(resolveLang(r)).ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("failed to render fragment", "name", name, "err", err)
	}
}

// renderStandalone renders a fully independent page, not wrapped in the
// shared layout (no unified nav bar/light admin-panel colors). For pages
// that need full control over their own visual presentation -- a
// full-screen immersive candle chart, say, would feel cramped squeezed into
// the backend dashboard's max-width-980px card layout, not like looking at
// a real trading terminal.
func (s *Server) renderStandalone(w http.ResponseWriter, r *http.Request, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmplFor(resolveLang(r)).ExecuteTemplate(w, name, data); err != nil {
		s.logger.Error("failed to render standalone page", "name", name, "err", err)
	}
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("request handling failed", "err", err)
	http.Error(w, i18n.T(resolveLang(r), "webui.render.internal_error", "error", err.Error()), http.StatusInternalServerError)
}
