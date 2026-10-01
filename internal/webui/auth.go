package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const sessionCookieName = "tf_session"
const sessionTTL = 24 * time.Hour

// sessionData is the information one login session carries. Before the
// multi-tenant SaaS rework, this only had an expiry time -- "logged in or
// not" was the only thing that needed checking; now it must also know "who
// is logged in," since every request's data access has to be filtered by
// this identity. Email is cached here too (fetched from the users table
// once at login time), so the nav bar can show the current login email
// without an extra database query on every single page render.
type sessionData struct {
	UserID  string
	Email   string
	Expires time.Time
}

// sessionStore is an in-process table of login sessions; a single-process
// deployment needs no external store like Redis or a database for this --
// every session simply expires on process restart and the user logs back
// in, the same tradeoff as the Agent config under /settings needing to be
// set up again after a restart.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]sessionData
}

func newSessionStore() *sessionStore {
	return &sessionStore{sessions: make(map[string]sessionData)}
}

func (s *sessionStore) create(userID, email string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[token] = sessionData{UserID: userID, Email: email, Expires: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	return token, nil
}

// lookup returns the session data for a token; a nonexistent or expired token both return ok=false.
func (s *sessionStore) lookup(token string) (sessionData, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.sessions[token]
	if !ok {
		return sessionData{}, false
	}
	if time.Now().After(data.Expires) {
		delete(s.sessions, token)
		return sessionData{}, false
	}
	return data, true
}

func (s *sessionStore) revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, token)
}

// ctxKey is a private key type for context.WithValue, avoiding key-type collisions with other packages.
type ctxKey int

const userCtxKey ctxKey = iota

// requireAuth wraps a login check: redirects to the login page when there's
// no valid session; with a valid session, injects the logged-in user's
// identity into the request context, for a handler to pull out via
// currentUserID/currentUserEmail.
//
// There's no longer a "no password configured, so no auth required"
// branch -- under multi-tenant SaaS, auth is each user's own email and
// password, not a single process-wide switch; tests instead go through a
// real signup/login to obtain a session (see the test helper near
// fakestore_test.go), with no more "skip auth in tests" path.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		data, ok := s.sessions.lookup(c.Value)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		ctx := context.WithValue(r.Context(), userCtxKey, data)
		next(w, r.WithContext(ctx))
	}
}

// currentUserID pulls the current logged-in user's ID out of an
// authenticated request's context. requireAuth already guarantees this
// value exists by the time a handler runs -- ok being false should only
// happen when a test calls a handler directly, bypassing requireAuth.
func currentUserID(r *http.Request) (string, bool) {
	data, ok := r.Context().Value(userCtxKey).(sessionData)
	return data.UserID, ok
}

// currentUserEmail is currentUserID's email counterpart, for display in the nav bar.
func currentUserEmail(r *http.Request) string {
	data, _ := r.Context().Value(userCtxKey).(sessionData)
	return data.Email
}
