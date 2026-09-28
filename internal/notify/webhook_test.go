package notify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPWebhookSenderPostsCorrectPayload(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewHTTPWebhookSender(nil)
	payload := WebhookPayload{
		Title: "测试标题", Body: "测试正文", Mode: ModeLive, StrategyID: "s1",
		Symbol: "BTCUSDT", Direction: "LONG", Score: 0.8, Price: "50000", Reason: "测试原因",
		Timestamp: time.Now(),
	}

	if err := sender.SendWebhook(t.Context(), srv.URL, "", payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	var got WebhookPayload
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("failed to parse received request body: %v", err)
	}
	if got.StrategyID != "s1" || got.Title != "测试标题" || got.Mode != ModeLive {
		t.Errorf("request body mismatch: %+v", got)
	}
}

func TestHTTPWebhookSenderSignsWithSecret(t *testing.T) {
	const secret = "my-secret"
	var gotBody []byte
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-TradeForge-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewHTTPWebhookSender(nil)
	payload := WebhookPayload{Title: "t", StrategyID: "s1"}
	if err := sender.SendWebhook(t.Context(), srv.URL, secret, payload); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBody)
	want := hex.EncodeToString(mac.Sum(nil))
	if gotSig != want {
		t.Errorf("signature = %q, want %q", gotSig, want)
	}
}

func TestHTTPWebhookSenderOmitsSignatureWithoutSecret(t *testing.T) {
	var gotSig string
	sawHeader := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig, sawHeader = r.Header.Get("X-TradeForge-Signature"), r.Header.Get("X-TradeForge-Signature") != ""
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender := NewHTTPWebhookSender(nil)
	if err := sender.SendWebhook(t.Context(), srv.URL, "", WebhookPayload{StrategyID: "s1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sawHeader {
		t.Errorf("should not include a signature header when no secret is configured, got: %q", gotSig)
	}
}

func TestHTTPWebhookSenderErrorsOnNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sender := NewHTTPWebhookSender(nil)
	if err := sender.SendWebhook(t.Context(), srv.URL, "", WebhookPayload{}); err == nil {
		t.Fatal("should error on a non-2xx status code")
	}
}
