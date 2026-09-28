// Package i18n renders tradeforge/pkg/types.Message values (a symbolic key
// plus named arguments) into human-readable text in the viewer's chosen
// language. It is the one place that knows what "en"/"zh" actually say for
// every translatable string in the platform -- everything else just builds a
// types.Message and lets this package turn it into a sentence at the moment
// it's actually shown to someone (a webui request, an outbound
// email/Telegram/webhook alert, an LLM prompt).
//
// Deliberately not text/template: named-placeholder substitution is a
// handful of lines, and this project avoids pulling in machinery it doesn't
// need (see internal/notify's stdlib-only email sender for the same
// judgment call).
package i18n

import (
	"fmt"
	"strings"

	"tradeforge/pkg/types"
)

// catalogs holds every registered key's text, per language. Populated by
// register calls in the package's other catalog_*.go files (one per
// producing package, e.g. catalog_strategy.go for internal/strategy's keys)
// via their own init() functions -- splitting the catalog this way means two
// people (or two batched translation passes) adding keys for different
// packages at the same time touch different files, not one giant map
// literal.
var catalogs = map[Lang]map[string]string{
	LangEN: {},
	LangZH: {},
}

// register adds a batch of key->text entries to lang's catalog. Panics on a
// duplicate key within the same language, since that's always a bug (two
// packages accidentally picked the same catalog key, or the same package
// registered a key twice) -- better to fail loudly at process startup than
// silently let the second registration win.
func register(lang Lang, entries map[string]string) {
	m := catalogs[lang]
	for k, v := range entries {
		if _, exists := m[k]; exists {
			panic(fmt.Sprintf("i18n: duplicate key %q registered for language %q", k, lang))
		}
		m[k] = v
	}
}

// Lang identifies which language to render a Message in.
type Lang string

const (
	LangEN Lang = "en"
	LangZH Lang = "zh"

	// DefaultLang is Chinese, not English: every existing user of this
	// platform is a Chinese speaker, and historical data was all written in
	// Chinese. An unset or unrecognized language preference must never
	// silently switch an existing user's view to English.
	DefaultLang Lang = LangZH
)

// Valid reports whether l is a language this package can actually render.
func (l Lang) Valid() bool { return l == LangEN || l == LangZH }

// ParseLang interprets a raw string (a cookie value, a query param, a stored
// user preference) as a Lang, falling back to DefaultLang for anything empty
// or unrecognized rather than erroring -- a malformed or missing language
// preference should degrade gracefully, not break the page.
func ParseLang(raw string) Lang {
	l := Lang(strings.ToLower(strings.TrimSpace(raw)))
	if l.Valid() {
		return l
	}
	return DefaultLang
}

// Render turns a types.Message into text in the given language.
//
//   - A Literal message (decoded from legacy data written before
//     types.Message existed, see types.Message's doc comment) renders as
//     exactly that literal, in every language -- there's no key to look up
//     and no honest way to translate it after the fact.
//   - A keyed message looks up catalog[lang][m.Key] and substitutes each
//     "{name}" placeholder with m.Args["name"]. A key missing from lang's
//     catalog falls back to the Chinese catalog (the platform's original,
//     always-populated language), and finally to the raw key itself if it's
//     missing from both -- a page showing one untranslated key is a bug to
//     fix, not a reason to fail the whole render.
func Render(lang Lang, m types.Message) string {
	if m.Key == "" {
		return m.Literal
	}
	tmpl, ok := catalogs[lang][m.Key]
	if !ok {
		tmpl, ok = catalogs[LangZH][m.Key]
	}
	if !ok {
		return m.Key
	}
	return interpolate(tmpl, m.Args)
}

// T is a convenience wrapper for call sites that want to build and render a
// message in one step, without needing a standalone types.Message value --
// mainly webui handlers producing a one-off banner/error string. Prefer
// types.Msg(...) instead when the message needs to be stored or passed
// around before it's rendered (that's the whole point of Class B messages).
func T(lang Lang, key string, args ...any) string {
	return Render(lang, types.Msg(key, args...))
}

// interpolate replaces every "{name}" placeholder in tmpl with
// fmt.Sprint(args["name"]). A placeholder with no matching arg is left
// as-is (visibly wrong, easy to spot and fix, rather than silently dropped).
func interpolate(tmpl string, args map[string]any) string {
	if len(args) == 0 || !strings.Contains(tmpl, "{") {
		return tmpl
	}
	var b strings.Builder
	b.Grow(len(tmpl))
	for i := 0; i < len(tmpl); {
		open := strings.IndexByte(tmpl[i:], '{')
		if open == -1 {
			b.WriteString(tmpl[i:])
			break
		}
		open += i
		b.WriteString(tmpl[i:open])
		close := strings.IndexByte(tmpl[open:], '}')
		if close == -1 {
			b.WriteString(tmpl[open:])
			break
		}
		close += open
		name := tmpl[open+1 : close]
		if v, ok := args[name]; ok {
			fmt.Fprint(&b, v)
		} else {
			b.WriteString(tmpl[open : close+1])
		}
		i = close + 1
	}
	return b.String()
}
