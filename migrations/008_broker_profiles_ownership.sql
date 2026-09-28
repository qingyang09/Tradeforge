-- broker_profiles 加归属用户：交易所下单通道配置从"每个 broker 全局唯一生效一份"
-- 变成"每个用户的每个 broker 各自一份"。key_version 同 007，给以后主密钥轮换预留。

ALTER TABLE broker_profiles ADD COLUMN IF NOT EXISTS user_id UUID;
ALTER TABLE broker_profiles ADD COLUMN IF NOT EXISTS key_version SMALLINT NOT NULL DEFAULT 1;

UPDATE broker_profiles SET user_id = '00000000-0000-0000-0000-000000000001' WHERE user_id IS NULL;

ALTER TABLE broker_profiles ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE broker_profiles ADD CONSTRAINT fk_broker_profiles_user
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;

-- 原来按 broker 分组：同一个 broker 全局最多一行 is_active。现在按"用户+broker"分组：
-- 不同用户可以各自有一份同一个 broker 的生效配置，互不影响。
DROP INDEX IF EXISTS idx_broker_profiles_active;
CREATE UNIQUE INDEX IF NOT EXISTS idx_broker_profiles_active ON broker_profiles (user_id, broker) WHERE is_active;

CREATE INDEX IF NOT EXISTS idx_broker_profiles_user ON broker_profiles (user_id);
