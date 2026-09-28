package notify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramClientSendMessageHitsCorrectPathAndBody(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := NewTelegramClient("test-token", WithTelegramBaseURL(srv.URL))
	if err := client.SendMessage(t.Context(), "12345", "hello"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/bottest-token/sendMessage" {
		t.Errorf("request path = %q, want /bottest-token/sendMessage", gotPath)
	}
	if gotBody["chat_id"] != "12345" || gotBody["text"] != "hello" {
		t.Errorf("request body mismatch: %+v", gotBody)
	}
}

func TestTelegramClientSurfacesAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"description":"chat not found"}`))
	}))
	defer srv.Close()

	client := NewTelegramClient("test-token", WithTelegramBaseURL(srv.URL))
	err := client.SendMessage(t.Context(), "bad-chat", "hello")
	if err == nil {
		t.Fatal("should error when Telegram returns ok=false")
	}
	if !strings.Contains(err.Error(), "chat not found") {
		t.Errorf("error message should include the description Telegram returned, got: %v", err)
	}
}

func TestTelegramClientRejectsEmptyBotToken(t *testing.T) {
	client := NewTelegramClient("")
	if err := client.SendMessage(t.Context(), "12345", "hello"); err == nil {
		t.Fatal("should error when no bot token is configured")
	}
}

func TestTelegramClientUsesInjectedHTTPClient(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	client := NewTelegramClient("t", WithTelegramBaseURL(srv.URL), WithTelegramHTTPClient(srv.Client()))
	if err := client.SendMessage(t.Context(), "1", "x"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("should have called the test server")
	}
}
