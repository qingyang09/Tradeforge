-- 用户账户：多用户 SaaS 改造第一阶段。邮箱+密码，密码用 bcrypt 哈希（登录口令的哈希
-- 算法），不要跟 internal/secretcrypto 用 scrypt 派生 AES 密钥的那套混为一谈——
-- 两者解决的是完全不同的问题（前者是"这是不是这个人"，后者是"用什么密钥加密存起来的
-- 交易所/LLM 凭据"）。
CREATE TABLE IF NOT EXISTS users (
    id            UUID PRIMARY KEY,
    email         TEXT        NOT NULL,
    password_hash BYTEA       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 邮箱唯一性大小写不敏感：不能允许 A@x.com 和 a@x.com 注册成两个账号。
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_lower ON users (lower(email));
