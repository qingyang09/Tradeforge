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
		t.Fatalf("意外错误：%v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q，期望 application/json", gotContentType)
	}
	var got WebhookPayload
	if err := json.Unmarshal(gotBody, &got); err != nil {
		t.Fatalf("解析收到的请求体失败：%v", err)
	}
	if got.StrategyID != "s1" || got.Title != "测试标题" || got.Mode != ModeLive {
		t.Errorf("请求体不符：%+v", got)
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
		t.Fatalf("意外错误：%v", err)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(gotBody)
	want := hex.EncodeToString(mac.Sum(nil))
	if gotSig != want {
		t.Errorf("签名 = %q，期望 %q", gotSig, want)
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
		t.Fatalf("意外错误：%v", err)
	}
	if sawHeader {
		t.Errorf("没配签名密钥时不应该带签名头，实际：%q", gotSig)
	}
}

func TestHTTPWebhookSenderErrorsOnNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sender := NewHTTPWebhookSender(nil)
	if err := sender.SendWebhook(t.Context(), srv.URL, "", WebhookPayload{}); err == nil {
		t.Fatal("非 2xx 状态码应该报错")
	}
}
