-- 通知投递审计：每次尝试把一条已触发决策推给某个渠道都记一行，两个目的：
-- 1）合规留痕，跟 decisions/orders 表一样"发生过什么"要能查；
-- 2）幂等去重——Kafka 消费组是 at-least-once 语义（见 internal/messaging.
--    DecisionReader 的注释与 cmd/executor 的既有行为），cmd/notifier 进程崩溃
--    重启后可能重新处理同一条已经消费过的决策；发送前检查这张表里有没有"这个
--    决策 + 这个渠道"的成功记录，避免用户收到重复提醒。
CREATE TABLE IF NOT EXISTS notification_deliveries (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    decision_id UUID        NOT NULL REFERENCES decisions (id) ON DELETE CASCADE,
    channel_id  UUID        NOT NULL REFERENCES notification_channels (id) ON DELETE CASCADE,
    status      TEXT        NOT NULL CHECK (status IN ('sent', 'failed')),
    error       TEXT,
    sent_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 幂等检查的核心索引：同一决策+同一渠道最多应该有一条 status='sent'。不做成唯一
-- 约束（失败重试会插入多条 failed 记录，这是预期行为），只在应用层查询"是否已
-- 存在 sent 记录"来决定要不要跳过。
CREATE INDEX IF NOT EXISTS idx_notification_deliveries_lookup
    ON notification_deliveries (decision_id, channel_id);
