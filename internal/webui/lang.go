package webui

import (
	"net/http"
	"time"

	"tradeforge/internal/i18n"
)

// langCookieName holds the viewer's chosen UI language. Independent of
// sessionCookieName/auth entirely -- it must work on /login and /signup,
// before any session exists, unlike tf_session.
const langCookieName = "tf_lang"

// langCookieTTL is long-lived (a year): a language choice is a standing
// preference, not something worth re-asking on every visit.
const langCookieTTL = 365 * 24 * time.Hour

// resolveLang reads the viewer's language preference from the tf_lang
// cookie. A missing or unrecognized cookie falls back to i18n.DefaultLang
// (Chinese) via ParseLang -- the existing user base is Chinese-speaking, so
// an absent preference must never silently default to English.
func resolveLang(r *http.Request) i18n.Lang {
	c, err := r.Cookie(langCookieName)
	if err != nil {
		return i18n.DefaultLang
	}
	return i18n.ParseLang(c.Value)
}

// handleSetLang returns a handler that sets the tf_lang cookie to lang and
// redirects back to the referring page (or / if there is none). GET
// /lang/en and GET /lang/zh both route here, with lang fixed per
// registration in Routes().
//
// This route sits outside requireAuth (it must work pre-login too, see
// Routes()'s doc comment), so there's no requireAuth-injected session in the
// request context to read via currentUserID -- when a session cookie is
// present and still valid, it's looked up directly against s.sessions
// instead, and the choice is persisted to that account via
// SetUserPreferredLang. An anonymous visitor (no session, or an invalid/
// expired one) just gets the cookie set, same as always. This is a
// best-effort, fire-and-forget persist: a failure here is logged and still
// lets the cookie-set/redirect proceed, since the viewer's own browser
// already has the choice applied either way.
func (s *Server) handleSetLang(lang i18n.Lang) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     langCookieName,
			Value:    string(lang),
			Path:     "/",
			Expires:  time.Now().Add(langCookieTTL),
			SameSite: http.SameSiteLaxMode,
		})
		if c, err := r.Cookie(sessionCookieName); err == nil {
			if data, ok := s.sessions.lookup(c.Value); ok {
				if err := s.store.SetUserPreferredLang(r.Context(), data.UserID, string(lang)); err != nil {
					s.logger.Warn("failed to persist language preference to account", "user_id", data.UserID, "err", err)
				}
			}
		}
		redirectTo := r.Referer()
		if redirectTo == "" {
			redirectTo = "/"
		}
		http.Redirect(w, r, redirectTo, http.StatusFound)
	}
}
