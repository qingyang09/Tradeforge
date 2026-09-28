-- 交易所下单通道配置：可以给每个下单通道（binance-testnet / okx-demo / ...）各保存
-- 一份或多份凭据，设置页面用来切换/启用/删除，cmd/executor 启动时按 -broker 选中的
-- 通道去读取"当前生效"的那一份，不用每次都靠环境变量传 key。
--
-- 跟 agent_profiles 的设计是同一套思路（见 002_agent_profiles.sql 的注释），
-- 复用同一套加密方案（internal/secretcrypto，管理员登录密码派生密钥、AES-256-GCM）：
-- encrypted_credentials 不是单个 API key，而是把该通道需要的凭据（api_key、
-- api_secret、可能还有 passphrase）打包成一段 JSON 后整体加密——不同交易所需要
-- 的凭据字段数量不一样（币安两件套，OKX 三件套），拆成定长的多列会随着以后接入
-- 更多交易所越来越难维护，不如统一存一段结构化密文。
--
-- 一个下单通道（broker 列相同）内最多一行 is_active = true，但不同通道可以各自
-- 有一份当前生效的配置同时存在——比如可以同时保存一份生效的 Binance 配置和一份
-- 生效的 OKX 配置，分别启动两个 cmd/executor 进程各接一个通道。
CREATE TABLE IF NOT EXISTS broker_profiles (
    id                     UUID PRIMARY KEY,
    label                  TEXT        NOT NULL,
    broker                 TEXT        NOT NULL,
    key_hint               TEXT        NOT NULL,
    encrypted_credentials  BYTEA       NOT NULL,
    key_salt               BYTEA       NOT NULL,
    key_nonce              BYTEA       NOT NULL,
    is_active              BOOLEAN     NOT NULL DEFAULT false,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 部分唯一索引按 broker 分组：同一个 broker 最多一行 is_active，不同 broker 互不影响。
CREATE UNIQUE INDEX IF NOT EXISTS idx_broker_profiles_active ON broker_profiles (broker) WHERE is_active;
