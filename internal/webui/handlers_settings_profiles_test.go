package webui

import (
	"net/http"
	"strings"
	"testing"

	"tradeforge/internal/strategy"
)

func saveProfileForm(label string) map[string]string {
	return map[string]string{
		"action": "save", "label": label, "provider": "anthropic",
		"api_key": "sk-ant-testkey-1234", "model": "", "base_url": "",
	}
}

func TestHandleSettingsSavePersistsEncryptedProfile(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	userID := testUserID(srv)

	w := postForm(srv, "/settings", urlValues(saveProfileForm("我的 Claude")))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "加密落库") {
		t.Errorf("应提示已加密落库，实际：%s", body)
	}

	if len(store.profiles) != 1 {
		t.Fatalf("应保存 1 份配置到 store，实际 %d 份", len(store.profiles))
	}

	profiles, err := store.ListAgentProfiles(t.Context(), userID)
	if err != nil || len(profiles) != 1 {
		t.Fatalf("ListAgentProfiles 应返回 1 份，err=%v profiles=%v", err, profiles)
	}
	p := profiles[0]
	if p.Label != "我的 Claude" || p.Provider != "anthropic" || !p.IsActive {
		t.Errorf("保存的配置不符合预期：%+v", p)
	}
	if p.UserID != userID {
		t.Errorf("保存的配置应该归属当前登录用户，实际 UserID = %q，期望 %q", p.UserID, userID)
	}
	if string(p.EncryptedAPIKey) == "sk-ant-testkey-1234" {
		t.Error("EncryptedAPIKey 不应是明文")
	}

	// 解密应该能拿回原始 key——验证真的是可用的加密，不是随便存了点字节。
	got, err := decryptProfileSecret(testMasterKey, p.EncryptedAPIKey, p.KeySalt, p.KeyNonce)
	if err != nil || got != "sk-ant-testkey-1234" {
		t.Errorf("解密结果 = (%q, %v)，期望原始 key", got, err)
	}

	_, status := srv.userAgent(t.Context(), userID)
	if status.ProfileID != p.ID {
		t.Errorf("内存里生效的 agentStatus.ProfileID = %q，期望等于保存的配置 ID %q",
			status.ProfileID, p.ID)
	}
}

func TestHandleSettingsActivateProfileSwitchesAgent(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	userID := testUserID(srv)

	// 保存两份配置，第二份会自动顶替第一份成为当前生效的。
	postForm(srv, "/settings", urlValues(saveProfileForm("配置A")))
	postForm(srv, "/settings", urlValues(saveProfileForm("配置B")))

	profiles, _ := store.ListAgentProfiles(t.Context(), userID)
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
	_, status := srv.userAgent(t.Context(), userID)
	if status.ProfileID != idB {
		t.Fatalf("保存后应自动激活最新一份 B，实际生效的是 %q", status.ProfileID)
	}

	// 切回配置A。
	w := postForm(srv, "/settings/profiles/"+idA+"/activate", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "已切换到「配置A」") {
		t.Errorf("应提示切换成功，实际：%s", w.Body.String())
	}
	_, status = srv.userAgent(t.Context(), userID)
	if status.ProfileID != idA {
		t.Errorf("激活后 ProfileID = %q，期望 %q", status.ProfileID, idA)
	}

	active, err := store.ActiveAgentProfile(t.Context(), userID)
	if err != nil || active.ID != idA {
		t.Errorf("数据库里的当前生效配置应同步更新为 A，实际：%+v err=%v", active, err)
	}
}

// TestHandleSettingsActivateProfileRejectsOtherUsersProfile 是多用户隔离的核心属性：
// B 用户不能激活/看到 A 用户的模型配置，报错要跟"这份配置根本不存在"完全一样，
// 不能让请求方探测出"这个 ID 存在，只是不是你的"。
func TestHandleSettingsActivateProfileRejectsOtherUsersProfile(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	postForm(srv, "/settings", urlValues(saveProfileForm("A 的配置")))
	profiles, _ := store.ListAgentProfiles(t.Context(), testUserID(srv))
	otherUsersProfileID := profiles[0].ID

	// 切换成另一个用户登录。
	loginAsNewUser(store, srv)

	w := postForm(srv, "/settings/profiles/"+otherUsersProfileID+"/activate", nil)
	if !strings.Contains(w.Body.String(), "找不到这份配置") {
		t.Errorf("激活别人的配置应该报'找不到'，实际：%s", w.Body.String())
	}
}

func TestHandleSettingsDeleteProfileClearsAgentWhenActiveDeleted(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	userID := testUserID(srv)
	postForm(srv, "/settings", urlValues(saveProfileForm("配置A")))

	profiles, _ := store.ListAgentProfiles(t.Context(), userID)
	if len(profiles) != 1 {
		t.Fatalf("应先保存出 1 份配置，实际 %d 份", len(profiles))
	}
	id := profiles[0].ID
	if ag, _ := srv.userAgent(t.Context(), userID); ag == nil {
		t.Fatal("保存后 agent 应该已就绪")
	}

	w := postForm(srv, "/settings/profiles/"+id+"/delete", nil)
	if !strings.Contains(w.Body.String(), "已删除") {
		t.Errorf("应提示已删除，实际：%s", w.Body.String())
	}
	if ag, _ := srv.userAgent(t.Context(), userID); ag != nil {
		t.Error("删除的正是当前生效的配置，内存里的 agent 也应被清空")
	}
	if len(store.profiles) != 0 {
		t.Errorf("应从 store 里删除，实际还剩 %d 份", len(store.profiles))
	}
}

func TestHandleSettingsDeleteProfileKeepsAgentWhenInactiveDeleted(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	userID := testUserID(srv)
	postForm(srv, "/settings", urlValues(saveProfileForm("配置A")))
	postForm(srv, "/settings", urlValues(saveProfileForm("配置B"))) // B 现在生效

	profiles, _ := store.ListAgentProfiles(t.Context(), userID)
	var idA string
	for _, p := range profiles {
		if p.Label == "配置A" {
			idA = p.ID
		}
	}
	if idA == "" {
		t.Fatalf("应该能找到配置A，实际：%+v", profiles)
	}
	_, statusBefore := srv.userAgent(t.Context(), userID)

	postForm(srv, "/settings/profiles/"+idA+"/delete", nil)

	ag, status := srv.userAgent(t.Context(), userID)
	if ag == nil {
		t.Error("删除的是未生效的配置，agent 不应被清空")
	}
	if status.ProfileID != statusBefore.ProfileID {
		t.Errorf("删除未生效的配置不应改变当前生效的 ProfileID，实际变成了 %q", status.ProfileID)
	}
}

// ---------- 模拟进程重启：新 Server 指向同一个 store，从数据库恢复模型配置 ----------

func TestUserAgentRestoresFromDBAfterRestart(t *testing.T) {
	store := newFakeStore()
	setupSrv := newTestServer(t, store)
	userID := testUserID(setupSrv)
	postForm(setupSrv, "/settings", urlValues(saveProfileForm("配置A")))

	// 模拟进程重启：全新的 Server，只是指向同一个 store、同一个用户 ID
	// （邮箱/密码不重要，userAgent 只按 userID 查）。
	freshSrv, err := New(store, nil, strategy.DefaultGate(), quietLogger())
	if err != nil {
		t.Fatalf("New() 失败：%v", err)
	}
	freshSrv.SetMasterKey(testMasterKey)

	ag, status := freshSrv.userAgent(t.Context(), userID)
	if ag == nil {
		t.Error("应从数据库恢复出一个可用的 agent")
	}
	if status.Model == "" {
		t.Error("恢复后的 agentStatus 应带上模型信息")
	}
}

func TestUserAgentNoOpWhenNoneSaved(t *testing.T) {
	srv := newTestServer(t, newFakeStore())
	ag, _ := srv.userAgent(t.Context(), testUserID(srv))
	if ag != nil {
		t.Error("没有已保存配置时不应凭空出现 agent")
	}
}

func TestUserAgentFailsGracefullyOnWrongMasterKey(t *testing.T) {
	store := newFakeStore()
	setupSrv := newTestServer(t, store)
	userID := testUserID(setupSrv)
	postForm(setupSrv, "/settings", urlValues(saveProfileForm("配置A")))

	// 服务端主密钥后来改了：新进程用新密钥去解密旧数据。
	freshSrv, err := New(store, nil, strategy.DefaultGate(), quietLogger())
	if err != nil {
		t.Fatalf("New() 失败：%v", err)
	}
	freshSrv.SetMasterKey("a-different-master-key-that-is-also-32+-bytes")

	ag, status := freshSrv.userAgent(t.Context(), userID)
	if ag != nil {
		t.Error("解密失败时不应设置一个用垃圾 key 构造出来的 agent")
	}
	if status.LastErr == "" {
		t.Error("密钥对不上时应该记录失败原因，而不是静默地什么都没发生")
	}
}
