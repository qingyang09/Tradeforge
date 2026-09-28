package webui

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
)

type loginData struct {
	Error string
}

func (s *Server) handleLoginShow(w http.ResponseWriter, r *http.Request) {
	s.renderStandalone(w, "login_page", loginData{})
}

func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.serverError(w, err)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	u, err := s.store.GetUserByEmail(r.Context(), email)
	if err != nil {
		// 邮箱不存在和密码错误给同一句提示，不让登录页面变成一个"这个邮箱注册过没有"
		// 的探测工具。
		s.renderStandalone(w, "login_page", loginData{Error: "邮箱或密码不正确"})
		return
	}
	if err := bcrypt.CompareHashAndPassword(u.PasswordHash, []byte(password)); err != nil {
		s.renderStandalone(w, "login_page", loginData{Error: "邮箱或密码不正确"})
		return
	}
	s.finishLogin(w, r, u)
}

type signupData struct {
	Error string
}

func (s *Server) handleSignupShow(w http.ResponseWriter, r *http.Request) {
	s.renderStandalone(w, "signup_page", signupData{})
}

// emailPattern 只做粗略的形状校验（有 @、@ 前后都有非空字符）——真正权威的唯一性
// 检查在数据库层（005_users.sql 的大小写不敏感唯一索引），这里不重新发明一遍完整的
// 邮箱语法校验，只挡明显不是邮箱的输入。
var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// minPasswordLen 是最低密码长度要求——只挡明显太弱的密码（空、几位数字），不做复杂度
// 强制（大小写/符号必须都有那一套），这个阶段没人要求做到那么细。
const minPasswordLen = 8

func (s *Server) handleSignupSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.serverError(w, err)
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")

	if !emailPattern.MatchString(email) {
		s.renderStandalone(w, "signup_page", signupData{Error: "请输入合法的邮箱地址"})
		return
	}
	if len(password) < minPasswordLen {
		s.renderStandalone(w, "signup_page", signupData{Error: "密码至少需要 8 位"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		s.serverError(w, err)
		return
	}
	u := storage.User{ID: idgen.NewUUID(), Email: email, PasswordHash: hash}
	if err := s.store.CreateUser(r.Context(), u); err != nil {
		if errors.Is(err, storage.ErrEmailTaken) {
			s.renderStandalone(w, "signup_page", signupData{Error: "该邮箱已注册"})
			return
		}
		s.serverError(w, err)
		return
	}
	s.finishLogin(w, r, u)
}

// finishLogin 建会话、种 cookie、跳转到看板——登录和注册成功后走的是同一段收尾逻辑。
func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, u storage.User) {
	token, err := s.sessions.create(u.ID, u.Email)
	if err != nil {
		s.serverError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		s.sessions.revoke(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusFound)
}
