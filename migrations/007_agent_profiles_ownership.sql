-- agent_profiles 加归属用户：LLM 供应商配置从"全局唯一生效一份"变成"每个用户自己
-- 一份"。key_version 现在恒为 1，是给以后做主密钥轮换预留的口子（增量轮换需要知道
-- 一行密文是用哪一版主密钥加的），这一阶段不需要真的用到它。

ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS user_id UUID;
ALTER TABLE agent_profiles ADD COLUMN IF NOT EXISTS key_version SMALLINT NOT NULL DEFAULT 1;

UPDATE agent_profiles SET user_id = '00000000-0000-0000-0000-000000000001' WHERE user_id IS NULL;

ALTER TABLE agent_profiles ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE agent_profiles ADD CONSTRAINT fk_agent_profiles_user
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;

-- 原来是全局唯一：整张表最多一行 is_active。现在按用户分组：每个用户各自最多一份
-- 生效配置，互不影响。
DROP INDEX IF EXISTS idx_agent_profiles_active;
CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_profiles_active ON agent_profiles (user_id) WHERE is_active;

CREATE INDEX IF NOT EXISTS idx_agent_profiles_user ON agent_profiles (user_id);
