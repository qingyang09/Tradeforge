-- TradeForge 初始 schema。
-- 该文件被 docker-compose 挂载到 postgres 的 /docker-entrypoint-initdb.d，
-- 首次启动空数据卷时自动执行。

-- 策略配置。config 存完整的 StrategyConfig JSON，顶层列只提取查询要用的字段。
CREATE TABLE IF NOT EXISTS strategies (
    id               UUID PRIMARY KEY,
    name             TEXT        NOT NULL,
    symbol           TEXT        NOT NULL,
    timeframe        TEXT        NOT NULL,
    state            TEXT        NOT NULL,
    config           JSONB       NOT NULL,
    source_utterance TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_strategies_symbol ON strategies (symbol);
CREATE INDEX IF NOT EXISTS idx_strategies_state  ON strategies (state);

-- 状态变更审计。每次流转都记录：谁、什么时候、基于哪次回测/模拟盘数据推进的。
-- 只增不改：这张表是合规留痕，任何 UPDATE/DELETE 都应视为异常。
CREATE TABLE IF NOT EXISTS strategy_state_transitions (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    strategy_id UUID        NOT NULL REFERENCES strategies (id) ON DELETE CASCADE,
    from_state  TEXT        NOT NULL,
    to_state    TEXT        NOT NULL,
    actor       TEXT        NOT NULL,
    reason      TEXT        NOT NULL,
    evidence    JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_transitions_strategy ON strategy_state_transitions (strategy_id, created_at DESC);

-- 组合引擎的决策留痕。signals 保存参与决策的全部模块信号，供可解释性展示。
CREATE TABLE IF NOT EXISTS decisions (
    id           UUID PRIMARY KEY,
    strategy_id  UUID        NOT NULL REFERENCES strategies (id) ON DELETE CASCADE,
    symbol       TEXT        NOT NULL,
    direction    TEXT        NOT NULL,
    score        DOUBLE PRECISION NOT NULL,
    triggered    BOOLEAN     NOT NULL,
    reason       TEXT        NOT NULL,
    price        NUMERIC(38, 18) NOT NULL,
    signals      JSONB       NOT NULL,
    bar_time     TIMESTAMPTZ NOT NULL,
    evaluated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_decisions_strategy ON decisions (strategy_id, bar_time DESC);
CREATE INDEX IF NOT EXISTS idx_decisions_triggered ON decisions (strategy_id, triggered) WHERE triggered;

-- 回测结果。样本内/样本外指标分列保存，展示层不得混用。
CREATE TABLE IF NOT EXISTS backtest_results (
    id              UUID PRIMARY KEY,
    strategy_id     UUID        NOT NULL REFERENCES strategies (id) ON DELETE CASCADE,
    symbol          TEXT        NOT NULL,
    overall         JSONB       NOT NULL,
    in_sample       JSONB       NOT NULL,
    out_of_sample   JSONB       NOT NULL,
    segments        JSONB       NOT NULL,
    fee_model       JSONB       NOT NULL,
    trades          JSONB,
    initial_capital NUMERIC(38, 18) NOT NULL,
    data_start      TIMESTAMPTZ NOT NULL,
    data_end        TIMESTAMPTZ NOT NULL,
    engine_version  TEXT        NOT NULL,
    ran_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_backtests_strategy ON backtest_results (strategy_id, ran_at DESC);

-- 订单记录。provenance 是强制字段：每笔单都要能回答"哪个模块的哪个信号触发的"。
-- mode 区分 PAPER / LIVE，模拟盘订单绝不能被误认为实盘成交。
CREATE TABLE IF NOT EXISTS orders (
    id                UUID PRIMARY KEY,
    strategy_id       UUID        NOT NULL REFERENCES strategies (id) ON DELETE CASCADE,
    symbol            TEXT        NOT NULL,
    side              TEXT        NOT NULL,
    type              TEXT        NOT NULL,
    mode              TEXT        NOT NULL CHECK (mode IN ('PAPER', 'LIVE')),
    quantity          NUMERIC(38, 18) NOT NULL,
    price             NUMERIC(38, 18),
    filled_price      NUMERIC(38, 18),
    fee               NUMERIC(38, 18),
    status            TEXT        NOT NULL,
    exchange_order_id TEXT,
    reject_reason     TEXT,
    provenance        JSONB       NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    filled_at         TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_orders_strategy ON orders (strategy_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_orders_symbol_mode ON orders (symbol, mode, created_at DESC);

-- 风控事件。触发风控导致的暂停/强平都记在这里，按标的隔离排查。
CREATE TABLE IF NOT EXISTS risk_events (
    id          BIGINT      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    strategy_id UUID        NOT NULL REFERENCES strategies (id) ON DELETE CASCADE,
    symbol      TEXT        NOT NULL,
    rule        TEXT        NOT NULL,
    detail      JSONB       NOT NULL,
    action      TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_risk_events_strategy ON risk_events (strategy_id, created_at DESC);
