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

// sessionData 是一条登录会话携带的信息。多用户 SaaS 改造之前这里只有过期时间——
// "有没有登录"就是唯一要检查的事；现在必须知道"是谁登录的"，每个请求的数据访问都要
// 按这个身份过滤。Email 一并缓存在这里（登录时从 users 表查出来存一份），是为了让
// 导航栏显示当前登录邮箱时不需要每次渲染页面都额外查一次库。
type sessionData struct {
	UserID  string
	Email   string
	Expires time.Time
}

// sessionStore 是进程内的登录会话表，单进程部署不需要 Redis/数据库这种外部存储——
// 进程重启后所有会话失效，用户重新登录即可，跟 /settings 里的 Agent 配置在重启后
// 也需要重新走一遍设置流程是同一个道理。
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

// lookup 返回 token 对应的会话数据；token 不存在或已过期都返回 ok=false。
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

// ctxKey 是 context.WithValue 用的私有 key 类型，避免跟其它包的 key 撞类型。
type ctxKey int

const userCtxKey ctxKey = iota

// requireAuth 包一层登录校验：没有有效会话就跳转登录页；有效会话则把登录用户的身份
// 注入 request context，供 handler 通过 currentUserID/currentUserEmail 取出。
//
// 不再有"没配密码就不设防"这个分支——多用户 SaaS 下鉴权是每个用户自己的邮箱密码，
// 不是一个进程级别的开关，测试改用真实注册/登录取得 session（见 fakestore_test.go
// 附近的测试 helper），不再有"测试环境跳过鉴权"这条路。
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

// currentUserID 从已认证请求的 context 里取出当前登录用户的 ID。requireAuth 已经
// 保证了走到 handler 内部时这个值一定存在——ok 为 false 只应该发生在测试直接调用
// handler、绕过了 requireAuth 的场景。
func currentUserID(r *http.Request) (string, bool) {
	data, ok := r.Context().Value(userCtxKey).(sessionData)
	return data.UserID, ok
}

// currentUserEmail 同 currentUserID，取邮箱，供导航栏展示。
func currentUserEmail(r *http.Request) string {
	data, _ := r.Context().Value(userCtxKey).(sessionData)
	return data.Email
}
