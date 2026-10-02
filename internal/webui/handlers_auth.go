package webui

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"tradeforge/internal/i18n"
	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

type loginData struct {
	Error types.Message
	Lang  i18n.Lang
}

func (s *Server) handleLoginShow(w http.ResponseWriter, r *http.Request) {
	s.renderStandalone(w, r, "login_page", loginData{Lang: resolveLang(r)})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.serverError(w, r, err)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	lang := resolveLang(r)

	u, err := s.store.GetUserByEmail(r.Context(), email)
	if err != nil {
		// A nonexistent email and a wrong password get the exact same
		// message, so the login page can't be used as a tool to probe
		// whether a given email is registered.
		s.renderStandalone(w, r, "login_page", loginData{Error: types.Msg("webui.auth.login.bad_credentials"), Lang: lang})
		return
	}
	if err := bcrypt.CompareHashAndPassword(u.PasswordHash, []byte(password)); err != nil {
		s.renderStandalone(w, r, "login_page", loginData{Error: types.Msg("webui.auth.login.bad_credentials"), Lang: lang})
		return
	}
	s.finishLogin(w, r, u)
}

type signupData struct {
	Error types.Message
	Lang  i18n.Lang
}

func (s *Server) handleSignupShow(w http.ResponseWriter, r *http.Request) {
	s.renderStandalone(w, r, "signup_page", signupData{Lang: resolveLang(r)})
}

// emailPattern only does a rough shape check (an @, with non-empty
// characters on both sides) -- the real authoritative uniqueness check
// lives at the database layer (005_users.sql's case-insensitive unique
// index); this doesn't reinvent a full email-syntax validator, it just
// blocks obviously-not-an-email input.
var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// minPasswordLen is the minimum password length requirement -- it only
// blocks obviously-too-weak passwords (empty, a few digits), without
// enforcing complexity rules (requiring both upper/lowercase and symbols);
// nobody has asked for that level of strictness at this stage.
const minPasswordLen = 8

func (s *Server) handleSignupSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.serverError(w, r, err)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	lang := resolveLang(r)

	if !emailPattern.MatchString(email) {
		s.renderStandalone(w, r, "signup_page", signupData{Error: types.Msg("webui.auth.signup.bad_email"), Lang: lang})
		return
	}
	if len(password) < minPasswordLen {
		s.renderStandalone(w, r, "signup_page", signupData{Error: types.Msg("webui.auth.signup.short_password"), Lang: lang})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Seed the new account's language preference from whatever language
	// they're currently browsing in, rather than always starting from
	// storage's own "zh" default -- someone signing up with the English
	// toggle already on shouldn't have their account silently reset to
	// Chinese on next login from a fresh browser.
	u := storage.User{ID: idgen.NewUUID(), Email: email, PasswordHash: hash, PreferredLang: string(lang)}
	if err := s.store.CreateUser(r.Context(), u); err != nil {
		if errors.Is(err, storage.ErrEmailTaken) {
			s.renderStandalone(w, r, "signup_page", signupData{Error: types.Msg("webui.auth.signup.email_taken"), Lang: lang})
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.finishLogin(w, r, u)
}

// finishLogin creates the session, sets the cookie, and redirects to the
// dashboard -- both a successful login and a successful signup share this
// exact finishing step.
//
// It also sets the tf_lang cookie from the account's own stored preference,
// so the language choice follows the account across devices/browsers rather
// than being stuck to whichever single browser happened to hold the cookie
// (see lang.go's handleSetLang, which is what wrote this preference in the
// first place). This intentionally overrides whatever tf_lang this browser
// already had -- the account's standing preference is treated as more
// authoritative than a pre-login guess made by a browser that may never have
// seen this account before.
func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, u storage.User) {
	token, err := s.sessions.create(u.ID, u.Email)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	})
	http.SetCookie(w, &http.Cookie{
		Name:     langCookieName,
		Value:    string(i18n.ParseLang(u.PreferredLang)),
		Path:     "/",
		Expires:  time.Now().Add(langCookieTTL),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}
