package webui

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"tradeforge/internal/secretcrypto"
)

func saveBrokerForm(label, broker, apiKey, apiSecret, passphrase string) map[string]string {
	return map[string]string{
		"label": label, "broker": broker,
		"api_key": apiKey, "api_secret": apiSecret, "passphrase": passphrase,
	}
}

func TestHandleSettingsSaveBrokerPersistsEncryptedCredentials(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	userID := testUserID(srv)

	w := postForm(srv, "/settings/brokers",
		urlValues(saveBrokerForm("我的 OKX", "okx-demo", "ok-key", "ok-secret", "ok-pass")))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "已保存并启用") {
		t.Errorf("应提示已保存并启用，实际：%s", w.Body.String())
	}

	if len(store.brokers) != 1 {
		t.Fatalf("应保存 1 份配置到 store，实际 %d 份", len(store.brokers))
	}
	profiles, err := store.ListBrokerProfiles(t.Context(), userID)
	if err != nil || len(profiles) != 1 {
		t.Fatalf("ListBrokerProfiles 应返回 1 份，err=%v profiles=%v", err, profiles)
	}
	p := profiles[0]
	if p.Label != "我的 OKX" || p.Broker != "okx-demo" || !p.IsActive {
		t.Errorf("保存的配置不符合预期：%+v", p)
	}
	if p.UserID != userID {
		t.Errorf("保存的配置应该归属当前登录用户，实际 UserID = %q，期望 %q", p.UserID, userID)
	}
	if strings.Contains(string(p.EncryptedCredentials), "ok-secret") {
		t.Error("EncryptedCredentials 不应包含明文 secret")
	}

	plaintext, err := secretcrypto.Decrypt(testMasterKey, p.EncryptedCredentials, p.KeySalt, p.KeyNonce)
	if err != nil {
		t.Fatalf("解密失败：%v", err)
	}
	var creds brokerCredentials
	if err := json.Unmarshal([]byte(plaintext), &creds); err != nil {
		t.Fatalf("解密结果不是合法的凭据 JSON：%v", err)
	}
	if creds.APIKey != "ok-key" || creds.APISecret != "ok-secret" || creds.Passphrase != "ok-pass" {
		t.Errorf("解密出的凭据不符合预期：%+v", creds)
	}
}

func TestHandleSettingsSaveBrokerRejectsOKXWithoutPassphrase(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)

	w := postForm(srv, "/settings/brokers",
		urlValues(saveBrokerForm("我的 OKX", "okx-demo", "ok-key", "ok-secret", "")))
	if !strings.Contains(w.Body.String(), "保存失败") {
		t.Errorf("OKX 缺少 passphrase 应该报错，实际：%s", w.Body.String())
	}
	if len(store.brokers) != 0 {
		t.Error("校验失败时不该写库")
	}
}

func TestHandleSettingsSaveBrokerRejectsPaperKind(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)

	w := postForm(srv, "/settings/brokers",
		urlValues(saveBrokerForm("模拟盘", "paper", "", "", "")))
	if !strings.Contains(w.Body.String(), "不支持的下单通道") {
		t.Errorf("paper 不该出现在这个表单里，实际：%s", w.Body.String())
	}
}

func TestHandleSettingsSaveBrokerActivationIsScopedPerBroker(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	userID := testUserID(srv)

	postForm(srv, "/settings/brokers",
		urlValues(saveBrokerForm("我的 OKX", "okx-demo", "ok-key", "ok-secret", "ok-pass")))
	postForm(srv, "/settings/brokers",
		urlValues(saveBrokerForm("我的币安", "binance-testnet", "bn-key", "bn-secret", "")))

	active, err := store.ActiveBrokerProfile(t.Context(), userID, "okx-demo")
	if err != nil || active.Label != "我的 OKX" {
		t.Errorf("保存 Binance 配置不该影响 OKX 当前生效的配置，实际：%+v err=%v", active, err)
	}
	activeBinance, err := store.ActiveBrokerProfile(t.Context(), userID, "binance-testnet")
	if err != nil || activeBinance.Label != "我的币安" {
		t.Errorf("Binance 当前生效的配置应该是刚保存的那份，实际：%+v err=%v", activeBinance, err)
	}
}

func TestHandleSettingsActivateBrokerProfileSwitchesActiveRow(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	userID := testUserID(srv)

	postForm(srv, "/settings/brokers",
		urlValues(saveBrokerForm("配置A", "okx-demo", "k1", "s1", "p1")))
	postForm(srv, "/settings/brokers",
		urlValues(saveBrokerForm("配置B", "okx-demo", "k2", "s2", "p2")))

	profiles, _ := store.ListBrokerProfiles(t.Context(), userID)
	var idA, idB string
	for _, p := range profiles {
		if p.Label == "配置A" {
			idA = p.ID
		}
		if p.Label == "配置B" {
			idB = p.ID
		}
	}
	if idA == "" || idB == "" {
		t.Fatalf("应该有两份配置，实际：%+v", profiles)
	}

	active, _ := store.ActiveBrokerProfile(t.Context(), userID, "okx-demo")
	if active.ID != idB {
		t.Fatalf("保存后应自动激活最新一份 B，实际生效的是 %q", active.ID)
	}

	w := postForm(srv, "/settings/brokers/"+idA+"/activate", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "已切换到「配置A」") {
		t.Errorf("应提示切换成功，实际：%s", w.Body.String())
	}

	active, err := store.ActiveBrokerProfile(t.Context(), userID, "okx-demo")
	if err != nil || active.ID != idA {
		t.Errorf("激活后当前生效的应该是 A，实际：%+v err=%v", active, err)
	}
}

func TestHandleSettingsDeleteBrokerProfile(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	postForm(srv, "/settings/brokers",
		urlValues(saveBrokerForm("配置A", "okx-demo", "k1", "s1", "p1")))
	profiles, _ := store.ListBrokerProfiles(t.Context(), testUserID(srv))
	id := profiles[0].ID

	w := postForm(srv, "/settings/brokers/"+id+"/delete", nil)
	if !strings.Contains(w.Body.String(), "已删除") {
		t.Errorf("应提示已删除，实际：%s", w.Body.String())
	}
	if len(store.brokers) != 0 {
		t.Errorf("应从 store 里删除，实际还剩 %d 份", len(store.brokers))
	}
}

// TestHandleSettingsBrokerActionsRejectOtherUsersProfile 是多用户隔离的核心属性：
// B 用户不能激活/删除 A 用户的交易所配置，报错要跟"这份配置根本不存在"完全一样。
func TestHandleSettingsBrokerActionsRejectOtherUsersProfile(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	postForm(srv, "/settings/brokers",
		urlValues(saveBrokerForm("A 的配置", "okx-demo", "k1", "s1", "p1")))
	profiles, _ := store.ListBrokerProfiles(t.Context(), testUserID(srv))
	otherUsersProfileID := profiles[0].ID

	loginAsNewUser(store, srv)

	w := postForm(srv, "/settings/brokers/"+otherUsersProfileID+"/activate", nil)
	if !strings.Contains(w.Body.String(), "找不到这份配置") {
		t.Errorf("激活别人的配置应该报'找不到'，实际：%s", w.Body.String())
	}

	w = postForm(srv, "/settings/brokers/"+otherUsersProfileID+"/delete", nil)
	if !strings.Contains(w.Body.String(), "删除失败") {
		t.Errorf("删除别人的配置应该报错，实际：%s", w.Body.String())
	}
	if len(store.brokers) != 1 {
		t.Error("别人的配置不应该被删掉")
	}
}
