package webui

import (
	"context"
	"net/http"
	"strings"

	"tradeforge/internal/agent"
	"tradeforge/internal/config"
	"tradeforge/internal/execution"
	"tradeforge/internal/i18n"
	"tradeforge/internal/modules"
	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// settingsData 是设置页面的渲染数据。
type settingsData struct {
	Providers []agent.Provider
	Status    agentStatus
	// Profiles 是当前用户已加密保存的模型配置，供切换/删除。
	Profiles []storage.AgentProfile

	// BrokerKinds 是设置页面"新增交易所配置"下拉框的选项（不含 paper，见
	// execution.CredentialedKinds 的注释）。BrokerProfiles 是当前用户已加密保存的
	// 交易所凭据，供切换/删除，语义跟 Profiles 对 LLM 配置的关系完全对称。
	BrokerKinds    []execution.BrokerKind
	BrokerProfiles []storage.BrokerProfile

	// NotificationChannels 是当前用户已保存的提醒渠道，供设置页面展示/切换/删除，
	// 跟 BrokerProfiles 对 BrokerKinds 的关系类似，但没有"生效中的一份"概念——见
	// storage.NotificationChannel 的注释。VAPIDPublicKey 是给"浏览器推送"卡片里的
	// 原生 JS 调用 PushManager.subscribe() 用的公钥，不是秘密。
	NotificationChannels []storage.NotificationChannel
	VAPIDPublicKey       string

	Message types.Message
	Error   types.Message
}

func (s *Server) handleSettingsShow(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	lang := resolveLang(r)
	profiles, err := s.store.ListAgentProfiles(r.Context(), userID)
	if err != nil {
		s.logger.Warn("读取已保存的模型配置失败", "err", err)
	}
	brokerProfiles, err := s.store.ListBrokerProfiles(r.Context(), userID)
	if err != nil {
		s.logger.Warn("读取已保存的交易所配置失败", "err", err)
	}
	notificationChannels, err := s.store.ListNotificationChannels(r.Context(), userID)
	if err != nil {
		s.logger.Warn("读取已保存的提醒渠道失败", "err", err)
	}
	_, status := s.userAgent(r.Context(), userID)
	s.renderPage(w, r, i18n.T(lang, "webui.settings.title_tag"), "settings_content", settingsData{
		Providers: agent.Providers, Status: status, Profiles: profiles,
		BrokerKinds: execution.CredentialedKinds, BrokerProfiles: brokerProfiles,
		NotificationChannels: notificationChannels, VAPIDPublicKey: s.vapidPublicKey,
	})
}

func (s *Server) renderSettings(w http.ResponseWriter, r *http.Request, message, errMsg types.Message) {
	userID, _ := currentUserID(r)
	lang := resolveLang(r)
	profiles, err := s.store.ListAgentProfiles(r.Context(), userID)
	if err != nil {
		s.logger.Warn("读取已保存的模型配置失败", "err", err)
	}
	brokerProfiles, err := s.store.ListBrokerProfiles(r.Context(), userID)
	if err != nil {
		s.logger.Warn("读取已保存的交易所配置失败", "err", err)
	}
	notificationChannels, err := s.store.ListNotificationChannels(r.Context(), userID)
	if err != nil {
		s.logger.Warn("读取已保存的提醒渠道失败", "err", err)
	}
	_, status := s.userAgent(r.Context(), userID)
	s.renderPage(w, r, i18n.T(lang, "webui.settings.title_tag"), "settings_content", settingsData{
		Providers: agent.Providers, Status: status, Profiles: profiles,
		BrokerKinds: execution.CredentialedKinds, BrokerProfiles: brokerProfiles,
		NotificationChannels: notificationChannels, VAPIDPublicKey: s.vapidPublicKey,
		Message: message, Error: errMsg,
	})
}

// handleSettingsSave 让用户直接在页面上配置 Agent 翻译层用哪个供应商、哪把 key，
// 不必再改环境变量重启进程。
//
// 多用户 SaaS 改造之后 masterKey 总是存在（cmd/webui/main.go 的生产启动路径没配
// TF_MASTER_KEY 直接拒绝启动，不再有"没有登录密码就退回内存态"这条降级路径了——
// 加密落库是唯一的行为，不是可选项。
func (s *Server) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	lang := resolveLang(r)

	if r.FormValue("action") == "clear" {
		s.setUserAgent(userID, nil, agentStatus{})
		s.renderSettings(w, r, types.Msg("webui.settings.agent.cleared"), types.Message{})
		return
	}

	provider := agent.Provider(strings.TrimSpace(r.FormValue("provider")))
	apiKey := strings.TrimSpace(r.FormValue("api_key"))
	model := strings.TrimSpace(r.FormValue("model"))
	baseURL := strings.TrimSpace(r.FormValue("base_url"))
	label := strings.TrimSpace(r.FormValue("label"))

	if !provider.Valid() {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.agent.unsupported_provider"))
		return
	}
	if apiKey == "" {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.agent.empty_key"))
		return
	}
	if label == "" {
		label = i18n.Render(lang, provider.Label())
	}

	// Timeout/MaxRetries 沿用环境变量里的基准配置（TF_AGENT_TIMEOUT、
	// TF_AGENT_MAX_RETRIES 仍然生效）；BaseURL/APIKey/Model 由 agent.NewLLM
	// 按所选供应商重新决定，不沿用某一家的默认值。
	base := config.Load().Agent
	llm, err := agent.NewLLM(provider, apiKey, model, baseURL, base)
	if err != nil {
		s.setUserAgentError(userID, err.Error())
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.save_failed", "err", err.Error()))
		return
	}

	effectiveModel := model
	if effectiveModel == "" {
		effectiveModel = provider.DefaultModel()
	}
	keyHint := maskAPIKey(apiKey)

	status := agentStatus{Provider: provider, Model: effectiveModel, KeyHint: keyHint}
	message := types.Msg("webui.settings.agent.saved_memory_only")

	ciphertext, salt, nonce, encErr := encryptProfileSecret(s.masterKey, apiKey)
	if encErr != nil {
		s.logger.Warn("加密模型配置失败，本次仅在内存中生效", "err", encErr)
	} else {
		profile := storage.AgentProfile{
			ID: idgen.NewUUID(), UserID: userID, Label: label, Provider: string(provider),
			Model: model, BaseURL: baseURL, KeyHint: keyHint,
			EncryptedAPIKey: ciphertext, KeySalt: salt, KeyNonce: nonce,
		}
		if saveErr := s.store.SaveAgentProfile(r.Context(), profile, true); saveErr != nil {
			s.logger.Warn("保存模型配置到数据库失败，本次仅在内存中生效", "err", saveErr)
		} else {
			status.ProfileID = profile.ID
			message = types.Msg("webui.settings.agent.saved_encrypted")
		}
	}

	s.setUserAgent(userID, agent.New(llm, modules.NewDefaultRegistry(), base.MaxRetries), status)
	s.renderSettings(w, r, message, types.Message{})
}

// handleSettingsActivateProfile 切换到某一份已保存的配置：解密它的 API key、构造 LLM、
// 立即生效，同时在数据库里把它标记成当前生效的一份。
func (s *Server) handleSettingsActivateProfile(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	id := r.PathValue("id")
	profile, err := s.store.GetAgentProfile(r.Context(), userID, id)
	if err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.profile_not_found", "err", err.Error()))
		return
	}
	apiKey, err := decryptProfileSecret(s.masterKey, profile.EncryptedAPIKey, profile.KeySalt, profile.KeyNonce)
	if err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.activate_failed", "err", err.Error()))
		return
	}

	base := config.Load().Agent
	provider := agent.Provider(profile.Provider)
	llm, err := agent.NewLLM(provider, apiKey, profile.Model, profile.BaseURL, base)
	if err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.activate_failed", "err", err.Error()))
		return
	}
	if err := s.store.ActivateAgentProfile(r.Context(), userID, id); err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.activate_failed", "err", err.Error()))
		return
	}

	effectiveModel := profile.Model
	if effectiveModel == "" {
		effectiveModel = provider.DefaultModel()
	}
	s.setUserAgent(userID, agent.New(llm, modules.NewDefaultRegistry(), base.MaxRetries), agentStatus{
		Provider: provider, Model: effectiveModel, KeyHint: profile.KeyHint, ProfileID: profile.ID,
	})
	s.renderSettings(w, r, types.Msg("webui.settings.switched_to", "label", profile.Label), types.Message{})
}

// handleSettingsDeleteProfile 删除一份保存的配置。删除的如果正是当前生效的那份，
// 会同时清掉内存里生效的 Agent——不这样做的话，界面显示"已配置"，但数据库里那份
// 支撑它的配置其实已经被删了，状态会对不上。
func (s *Server) handleSettingsDeleteProfile(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	id := r.PathValue("id")
	_, status := s.userAgent(r.Context(), userID)
	if err := s.store.DeleteAgentProfile(r.Context(), userID, id); err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.delete_failed", "err", err.Error()))
		return
	}
	if status.ProfileID == id {
		s.setUserAgent(userID, nil, agentStatus{})
	}
	s.renderSettings(w, r, types.Msg("webui.settings.deleted"), types.Message{})
}

// loadUserAgentFromDB 尝试从数据库恢复某个用户当前生效的模型配置，供 userAgent
// 在缓存未命中时调用（见 server.go）。找不到已保存的配置、或者解密失败，都不是
// 致命错误：只返回 nil agent，调用方会视情况退回环境变量配置的团队默认值，
// 或者干脆保持"未就绪"，用户可以在设置页面重新配置。
func (s *Server) loadUserAgentFromDB(ctx context.Context, userID string) (*agent.Agent, agentStatus) {
	profile, err := s.store.ActiveAgentProfile(ctx, userID)
	if err != nil {
		return nil, agentStatus{}
	}

	apiKey, err := decryptProfileSecret(s.masterKey, profile.EncryptedAPIKey, profile.KeySalt, profile.KeyNonce)
	if err != nil {
		return nil, agentStatus{LastErr: err.Error()}
	}

	base := config.Load().Agent
	provider := agent.Provider(profile.Provider)
	llm, err := agent.NewLLM(provider, apiKey, profile.Model, profile.BaseURL, base)
	if err != nil {
		return nil, agentStatus{LastErr: err.Error()}
	}

	effectiveModel := profile.Model
	if effectiveModel == "" {
		effectiveModel = provider.DefaultModel()
	}
	return agent.New(llm, modules.NewDefaultRegistry(), base.MaxRetries), agentStatus{
		Provider: provider, Model: effectiveModel, KeyHint: profile.KeyHint, ProfileID: profile.ID,
	}
}
