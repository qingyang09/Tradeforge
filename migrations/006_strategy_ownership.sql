-- strategies 加归属用户。多用户 SaaS 改造第一阶段的核心隔离列——从这张表开始，
-- 因为它是其余审计表（orders/decisions/risk_events/backtest_results/
-- strategy_state_transitions）追溯归属的唯一路径：那几张子表故意不各自加一份
-- user_id，全部通过 JOIN strategies 传递判断，避免同一份归属信息散落在六张表里、
-- 以后改起来互相漂移。

ALTER TABLE strategies ADD COLUMN IF NOT EXISTS user_id UUID;

-- 这次迁移之前就存在的策略（本地开发过程中积累的测试数据）没有归属用户，不能悄悄
-- 丢弃——固定一个哨兵用户挂靠它们。这个用户的 password_hash 是一个不合法的占位字节
-- （不是真的 bcrypt 哈希），任何人都不可能用它登录成功，纯粹是外键锚点，不是一个
-- 真实可用的账号。
INSERT INTO users (id, email, password_hash)
VALUES ('00000000-0000-0000-0000-000000000001', 'legacy@tradeforge.local', '\x00'::bytea)
ON CONFLICT (id) DO NOTHING;

UPDATE strategies SET user_id = '00000000-0000-0000-0000-000000000001' WHERE user_id IS NULL;

ALTER TABLE strategies ALTER COLUMN user_id SET NOT NULL;
ALTER TABLE strategies ADD CONSTRAINT fk_strategies_user
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_strategies_user ON strategies (user_id);
