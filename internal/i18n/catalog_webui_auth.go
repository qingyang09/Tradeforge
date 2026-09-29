package i18n

// Catalog entries for internal/webui/templates/login.html and signup.html,
// and the errors handlers_auth.go builds for them. Class A, same as
// catalog_webui_layout.go.
func init() {
	register(LangEN, map[string]string{
		"webui.auth.login.title":           "Log in to TradeForge",
		"webui.auth.login.title_tag":       "Log In",
		"webui.auth.login.submit":          "Log In",
		"webui.auth.login.no_account":      "Don't have an account?",
		"webui.auth.login.signup_link":     "Sign up",
		"webui.auth.login.bad_credentials": "Incorrect email or password",
		"webui.auth.signup.title":          "Create a TradeForge account",
		"webui.auth.signup.title_tag":      "Sign Up",
		"webui.auth.signup.submit":         "Sign Up",
		"webui.auth.signup.have_account":   "Already have an account?",
		"webui.auth.signup.login_link":     "Log in",
		"webui.auth.signup.bad_email":      "Please enter a valid email address",
		"webui.auth.signup.short_password": "Password must be at least 8 characters",
		"webui.auth.signup.email_taken":    "This email is already registered",
		"webui.auth.field.email":           "Email",
		"webui.auth.field.password":        "Password",
	})
	register(LangZH, map[string]string{
		"webui.auth.login.title":           "TradeForge 登录",
		"webui.auth.login.title_tag":       "登录",
		"webui.auth.login.submit":          "登录",
		"webui.auth.login.no_account":      "还没有账号？",
		"webui.auth.login.signup_link":     "注册",
		"webui.auth.login.bad_credentials": "邮箱或密码不正确",
		"webui.auth.signup.title":          "注册 TradeForge 账号",
		"webui.auth.signup.title_tag":      "注册",
		"webui.auth.signup.submit":         "注册",
		"webui.auth.signup.have_account":   "已经有账号？",
		"webui.auth.signup.login_link":     "登录",
		"webui.auth.signup.bad_email":      "请输入合法的邮箱地址",
		"webui.auth.signup.short_password": "密码至少需要 8 位",
		"webui.auth.signup.email_taken":    "该邮箱已注册",
		"webui.auth.field.email":           "邮箱",
		"webui.auth.field.password":        "密码",
	})
}
