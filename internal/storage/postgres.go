// Package storage 提供 Postgres 数据访问层。
//
// 约定：所有金额/价格列在库里都是 NUMERIC，读写时用 decimal.Decimal 的
// 字符串形式传递，绝不经过 float64——那一步转换会悄悄丢精度。
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"tradeforge/internal/config"
	"tradeforge/pkg/types"
)

// ErrNotFound 表示按主键未查到记录。
var ErrNotFound = errors.New("记录不存在")

// Store 是 Postgres 数据访问入口。
type Store struct {
	pool *pgxpool.Pool
}

// Open 建立连接池并验证连通性。
func Open(ctx context.Context, cfg config.PostgresConfig) (*Store, error) {
	pool, err := pgxpool.New(ctx, cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("创建连接池失败：%w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("连接 Postgres 失败（%s:%d）：%w", cfg.Host, cfg.Port, err)
	}
	return &Store{pool: pool}, nil
}

// NewStore 用已有连接池构造 Store，便于测试注入。
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool 暴露底层连接池，供需要自定义查询的场景使用。
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close 关闭连接池。
func (s *Store) Close() { s.pool.Close() }

// ---------- 策略 ----------

// SaveStrategy 插入或更新一条策略配置。userID 必须跟已存在的同 id 行一致才允许更新
// （见 ON CONFLICT 分支的 WHERE 子句）——不这样做的话，一个伪造/重放的请求带着别人
// 策略的 id 就能直接覆盖别人的策略内容，不只是"看到"，是能"改写"，必须在这一层堵住，
// 不能指望上层业务逻辑总是先做归属校验。id 已存在但不属于当前用户时返回 ErrNotFound——
// 正常流程里 id 都是服务端生成的新 UUID，不会真的撞到这个分支，只有伪造请求会撞上。
func (s *Store) SaveStrategy(ctx context.Context, cfg types.StrategyConfig) error {
	blob, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("序列化策略配置失败：%w", err)
	}
	const q = `
		INSERT INTO strategies (id, user_id, name, symbol, timeframe, state, config, source_utterance, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now(), now())
		ON CONFLICT (id) DO UPDATE SET
			name = EXCLUDED.name,
			symbol = EXCLUDED.symbol,
			timeframe = EXCLUDED.timeframe,
			state = EXCLUDED.state,
			config = EXCLUDED.config,
			source_utterance = EXCLUDED.source_utterance,
			updated_at = now()
		WHERE strategies.user_id = EXCLUDED.user_id`
	tag, err := s.pool.Exec(ctx, q,
		cfg.ID, cfg.UserID, cfg.Name, cfg.Symbol, string(cfg.Timeframe), string(cfg.State), blob, cfg.SourceUtterance)
	if err != nil {
		return fmt.Errorf("保存策略 %s 失败：%w", cfg.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("策略 %s：%w", cfg.ID, ErrNotFound)
	}
	return nil
}

// GetStrategy 按 ID 读取策略配置。userID 不匹配（策略存在但不是当前用户的）跟策略
// 根本不存在一样返回 ErrNotFound。
func (s *Store) GetStrategy(ctx context.Context, userID, id string) (types.StrategyConfig, error) {
	var blob []byte
	var uid, state string
	var createdAt, updatedAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT config, user_id, state, created_at, updated_at FROM strategies WHERE id = $1 AND user_id = $2`, id, userID).
		Scan(&blob, &uid, &state, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.StrategyConfig{}, fmt.Errorf("策略 %s：%w", id, ErrNotFound)
	}
	if err != nil {
		return types.StrategyConfig{}, fmt.Errorf("读取策略 %s 失败：%w", id, err)
	}
	var cfg types.StrategyConfig
	if err := json.Unmarshal(blob, &cfg); err != nil {
		return types.StrategyConfig{}, fmt.Errorf("反序列化策略 %s 失败：%w", id, err)
	}
	// 状态、归属用户、创建/更新时间都以独立列为准：这几列由数据库自己维护，config 里嵌
	// 的这几个字段只是调用方传 SaveStrategy 时随手带的一份快照，可能是零值或过期值——
	// 之前只覆盖了 State 一个字段，CreatedAt/UpdatedAt 漏了，导致界面上真实出现过
	// "0001-01-01" 这种从未被写过的零值时间；UserID 同理不能信任 JSONB 里可能存的旧值。
	cfg.UserID = uid
	cfg.State = types.StrategyState(state)
	cfg.CreatedAt = createdAt
	cfg.UpdatedAt = updatedAt
	return cfg, nil
}

// DeleteStrategy 删除一条策略及其全部关联数据（回测结果/订单/决策/状态流转记录，
// 靠 schema 里的 ON DELETE CASCADE 级联删除，这里不需要手动逐张表清理）。
//
// 这一层不做"能不能删"的业务判断（比如只允许删 DRAFT 状态）——那是调用方
// （internal/webui 的 handler）的职责，这里只管机械执行删除，跟 SaveStrategy
// 不做状态机合法性校验是同一个分工原则。
func (s *Store) DeleteStrategy(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM strategies WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("删除策略 %s 失败：%w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("策略 %s：%w", id, ErrNotFound)
	}
	return nil
}

// ListStrategies 列出该用户全部策略，按创建时间正序。看板要展示状态机总览需要完整
// 列表，不能只按单个状态过滤。
func (s *Store) ListStrategies(ctx context.Context, userID string) ([]types.StrategyConfig, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT config, user_id, state, created_at, updated_at FROM strategies WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("查询全部策略失败：%w", err)
	}
	defer rows.Close()

	var out []types.StrategyConfig
	for rows.Next() {
		var blob []byte
		var uid, st string
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&blob, &uid, &st, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		var cfg types.StrategyConfig
		if err := json.Unmarshal(blob, &cfg); err != nil {
			return nil, fmt.Errorf("反序列化策略失败：%w", err)
		}
		// 见 GetStrategy 的注释：这几列以数据库为准，config 里嵌的副本可能是零值/过期值。
		cfg.UserID = uid
		cfg.State = types.StrategyState(st)
		cfg.CreatedAt = createdAt
		cfg.UpdatedAt = updatedAt
		out = append(out, cfg)
	}
	return out, rows.Err()
}

// ListStrategiesByState 列出该用户名下处于指定状态的全部策略——userID 是
// cmd/executor/cmd/signal-engine 的 -owner-email 解析出来的，划定"这个进程只服务
// 这一个用户的策略、只用这一个用户的凭据"这条边界，见这两个命令 main.go 的注释。
func (s *Store) ListStrategiesByState(ctx context.Context, userID string, state types.StrategyState) ([]types.StrategyConfig, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT config, user_id, state, created_at, updated_at FROM strategies WHERE user_id = $1 AND state = $2 ORDER BY created_at`,
		userID, string(state))
	if err != nil {
		return nil, fmt.Errorf("按状态查询策略失败：%w", err)
	}
	defer rows.Close()

	var out []types.StrategyConfig
	for rows.Next() {
		var blob []byte
		var uid, st string
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&blob, &uid, &st, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		var cfg types.StrategyConfig
		if err := json.Unmarshal(blob, &cfg); err != nil {
			return nil, fmt.Errorf("反序列化策略失败：%w", err)
		}
		cfg.UserID = uid
		cfg.State = types.StrategyState(st)
		cfg.CreatedAt = createdAt
		cfg.UpdatedAt = updatedAt
		out = append(out, cfg)
	}
	return out, rows.Err()
}

// ListStrategiesByStateAllUsers 跨全部用户列出处于指定状态的策略，不做 user_id 过滤。
//
// 这是本包唯一不按 user_id 过滤的查询方法，专供 cmd/executor/cmd/signal-engine 这类
// "一个进程服务多个用户"的系统级场景使用（多用户并发执行改造，第二阶段）。绝不能从
// internal/webui 的 handler 调用——那里每次调用都必须锚定在当前登录用户的 user_id 上，
// 调用这个方法等于让一个用户看到/影响到另一个用户的数据。
// SECURITY: cross-user query. 如果你在 internal/webui 里看到有代码引用了这个方法，
// 那是一个安全 bug（internal/webui 下有一条 grep 守卫测试专门盯这件事）。
func (s *Store) ListStrategiesByStateAllUsers(ctx context.Context, state types.StrategyState) ([]types.StrategyConfig, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT config, user_id, state, created_at, updated_at FROM strategies WHERE state = $1 ORDER BY user_id, created_at`,
		string(state))
	if err != nil {
		return nil, fmt.Errorf("按状态跨用户查询策略失败：%w", err)
	}
	defer rows.Close()

	var out []types.StrategyConfig
	for rows.Next() {
		var blob []byte
		var uid, st string
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&blob, &uid, &st, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		var cfg types.StrategyConfig
		if err := json.Unmarshal(blob, &cfg); err != nil {
			return nil, fmt.Errorf("反序列化策略失败：%w", err)
		}
		cfg.UserID = uid
		cfg.State = types.StrategyState(st)
		cfg.CreatedAt = createdAt
		cfg.UpdatedAt = updatedAt
		out = append(out, cfg)
	}
	return out, rows.Err()
}

// GetStrategyAllUsers 按 ID 读取策略配置，不做 user_id 过滤。
//
// 这是本包第二个不按 user_id 过滤的查询方法（第一个是 ListStrategiesByStateAllUsers，
// 见其注释），专供 cmd/notifier 使用：types.Decision 只携带 StrategyID，不携带
// UserID（见 pkg/types/strategy.go 的 Decision 注释），notifier 消费 Kafka 决策时
// 必须先反查这条决策属于哪个用户，才能查到该用户配置的提醒渠道——这一步天然是
// 跨用户的，同一个 notifier 进程要能服务所有用户的决策。
// SECURITY: cross-user query. 如果你在 internal/webui 里看到有代码引用了这个方法，
// 那是一个安全 bug（internal/webui 下有一条 grep 守卫测试专门盯这件事）。
func (s *Store) GetStrategyAllUsers(ctx context.Context, id string) (types.StrategyConfig, error) {
	var blob []byte
	var uid, state string
	var createdAt, updatedAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT config, user_id, state, created_at, updated_at FROM strategies WHERE id = $1`, id).
		Scan(&blob, &uid, &state, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.StrategyConfig{}, fmt.Errorf("策略 %s：%w", id, ErrNotFound)
	}
	if err != nil {
		return types.StrategyConfig{}, fmt.Errorf("跨用户读取策略 %s 失败：%w", id, err)
	}
	var cfg types.StrategyConfig
	if err := json.Unmarshal(blob, &cfg); err != nil {
		return types.StrategyConfig{}, fmt.Errorf("反序列化策略 %s 失败：%w", id, err)
	}
	// 见 GetStrategy 的注释：状态、归属用户、创建/更新时间都以独立列为准，config
	// 里嵌的这几个字段只是调用方随手带的一份快照，可能是零值或过期值。
	cfg.UserID = uid
	cfg.State = types.StrategyState(state)
	cfg.CreatedAt = createdAt
	cfg.UpdatedAt = updatedAt
	return cfg, nil
}

// ---------- 决策审计 ----------

// RecordDecision 实现 engine.Auditor：把一次决策写入审计表。
func (s *Store) RecordDecision(ctx context.Context, d types.Decision) error {
	signals, err := json.Marshal(d.Signals)
	if err != nil {
		return fmt.Errorf("序列化信号失败：%w", err)
	}
	const q = `
		INSERT INTO decisions (id, strategy_id, symbol, direction, score, triggered, reason, price, signals, bar_time, evaluated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	_, err = s.pool.Exec(ctx, q,
		d.ID, d.StrategyID, d.Symbol, string(d.Direction), d.Score, d.Triggered,
		d.Reason, d.Price.String(), signals, d.Timestamp, d.EvaluatedAt)
	if err != nil {
		return fmt.Errorf("写入决策审计失败：%w", err)
	}
	return nil
}

// ListDecisions 按时间倒序读取某策略的决策记录。
// userID 通过 JOIN strategies 传递校验归属——decisions 表本身没有 user_id 列
// （见 006_strategy_ownership.sql 的注释：子表所有权一律通过 strategy_id 传递判断，
// 不在每张子表上各自维护一份可能漂移的归属列）。
func (s *Store) ListDecisions(ctx context.Context, userID, strategyID string, limit int) ([]types.Decision, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `
		SELECT d.id, d.strategy_id, d.symbol, d.direction, d.score, d.triggered, d.reason, d.price, d.signals, d.bar_time, d.evaluated_at
		FROM decisions d JOIN strategies s ON s.id = d.strategy_id
		WHERE d.strategy_id = $1 AND s.user_id = $2 ORDER BY d.bar_time DESC LIMIT $3`
	rows, err := s.pool.Query(ctx, q, strategyID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("查询决策记录失败：%w", err)
	}
	defer rows.Close()

	var out []types.Decision
	for rows.Next() {
		var d types.Decision
		var dir, price string
		var signals []byte
		if err := rows.Scan(&d.ID, &d.StrategyID, &d.Symbol, &dir, &d.Score, &d.Triggered,
			&d.Reason, &price, &signals, &d.Timestamp, &d.EvaluatedAt); err != nil {
			return nil, err
		}
		d.Direction = types.Direction(dir)
		if d.Price, err = decimal.NewFromString(price); err != nil {
			return nil, fmt.Errorf("解析决策价格 %q 失败：%w", price, err)
		}
		if err := json.Unmarshal(signals, &d.Signals); err != nil {
			return nil, fmt.Errorf("反序列化信号失败：%w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---------- 状态流转审计 ----------

// Transition 是一条状态变更记录。
type Transition struct {
	StrategyID string
	From       types.StrategyState
	To         types.StrategyState
	// Actor 是操作者标识："user:<id>"、"system:backtest" 等。
	Actor string
	// Reason 说明推进依据。
	Reason string
	// Evidence 保存支撑本次推进的数据（如回测指标快照）。
	Evidence  map[string]any
	CreatedAt time.Time
}

// RecordTransition 追加一条状态流转审计。这张表只增不改。
func (s *Store) RecordTransition(ctx context.Context, t Transition) error {
	var evidence []byte
	if t.Evidence != nil {
		var err error
		if evidence, err = json.Marshal(t.Evidence); err != nil {
			return fmt.Errorf("序列化流转依据失败：%w", err)
		}
	}
	const q = `
		INSERT INTO strategy_state_transitions (strategy_id, from_state, to_state, actor, reason, evidence)
		VALUES ($1, $2, $3, $4, $5, $6)`
	if _, err := s.pool.Exec(ctx, q,
		t.StrategyID, string(t.From), string(t.To), t.Actor, t.Reason, evidence); err != nil {
		return fmt.Errorf("写入状态流转审计失败：%w", err)
	}
	return nil
}

// UpdateStrategyState 更新策略状态列，并在同一事务内写入审计记录。userID 不匹配
// （策略存在但不是当前用户的）会跟"并发冲突"走同一条报错路径——两者对调用方来说都是
// "这次推进没有成功"，不需要用不同的错误类型区分，也避免暴露"这个策略 ID 存在，
// 只是不是你的"这种信息。
//
// 状态、审计两者必须同事务：状态变了却没留下审计记录，等于丢失了合规证据。
func (s *Store) UpdateStrategyState(ctx context.Context, userID string, t Transition) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("开启事务失败：%w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // 提交成功后 Rollback 是空操作

	// 带上 from_state 条件做乐观锁：并发推进时只有一方能成功；带上 user_id 防止
	// 跨用户推进别人的策略状态。
	tag, err := tx.Exec(ctx,
		`UPDATE strategies SET state = $1, updated_at = now() WHERE id = $2 AND state = $3 AND user_id = $4`,
		string(t.To), t.StrategyID, string(t.From), userID)
	if err != nil {
		return fmt.Errorf("更新策略状态失败：%w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("策略 %s 的状态已不是 %s，本次推进被拒绝（可能有并发操作）",
			t.StrategyID, t.From)
	}

	var evidence []byte
	if t.Evidence != nil {
		if evidence, err = json.Marshal(t.Evidence); err != nil {
			return fmt.Errorf("序列化流转依据失败：%w", err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO strategy_state_transitions (strategy_id, from_state, to_state, actor, reason, evidence)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		t.StrategyID, string(t.From), string(t.To), t.Actor, t.Reason, evidence); err != nil {
		return fmt.Errorf("写入状态流转审计失败：%w", err)
	}

	return tx.Commit(ctx)
}

// ListTransitions 读取某策略的全部状态流转记录，按时间正序。userID 通过 JOIN
// strategies 传递校验归属，同 ListDecisions。
func (s *Store) ListTransitions(ctx context.Context, userID, strategyID string) ([]Transition, error) {
	const q = `
		SELECT t.strategy_id, t.from_state, t.to_state, t.actor, t.reason, t.evidence, t.created_at
		FROM strategy_state_transitions t JOIN strategies s ON s.id = t.strategy_id
		WHERE t.strategy_id = $1 AND s.user_id = $2 ORDER BY t.created_at`
	rows, err := s.pool.Query(ctx, q, strategyID, userID)
	if err != nil {
		return nil, fmt.Errorf("查询状态流转记录失败：%w", err)
	}
	defer rows.Close()

	var out []Transition
	for rows.Next() {
		var t Transition
		var from, to string
		var evidence []byte
		if err := rows.Scan(&t.StrategyID, &from, &to, &t.Actor, &t.Reason, &evidence, &t.CreatedAt); err != nil {
			return nil, err
		}
		t.From, t.To = types.StrategyState(from), types.StrategyState(to)
		if len(evidence) > 0 {
			if err := json.Unmarshal(evidence, &t.Evidence); err != nil {
				return nil, fmt.Errorf("反序列化流转依据失败：%w", err)
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
