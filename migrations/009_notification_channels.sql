-- 通知渠道：用户可以同时开启任意数量的渠道（同一个 kind 也可以有多条，比如两个
-- webhook、多台设备各自的 web push 订阅），互不排斥——跟 broker_profiles「同一个
-- broker 最多一行 is_active」的语义刻意不同：下单通道同一时刻只能用一份凭据打给
-- 交易所，而提醒渠道没有这种互斥性，用户很可能真的想同时收邮件+Telegram+webhook。
-- 见 internal/storage/notification_channels.go 顶部注释。故意不加任何唯一/部分
-- 唯一索引——不要为了"跟 broker_profiles 保持一致"顺手加上。
--
-- encrypted_config/key_salt/key_nonce 沿用 broker_profiles/agent_profiles 同一套
-- 加密方案（internal/secretcrypto，AES-256-GCM，服务端统一 TF_MASTER_KEY）：
-- 不同 kind 需要的字段不同（email 只要地址，telegram 只要 chat_id，webhook 要
-- URL 和可选的签名 secret，webpush 要 PushSubscription 的
-- endpoint/p256dh/auth），打包成一段 JSON 整体加密，不为每个 kind 各开一组列。
CREATE TABLE IF NOT EXISTS notification_channels (
    id               UUID PRIMARY KEY,
    user_id          UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind             TEXT        NOT NULL CHECK (kind IN ('email', 'telegram', 'webhook', 'webpush')),
    label            TEXT        NOT NULL,
    key_hint         TEXT        NOT NULL,
    encrypted_config BYTEA       NOT NULL,
    key_salt         BYTEA       NOT NULL,
    key_nonce        BYTEA       NOT NULL,
    is_enabled       BOOLEAN     NOT NULL DEFAULT true,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_notification_channels_user ON notification_channels (user_id);
