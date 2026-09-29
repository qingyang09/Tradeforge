package webui

import (
	"encoding/json"
	"net/http"
	"strings"

	"tradeforge/internal/i18n"
	"tradeforge/internal/notify"
	"tradeforge/internal/secretcrypto"
	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// handleSettingsSaveNotificationChannel 保存一条新的提醒渠道配置。跟
// handleSettingsSaveBroker 是同一种"一个 kind 参数驱动分支"的形状，因为不同 kind
// 需要的字段完全不同（email 只要地址，telegram 只要 chat_id，webhook 要 URL +
// 可选签名 secret）——校验通过后用服务端主密钥加密落库，归属当前登录用户。
func (s *Server) handleSettingsSaveNotificationChannel(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	kind := strings.TrimSpace(r.FormValue("kind"))
	label := strings.TrimSpace(r.FormValue("label"))

	var plaintext []byte
	var hint string
	switch kind {
	case "email":
		addr := strings.TrimSpace(r.FormValue("address"))
		if addr == "" {
			s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.notif.empty_address"))
			return
		}
		var err error
		plaintext, err = json.Marshal(notify.EmailConfig{Address: addr})
		if err != nil {
			s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.save_failed", "err", err.Error()))
			return
		}
		hint = addr
	case "telegram":
		chatID := strings.TrimSpace(r.FormValue("chat_id"))
		if chatID == "" {
			s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.notif.empty_chatid"))
			return
		}
		var err error
		plaintext, err = json.Marshal(notify.TelegramConfig{ChatID: chatID})
		if err != nil {
			s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.save_failed", "err", err.Error()))
			return
		}
		hint = chatID
	case "webhook":
		url := strings.TrimSpace(r.FormValue("url"))
		if !strings.HasPrefix(url, "https://") {
			s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.notif.webhook_needs_https"))
			return
		}
		secret := strings.TrimSpace(r.FormValue("secret"))
		var err error
		plaintext, err = json.Marshal(notify.WebhookConfig{URL: url, Secret: secret})
		if err != nil {
			s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.save_failed", "err", err.Error()))
			return
		}
		hint = url
	default:
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.notif.unsupported_kind"))
		return
	}
	if label == "" {
		label = kind
	}

	ciphertext, salt, nonce, err := secretcrypto.Encrypt(s.masterKey, string(plaintext))
	if err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.encrypt_failed", "err", err.Error()))
		return
	}

	ch := storage.NotificationChannel{
		ID: idgen.NewUUID(), UserID: userID, Kind: kind, Label: label,
		KeyHint:         secretcrypto.MaskAPIKey(hint),
		EncryptedConfig: ciphertext, KeySalt: salt, KeyNonce: nonce, IsEnabled: true,
	}
	if err := s.store.SaveNotificationChannel(r.Context(), ch); err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.db_save_failed", "err", err.Error()))
		return
	}
	s.renderSettings(w, r, types.Msg("webui.settings.notif.saved", "label", label), types.Message{})
}

// handleSettingsWebPushSubscribe 接收浏览器 PushManager.subscribe() 产出的订阅，
// 存成一条 kind="webpush" 的渠道。跟其它三个渠道不同，这个端点不是表单 POST，
// 是页面里一小段原生 JS 发起的 fetch() JSON POST（见 settings.html 里"浏览器推送"
// 卡片的 <script>）——Web Push 订阅是浏览器 API，htmx 覆盖不了，是这个应用里
// 唯一需要原生 JS 的地方。返回 204，不返回 HTML 片段。
func (s *Server) handleSettingsWebPushSubscribe(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	lang := resolveLang(r)
	var sub notify.PushSubscription
	if err := json.NewDecoder(r.Body).Decode(&sub); err != nil {
		http.Error(w, i18n.T(lang, "webui.settings.webpush.invalid_subscription"), http.StatusBadRequest)
		return
	}
	if sub.Endpoint == "" || sub.P256dh == "" || sub.Auth == "" {
		http.Error(w, i18n.T(lang, "webui.settings.webpush.missing_fields"), http.StatusBadRequest)
		return
	}

	plaintext, err := json.Marshal(sub)
	if err != nil {
		http.Error(w, i18n.T(lang, "webui.settings.webpush.serialize_failed"), http.StatusInternalServerError)
		return
	}
	ciphertext, salt, nonce, err := secretcrypto.Encrypt(s.masterKey, string(plaintext))
	if err != nil {
		http.Error(w, i18n.T(lang, "webui.settings.webpush.encrypt_failed"), http.StatusInternalServerError)
		return
	}

	// Label/KeyHint 是给"已配置的提醒渠道"表格展示用的固定文案——这条记录代表
	// "此设备通过浏览器推送订阅"这一个具体的渠道条目，不是页面静态文案，是随这次
	// 订阅一起落库的数据；storage.NotificationChannel.Label/KeyHint 目前是纯 string
	// 列（storage 包不在这次改造范围内），还没有 types.Message 化，所以这里暂时
	// 保持中文——跟下面 Kind/Label 一起落库的其它渠道的默认 label（kind 本身）是
	// 同一类"存进数据库的数据"，不是本次要转换的页面静态文案。
	ch := storage.NotificationChannel{
		ID: idgen.NewUUID(), UserID: userID, Kind: "webpush", Label: "浏览器推送（此设备）",
		KeyHint:         "（此设备）",
		EncryptedConfig: ciphertext, KeySalt: salt, KeyNonce: nonce, IsEnabled: true,
	}
	if err := s.store.SaveNotificationChannel(r.Context(), ch); err != nil {
		http.Error(w, i18n.T(lang, "webui.settings.webpush.save_failed"), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSettingsToggleNotificationChannel 切换一条渠道的启停状态。
func (s *Server) handleSettingsToggleNotificationChannel(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	id := r.PathValue("id")
	ch, err := s.store.GetNotificationChannel(r.Context(), userID, id)
	if err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.channel_not_found", "err", err.Error()))
		return
	}
	if err := s.store.SetNotificationChannelEnabled(r.Context(), userID, id, !ch.IsEnabled); err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.toggle_failed", "err", err.Error()))
		return
	}
	message := types.Msg("webui.settings.notif.enabled_msg", "label", ch.Label)
	if ch.IsEnabled {
		message = types.Msg("webui.settings.notif.disabled_msg", "label", ch.Label)
	}
	s.renderSettings(w, r, message, types.Message{})
}

// handleSettingsDeleteNotificationChannel 删除一条保存的渠道配置。
func (s *Server) handleSettingsDeleteNotificationChannel(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	id := r.PathValue("id")
	if err := s.store.DeleteNotificationChannel(r.Context(), userID, id); err != nil {
		s.renderSettings(w, r, types.Message{}, types.Msg("webui.settings.error.delete_failed", "err", err.Error()))
		return
	}
	s.renderSettings(w, r, types.Msg("webui.settings.deleted"), types.Message{})
}
