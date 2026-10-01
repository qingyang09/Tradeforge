package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"tradeforge/internal/execution"
	"tradeforge/internal/i18n"
	"tradeforge/internal/secretcrypto"
	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// brokerCredentials is the plaintext structure packed into
// broker_profiles.encrypted_credentials' ciphertext. Different exchanges need
// different numbers of fields (Binance needs two, OKX needs three including
// Passphrase) -- packed as one JSON blob and encrypted as a whole, rather
// than opening a separate ciphertext column per exchange (see
// migrations/004_broker_profiles.sql's doc comment).
type brokerCredentials struct {
	APIKey     string `json:"api_key"`
	APISecret  string `json:"api_secret"`
	Passphrase string `json:"passphrase,omitempty"`
}

// handleSettingsSaveBroker lets the user configure exchange order-routing
// credentials directly on the settings page, instead of relying on
// environment variables passed to cmd/executor. The logic follows the same
// pattern as handleSettingsSave (LLM configuration): first validate the
// credentials by letting execution.NewBroker try to construct the
// corresponding Broker (reusing its existing required-field checks rather
// than reinventing a validation ruleset), then on success encrypt with the
// server-side master key and persist, owned by the currently logged-in user
// -- each user's credentials are fully isolated from every other user's (see
// the broker_profiles table's user_id column).
//
// Saving here only stores the credentials -- it doesn't immediately take
// effect in any already-running cmd/executor process, which is a separate
// process; the settings page's job is "prepare and encrypt-store the
// credentials," and cmd/executor reads the currently-active one for the
// current user itself at startup (see cmd/executor/main.go's buildBroker,
// where -owner-email decides who "the current user" is).
func (s *Server) handleSettingsSaveBroker(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	lang := resolveLang(r)
	kind := execution.BrokerKind(strings.TrimSpace(r.FormValue("broker")))
	apiKey := strings.TrimSpace(r.FormValue("api_key"))
	apiSecret := strings.TrimSpace(r.FormValue("api_secret"))
	passphrase := strings.TrimSpace(r.FormValue("passphrase"))
	label := strings.TrimSpace(r.FormValue("label"))

	if !kind.Valid() || kind == execution.BrokerKindPaper {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.broker.unsupported_kind"))
		return
	}
	if label == "" {
		label = i18n.Render(lang, kind.Label())
	}

	if _, err := execution.NewBroker(kind, apiKey, apiSecret, passphrase); err != nil {
		reason := err.Error()
		var cfgErr *execution.BrokerConfigError
		if errors.As(err, &cfgErr) {
			reason = i18n.Render(lang, cfgErr.Reason)
		}
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.save_failed", "err", reason))
		return
	}

	creds := brokerCredentials{APIKey: apiKey, APISecret: apiSecret, Passphrase: passphrase}
	plaintext, err := json.Marshal(creds)
	if err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.save_failed", "err", err.Error()))
		return
	}
	ciphertext, salt, nonce, err := secretcrypto.Encrypt(s.masterKey, string(plaintext))
	if err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.encrypt_failed", "err", err.Error()))
		return
	}

	profile := storage.BrokerProfile{
		ID: idgen.NewUUID(), UserID: userID, Label: label, Broker: string(kind),
		KeyHint:              secretcrypto.MaskAPIKey(apiKey),
		EncryptedCredentials: ciphertext, KeySalt: salt, KeyNonce: nonce,
	}
	if err := s.store.SaveBrokerProfile(r.Context(), profile, true); err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.db_save_failed", "err", err.Error()))
		return
	}
	s.renderSettings(w, r, types.Msg("webui.settings.broker.saved", "label", label, "broker", string(kind)), types.Message{})
}

// handleSettingsActivateBrokerProfile switches one saved exchange
// configuration to be the active one for its broker -- this just flips a
// marker in the database, it never decrypts anything (there's no need to,
// and the settings page should never decrypt and display or use an exchange
// secret).
func (s *Server) handleSettingsActivateBrokerProfile(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	id := r.PathValue("id")
	profile, err := s.store.GetBrokerProfile(r.Context(), userID, id)
	if err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.profile_not_found", "err", err.Error()))
		return
	}
	if err := s.store.ActivateBrokerProfile(r.Context(), userID, id); err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.activate_failed", "err", err.Error()))
		return
	}
	s.renderSettings(w, r, types.Msg("webui.settings.switched_to", "label", profile.Label), types.Message{})
}

// handleSettingsDeleteBrokerProfile deletes a saved exchange configuration.
func (s *Server) handleSettingsDeleteBrokerProfile(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	id := r.PathValue("id")
	if err := s.store.DeleteBrokerProfile(r.Context(), userID, id); err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.delete_failed", "err", err.Error()))
		return
	}
	s.renderSettings(w, r, types.Msg("webui.settings.deleted"), types.Message{})
}
