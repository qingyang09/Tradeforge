package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"tradeforge/internal/i18n"
)

// TestResolveLangDefaultsToZhWithNoCookie confirms an absent tf_lang cookie
// never silently defaults to English -- the existing user base is
// Chinese-speaking, so an unset preference must stay Chinese.
func TestResolveLangDefaultsToZhWithNoCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := resolveLang(req); got != i18n.LangZH {
		t.Errorf("resolveLang with no cookie = %q, want %q", got, i18n.LangZH)
	}
}

// TestHandleSetLangPersistsToLoggedInAccount confirms that a logged-in
// user's language-toggle click doesn't just set the tf_lang cookie on this
// one browser -- it also writes the choice back to the account via
// SetUserPreferredLang, so the preference follows them to another device/
// browser at next login, and so cmd/notifier can later alert them in it.
func TestHandleSetLangPersistsToLoggedInAccount(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)

	w := getPage(srv, "/lang/en")
	if w.Code != http.StatusFound {
		t.Fatalf("GET /lang/en = %d, want %d", w.Code, http.StatusFound)
	}

	got := store.users[testDefaultUserID].PreferredLang
	if got != "en" {
		t.Errorf("after toggling to English while logged in, account PreferredLang = %q, want %q", got, "en")
	}
}

// TestHandleSetLangWithoutSessionJustSetsCookie confirms an anonymous
// visitor (no session cookie at all, e.g. browsing /login) can still toggle
// the language -- there's simply no account to persist the choice to.
func TestHandleSetLangWithoutSessionJustSetsCookie(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)

	req := httptest.NewRequest(http.MethodGet, "/lang/en", nil) // deliberately no session cookie attached
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("GET /lang/en (no session) = %d, want %d", w.Code, http.StatusFound)
	}
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == langCookieName && c.Value == "en" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the tf_lang cookie to be set to \"en\" even with no session")
	}
}

// TestFinishLoginSyncsCookieFromAccountPreference confirms that logging in
// sets tf_lang from the account's own stored preference, so a preference set
// on one browser follows the account to a fresh browser/device at login,
// rather than that second browser silently defaulting to Chinese forever.
func TestFinishLoginSyncsCookieFromAccountPreference(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)

	const email = "lang-sync@example.com"
	signupReq := httptest.NewRequest(http.MethodPost, "/signup", nil)
	signupReq.PostForm = map[string][]string{
		"email":    {email},
		"password": {"a-long-enough-password"},
	}
	signupW := httptest.NewRecorder()
	srv.Routes().ServeHTTP(signupW, signupReq)
	if signupW.Code != http.StatusFound {
		t.Fatalf("POST /signup = %d, want %d", signupW.Code, http.StatusFound)
	}

	var userID string
	for id, u := range store.users {
		if u.Email == email {
			userID = id
		}
	}
	if userID == "" {
		t.Fatalf("signup did not create a user record for %s", email)
	}
	if err := store.SetUserPreferredLang(context.Background(), userID, "en"); err != nil {
		t.Fatalf("SetUserPreferredLang: %v", err)
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/login", nil)
	loginReq.PostForm = map[string][]string{
		"email":    {email},
		"password": {"a-long-enough-password"},
	}
	loginW := httptest.NewRecorder()
	srv.Routes().ServeHTTP(loginW, loginReq)
	if loginW.Code != http.StatusFound {
		t.Fatalf("POST /login = %d, want %d", loginW.Code, http.StatusFound)
	}

	found := false
	for _, c := range loginW.Result().Cookies() {
		if c.Name == langCookieName && c.Value == "en" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected login to set tf_lang=en from the account's stored preference")
	}
}
