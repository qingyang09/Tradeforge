package webui

import (
	"context"
	"net/http"
	"sync"

	"tradeforge/internal/agent"
	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
)

// testDefaultUserID/testDefaultUserEmail 是每个测试 Server 默认登录用户的固定身份——
// 固定下来（不是每次随机生成一个）是为了让测试文件里大量"先往 store 里塞一条
// StrategyConfig{ID: ...}、再建 Server、再请求"这种写法能直接把 UserID 写成这个常量，
// 不需要先建好 Server 才能拿到 userID、被迫把每个测试的语句顺序倒过来。每个测试都有
// 自己独立的 fakeStore，用同一个常量 ID 不会跨测试冲突。
const (
	testDefaultUserID    = "00000000-0000-4000-8000-000000000099"
	testDefaultUserEmail = "test-default@example.com"
)

// testMasterKey 是测试用的服务端主密钥——足够长（>=32 位），满足
// cmd/webui/main.go 生产启动路径的同一条校验，让设置页面的加密落库路径在测试里
// 走到底，不需要再有一条"没配主密钥就退回内存态"的分支（那条分支在生产代码里已经
// 整个删掉了，测试也不该继续依赖一个不存在的行为）。
const testMasterKey = "test-master-key-at-least-32-bytes-long!"

// testSessionTokens 把每个测试用 *Server 映射到它默认登录用户的会话 token——
// getPage/postForm 发请求前会自动查这张表、带上对应的 cookie，这样现有大量测试
// 调用 postForm(srv, path, form) 的写法不用因为"每个路由现在都要求登录"而全部
// 手工改成先建会话再发请求，改动收敛在这几个共享 helper 内部。
var (
	testSessionMu     sync.Mutex
	testSessionTokens = map[*Server]string{}
)

// loginTestUser 在 store 里建一个默认测试账号、登录、把 session token 记进
// testSessionTokens，供 getPage/postForm 自动使用。newTestServer 系列的构造函数
// 统一调用它，不需要每个测试自己操心"要不要登录"。
func loginTestUser(store *fakeStore, srv *Server) {
	u := storage.User{ID: testDefaultUserID, Email: testDefaultUserEmail}
	store.users[u.ID] = u
	token, err := srv.sessions.create(u.ID, u.Email)
	if err != nil {
		panic("创建测试会话失败：" + err.Error()) // 测试基础设施本身的错误，不是被测代码的错误
	}
	testSessionMu.Lock()
	testSessionTokens[srv] = token
	testSessionMu.Unlock()
}

// testUserID 返回某个测试 Server 默认登录用户的 ID——测试断言"存进去的数据 UserID
// 对不对"时用它，不用每次都重新解析 session token。
func testUserID(srv *Server) string {
	testSessionMu.Lock()
	token := testSessionTokens[srv]
	testSessionMu.Unlock()
	data, _ := srv.sessions.lookup(token)
	return data.UserID
}

// currentAgent 是测试专用的便捷包装：老测试写的是 srv.currentAgent()（单用户时代
// 的写法），多用户改造后 Agent 按用户缓存，这里补一层用 testUserID(srv) 当默认用户，
// 保留旧测试调用点尽量不变。
func currentAgent(srv *Server) *agent.Agent {
	ag, _ := srv.userAgent(context.Background(), testUserID(srv))
	return ag
}

// attachTestSession 给请求带上 srv 对应的登录 cookie；srv 没有登录过（理论上不会
// 发生，newTestServer 系列都会自动登录）就什么也不做，交给 requireAuth 正常拒绝。
func attachTestSession(srv *Server, req *http.Request) {
	testSessionMu.Lock()
	token, ok := testSessionTokens[srv]
	testSessionMu.Unlock()
	if ok {
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}
}

// loginAsNewUser 让同一个 srv 切换成另一个新用户登录（用于跨用户隔离测试：先以
// 用户 A 身份创建数据，再切到用户 B 断言看不到/改不动）。返回新用户的 ID。
func loginAsNewUser(store *fakeStore, srv *Server) string {
	userID := idgen.NewUUID()
	u := storage.User{ID: userID, Email: "test-" + userID + "@example.com"}
	store.users[userID] = u
	token, err := srv.sessions.create(u.ID, u.Email)
	if err != nil {
		panic("创建测试会话失败：" + err.Error())
	}
	testSessionMu.Lock()
	testSessionTokens[srv] = token
	testSessionMu.Unlock()
	return userID
}
