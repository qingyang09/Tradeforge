package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
)

// rawRequest 发一个不带任何会话 cookie 的请求——测试鉴权本身要绕开 getPage/postForm
// 默认自动登录的行为，不然永远测不出"没登录会被拒绝"这件事。
func rawRequest(srv *Server, method, path string, form url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	return w
}

// createTestUser 直接往 store 里插入一个已知邮箱/密码的账号，绕开 signup 表单——
// 给"用这个账号登录"这类测试用，不用每次都先走一遍注册流程。
func createTestUser(t *testing.T, store *fakeStore, email, password string) storage.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("生成测试密码哈希失败：%v", err)
	}
	u := storage.User{ID: idgen.NewUUID(), Email: email, PasswordHash: hash}
	store.users[u.ID] = u
	return u
}

func TestUnauthenticatedRequestRedirectsToLogin(t *testing.T) {
	srv := newTestServer(t, newFakeStore())

	w := rawRequest(srv, http.MethodGet, "/", nil)
	if w.Code != http.StatusFound {
		t.Fatalf("状态码 = %d，期望 302 跳转到登录页", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q，期望 /login", loc)
	}
}

func TestLoginWithWrongPasswordRejected(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	createTestUser(t, store, "user@example.com", "correct-horse-battery")

	w := rawRequest(srv, http.MethodPost, "/login",
		url.Values{"email": {"user@example.com"}, "password": {"wrong"}})
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "邮箱或密码不正确") {
		t.Errorf("密码错误应提示，实际：%s", w.Body.String())
	}
	if len(w.Result().Cookies()) != 0 {
		t.Error("密码错误不应下发会话 cookie")
	}
}

func TestLoginWithUnknownEmailRejectedWithSameMessage(t *testing.T) {
	srv := newTestServer(t, newFakeStore())

	w := rawRequest(srv, http.MethodPost, "/login",
		url.Values{"email": {"nobody@example.com"}, "password": {"whatever"}})
	// 邮箱不存在跟密码错误必须是同一句提示——不能让登录页变成"这个邮箱注册过没有"
	// 的探测工具，见 handleLoginSubmit 的注释。
	if !strings.Contains(w.Body.String(), "邮箱或密码不正确") {
		t.Errorf("未注册的邮箱应提示跟密码错误一样的话，实际：%s", w.Body.String())
	}
}

func TestLoginWithCorrectPasswordGrantsAccess(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	createTestUser(t, store, "user@example.com", "correct-horse-battery")

	loginResp := rawRequest(srv, http.MethodPost, "/login",
		url.Values{"email": {"user@example.com"}, "password": {"correct-horse-battery"}})
	if loginResp.Code != http.StatusFound {
		t.Fatalf("登录成功应跳转，状态码 = %d", loginResp.Code)
	}
	cookies := loginResp.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("登录成功应下发会话 cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("带着有效会话访问看板，状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	createTestUser(t, store, "user@example.com", "correct-horse-battery")

	loginResp := rawRequest(srv, http.MethodPost, "/login",
		url.Values{"email": {"user@example.com"}, "password": {"correct-horse-battery"}})
	cookies := loginResp.Result().Cookies()

	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
	for _, c := range cookies {
		logoutReq.AddCookie(c)
	}
	logoutW := httptest.NewRecorder()
	srv.Routes().ServeHTTP(logoutW, logoutReq)
	if logoutW.Code != http.StatusFound {
		t.Fatalf("退出登录应跳转，状态码 = %d", logoutW.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	srv.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("退出登录后旧会话应失效，状态码 = %d", w.Code)
	}
}

// ---------- 注册 ----------

func TestSignupCreatesAccountAndLogsIn(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)

	w := rawRequest(srv, http.MethodPost, "/signup",
		url.Values{"email": {"new@example.com"}, "password": {"a-strong-password"}})
	if w.Code != http.StatusFound {
		t.Fatalf("注册成功应跳转，状态码 = %d，响应：%s", w.Code, w.Body.String())
	}
	if len(w.Result().Cookies()) == 0 {
		t.Fatal("注册成功应自动登录、下发会话 cookie")
	}

	u, err := store.GetUserByEmail(t.Context(), "new@example.com")
	if err != nil {
		t.Fatalf("应该能查到刚注册的账号：%v", err)
	}
	if err := bcrypt.CompareHashAndPassword(u.PasswordHash, []byte("a-strong-password")); err != nil {
		t.Error("存的密码哈希应该能验证出原密码")
	}
}

func TestSignupRejectsDuplicateEmailCaseInsensitive(t *testing.T) {
	store := newFakeStore()
	srv := newTestServer(t, store)
	createTestUser(t, store, "dup@example.com", "whatever-password")

	w := rawRequest(srv, http.MethodPost, "/signup",
		url.Values{"email": {"DUP@example.com"}, "password": {"another-password"}})
	if !strings.Contains(w.Body.String(), "该邮箱已注册") {
		t.Errorf("重复邮箱（大小写不同）应该被拒绝，实际：%s", w.Body.String())
	}
}

func TestSignupRejectsMalformedEmail(t *testing.T) {
	srv := newTestServer(t, newFakeStore())
	w := rawRequest(srv, http.MethodPost, "/signup",
		url.Values{"email": {"not-an-email"}, "password": {"a-strong-password"}})
	if !strings.Contains(w.Body.String(), "合法的邮箱") {
		t.Errorf("不合法的邮箱应该被拒绝，实际：%s", w.Body.String())
	}
}

func TestSignupRejectsShortPassword(t *testing.T) {
	srv := newTestServer(t, newFakeStore())
	w := rawRequest(srv, http.MethodPost, "/signup",
		url.Values{"email": {"user@example.com"}, "password": {"short"}})
	if !strings.Contains(w.Body.String(), "至少需要 8 位") {
		t.Errorf("太短的密码应该被拒绝，实际：%s", w.Body.String())
	}
}
