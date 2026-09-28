package webui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"tradeforge/internal/execution"
	"tradeforge/internal/secretcrypto"
	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
)

// brokerCredentials 是打包进 broker_profiles.encrypted_credentials 密文里的明文结构。
// 不同交易所需要的字段数不一样（币安两件套，OKX 三件套 Passphrase 才用得上），
// 打包成一段 JSON 整体加密，不用为每家交易所各开一组密文列（见
// migrations/004_broker_profiles.sql 的注释）。
type brokerCredentials struct {
	APIKey     string `json:"api_key"`
	APISecret  string `json:"api_secret"`
	Passphrase string `json:"passphrase,omitempty"`
}

// handleSettingsSaveBroker 让用户直接在设置页面配置交易所下单通道的凭据，不用再
// 靠环境变量传给 cmd/executor。逻辑跟 handleSettingsSave（LLM 配置）是同一个模式：
// 先用 execution.NewBroker 校验凭据能不能构造出对应的 Broker（复用它已有的必填项
// 检查，不重新发明一套校验规则），校验通过后用服务端主密钥加密落库，归属当前
// 登录用户——不同用户各自的凭据完全隔离（见 broker_profiles 表 user_id 列）。
//
// 这里存的只是凭据本身，不会立即让某个正在运行的 cmd/executor 进程生效——那是一个
// 独立的进程，设置页面负责的是"把凭据准备好、加密存起来"，cmd/executor 启动时自己
// 去读当前用户当前生效的一份（见 cmd/executor/main.go 的 buildBroker，-owner-email
// 决定"当前用户"是谁）。
func (s *Server) handleSettingsSaveBroker(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	kind := execution.BrokerKind(strings.TrimSpace(r.FormValue("broker")))
	apiKey := strings.TrimSpace(r.FormValue("api_key"))
	apiSecret := strings.TrimSpace(r.FormValue("api_secret"))
	passphrase := strings.TrimSpace(r.FormValue("passphrase"))
	label := strings.TrimSpace(r.FormValue("label"))

	if !kind.Valid() || kind == execution.BrokerKindPaper {
		s.renderSettings(w, r, "", "不支持的下单通道。")
		return
	}
	if label == "" {
		label = kind.Label()
	}

	if _, err := execution.NewBroker(kind, apiKey, apiSecret, passphrase); err != nil {
		s.renderSettings(w, r, "", "保存失败："+err.Error())
		return
	}

	creds := brokerCredentials{APIKey: apiKey, APISecret: apiSecret, Passphrase: passphrase}
	plaintext, err := json.Marshal(creds)
	if err != nil {
		s.renderSettings(w, r, "", "保存失败："+err.Error())
		return
	}
	ciphertext, salt, nonce, err := secretcrypto.Encrypt(s.masterKey, string(plaintext))
	if err != nil {
		s.renderSettings(w, r, "", "加密失败："+err.Error())
		return
	}

	profile := storage.BrokerProfile{
		ID: idgen.NewUUID(), UserID: userID, Label: label, Broker: string(kind),
		KeyHint:              secretcrypto.MaskAPIKey(apiKey),
		EncryptedCredentials: ciphertext, KeySalt: salt, KeyNonce: nonce,
	}
	if err := s.store.SaveBrokerProfile(r.Context(), profile, true); err != nil {
		s.renderSettings(w, r, "", "保存到数据库失败："+err.Error())
		return
	}
	s.renderSettings(w, r, fmt.Sprintf("已保存并启用「%s」，下次启动 cmd/executor -broker %s 时会自动读取。", label, kind), "")
}

// handleSettingsActivateBrokerProfile 把某一份已保存的交易所配置切换成它所属 broker
// 当前生效的一份——只是切数据库里的标记，不解密（不需要，也不该在设置页面把交易所
// secret 解出来展示或使用）。
func (s *Server) handleSettingsActivateBrokerProfile(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	id := r.PathValue("id")
	profile, err := s.store.GetBrokerProfile(r.Context(), userID, id)
	if err != nil {
		s.renderSettings(w, r, "", "找不到这份配置："+err.Error())
		return
	}
	if err := s.store.ActivateBrokerProfile(r.Context(), userID, id); err != nil {
		s.renderSettings(w, r, "", "启用失败："+err.Error())
		return
	}
	s.renderSettings(w, r, "已切换到「"+profile.Label+"」。", "")
}

// handleSettingsDeleteBrokerProfile 删除一份保存的交易所配置。
func (s *Server) handleSettingsDeleteBrokerProfile(w http.ResponseWriter, r *http.Request) {
	userID, _ := currentUserID(r)
	id := r.PathValue("id")
	if err := s.store.DeleteBrokerProfile(r.Context(), userID, id); err != nil {
		s.renderSettings(w, r, "", "删除失败："+err.Error())
		return
	}
	s.renderSettings(w, r, "已删除。", "")
}
