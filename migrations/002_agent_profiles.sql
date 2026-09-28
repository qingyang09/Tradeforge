-- LLM 供应商配置：可以保存多份（比如 Anthropic 一份、OpenAI 一份），设置页面用下拉
-- 切换哪一份当前生效，不用每次重启进程都重新填一遍 key。
--
-- api_key 从不明文落库：encrypted_api_key 是用管理员登录密码派生出的密钥
-- （scrypt + AES-256-GCM）加密后的密文，key_salt 是这次派生用的盐，key_nonce 是
-- GCM 用的随机数——三者缺一都解不出明文。管理员密码后来改了的话，已保存的 key
-- 就再也解不出来，需要重新填一次；这是刻意的权衡：不引入另一个"主密码"概念，
-- 复用已有的登录密码当加密材料（见 internal/webui/profilecrypto.go）。
--
-- key_hint 是掩码后的展示值（如 "sk-a…b12d"），跟设置页面 maskAPIKey() 用的是
-- 同一份数据，保存时算好存起来，列表页不需要解密就能显示"当前配的是哪把 key"。
CREATE TABLE IF NOT EXISTS agent_profiles (
    id                UUID PRIMARY KEY,
    label             TEXT        NOT NULL,
    provider          TEXT        NOT NULL,
    model             TEXT        NOT NULL DEFAULT '',
    base_url          TEXT        NOT NULL DEFAULT '',
    key_hint          TEXT        NOT NULL,
    encrypted_api_key BYTEA       NOT NULL,
    key_salt          BYTEA       NOT NULL,
    key_nonce         BYTEA       NOT NULL,
    is_active         BOOLEAN     NOT NULL DEFAULT false,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 部分唯一索引：同一时刻最多一行 is_active = true，"当前生效的是哪一份"不会有歧义。
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_profiles_active ON agent_profiles (is_active) WHERE is_active;
