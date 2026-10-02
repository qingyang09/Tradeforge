//go:build integration

// Requires a Postgres started via docker-compose, with migrations/005_users.sql
// and migrations/011_users_preferred_lang.sql already applied:
//
//	docker compose up -d
//	go test -tags=integration ./internal/storage/... -run PreferredLang -v
package storage

import (
	"context"
	"testing"
	"time"

	"tradeforge/internal/config"
	"tradeforge/pkg/idgen"
)

func TestUserPreferredLangDefaultsAndUpdates(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connect to Postgres (did you run docker compose up -d and apply migration 011?): %v", err)
	}
	defer store.Close()

	// A caller that leaves PreferredLang unset should still get the "zh"
	// default persisted, not an empty string -- confirms CreateUser's
	// normalization actually reaches the database column, not just an
	// in-memory zero value.
	u := User{ID: idgen.NewUUID(), Email: "pref-lang-default-" + idgen.NewUUID() + "@integration.test", PasswordHash: []byte("x")}
	if err := store.CreateUser(ctx, u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	got, err := store.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.PreferredLang != "zh" {
		t.Errorf("PreferredLang for a user created with no explicit preference = %q, want %q", got.PreferredLang, "zh")
	}

	// A caller that does set PreferredLang (e.g. signup capturing the
	// current browser language, see handlers_auth.go) should get that exact
	// value persisted, not silently overridden to the default.
	u2 := User{
		ID: idgen.NewUUID(), Email: "pref-lang-explicit-" + idgen.NewUUID() + "@integration.test",
		PasswordHash: []byte("x"), PreferredLang: "en",
	}
	if err := store.CreateUser(ctx, u2); err != nil {
		t.Fatalf("create user with explicit preference: %v", err)
	}
	got2, err := store.GetUser(ctx, u2.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got2.PreferredLang != "en" {
		t.Errorf("PreferredLang for a user created with an explicit \"en\" preference = %q, want %q", got2.PreferredLang, "en")
	}

	// SetUserPreferredLang (called by the webui's language toggle, see
	// internal/webui/lang.go's handleSetLang) updates the stored value, and
	// every read path (GetUser, GetUserByEmail) reflects the change --
	// cmd/notifier reads it back via GetUser at alert time.
	if err := store.SetUserPreferredLang(ctx, u.ID, "en"); err != nil {
		t.Fatalf("set preferred lang: %v", err)
	}
	byEmail, err := store.GetUserByEmail(ctx, u.Email)
	if err != nil {
		t.Fatalf("get user by email: %v", err)
	}
	if byEmail.PreferredLang != "en" {
		t.Errorf("after SetUserPreferredLang, GetUserByEmail's PreferredLang = %q, want %q", byEmail.PreferredLang, "en")
	}
}
