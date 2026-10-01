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

// settingsData is the settings page's render data.
type settingsData struct {
	Providers []agent.Provider
	Status    agentStatus
	// Profiles are the current user's already-encrypted, saved model
	// configurations, for switching/deleting.
	Profiles []storage.AgentProfile

	// BrokerKinds are the options for the settings page's "add exchange
	// configuration" dropdown (excludes paper, see execution.CredentialedKinds's
	// doc comment). BrokerProfiles are the current user's already-encrypted,
	// saved exchange credentials, for switching/deleting -- the same
	// relationship Profiles has to LLM configuration, fully symmetric.
	BrokerKinds    []execution.BrokerKind
	BrokerProfiles []storage.BrokerProfile

	// NotificationChannels are the current user's saved notification
	// channels, for the settings page to display/toggle/delete -- similar to
	// BrokerProfiles' relationship to BrokerKinds, but with no "currently
	// active one" concept -- see storage.NotificationChannel's doc comment.
	// VAPIDPublicKey is the public key the "browser push" card's native JS
	// uses to call PushManager.subscribe(); it isn't a secret.
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
		s.logger.Warn("failed to read saved model configuration", "err", err)
	}
	brokerProfiles, err := s.store.ListBrokerProfiles(r.Context(), userID)
	if err != nil {
		s.logger.Warn("failed to read saved exchange configuration", "err", err)
	}
	notificationChannels, err := s.store.ListNotificationChannels(r.Context(), userID)
	if err != nil {
		s.logger.Warn("failed to read saved notification channels", "err", err)
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
		s.logger.Warn("failed to read saved model configuration", "err", err)
	}
	brokerProfiles, err := s.store.ListBrokerProfiles(r.Context(), userID)
	if err != nil {
		s.logger.Warn("failed to read saved exchange configuration", "err", err)
	}
	notificationChannels, err := s.store.ListNotificationChannels(r.Context(), userID)
	if err != nil {
		s.logger.Warn("failed to read saved notification channels", "err", err)
	}
	_, status := s.userAgent(r.Context(), userID)
	s.renderPage(w, r, i18n.T(lang, "webui.settings.title_tag"), "settings_content", settingsData{
		Providers: agent.Providers, Status: status, Profiles: profiles,
		BrokerKinds: execution.CredentialedKinds, BrokerProfiles: brokerProfiles,
		NotificationChannels: notificationChannels, VAPIDPublicKey: s.vapidPublicKey,
		Message: message, Error: errMsg,
	})
}

// handleSettingsSave lets the user configure which provider/key the Agent
// translation layer uses directly on the page, without having to change an
// environment variable and restart the process.
//
// After the multi-tenant SaaS rework, masterKey always exists (the
// production startup path in cmd/webui/main.go refuses to start without
// TF_MASTER_KEY configured, so there's no longer a "fall back to in-memory
// only" degraded path for a missing login password) -- encrypted persistence
// is the only behavior now, not an option.
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

	// Timeout/MaxRetries keep using the baseline configuration from
	// environment variables (TF_AGENT_TIMEOUT/TF_AGENT_MAX_RETRIES still take
	// effect); BaseURL/APIKey/Model are redecided by agent.NewLLM based on
	// the selected provider, not carried over from any particular provider's
	// defaults.
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
		s.logger.Warn("failed to encrypt the model configuration; this one only takes effect in memory", "err", encErr)
	} else {
		profile := storage.AgentProfile{
			ID: idgen.NewUUID(), UserID: userID, Label: label, Provider: string(provider),
			Model: model, BaseURL: baseURL, KeyHint: keyHint,
			EncryptedAPIKey: ciphertext, KeySalt: salt, KeyNonce: nonce,
		}
		if saveErr := s.store.SaveAgentProfile(r.Context(), profile, true); saveErr != nil {
			s.logger.Warn("failed to save the model configuration to the database; this one only takes effect in memory", "err", saveErr)
		} else {
			status.ProfileID = profile.ID
			message = types.Msg("webui.settings.agent.saved_encrypted")
		}
	}

	s.setUserAgent(userID, agent.New(llm, modules.NewDefaultRegistry(), base.MaxRetries), status)
	s.renderSettings(w, r, message, types.Message{})
}

// handleSettingsActivateProfile switches to one of the saved configurations:
// decrypts its API key, constructs the LLM, makes it effective immediately,
// and marks it as the currently-active one in the database.
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

// handleSettingsDeleteProfile deletes a saved configuration. If the one being
// deleted is the currently-active one, it also clears the in-memory
// effective Agent -- otherwise the interface would still show "configured"
// while the database row backing it had already been deleted, leaving the
// displayed state out of sync with reality.
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

// loadUserAgentFromDB tries to restore a given user's currently-effective
// model configuration from the database, called by userAgent on a cache miss
// (see server.go). Finding no saved configuration, or a decryption failure,
// are both non-fatal: it just returns a nil agent, and the caller falls back
// to the team default configured via an environment variable as
// appropriate, or simply stays "not ready" -- the user can reconfigure on
// the settings page.
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
