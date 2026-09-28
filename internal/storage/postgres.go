// Package storage provides the Postgres data access layer.
//
// Convention: all amount/price columns in the database are NUMERIC. Reads
// and writes pass through decimal.Decimal's string representation and never
// go through float64 -- that conversion silently loses precision.
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

// ErrNotFound indicates no row was found for the given primary key.
var ErrNotFound = errors.New("record not found")

// Store is the entry point for Postgres data access.
type Store struct {
	pool *pgxpool.Pool
}

// Open establishes a connection pool and verifies connectivity.
func Open(ctx context.Context, cfg config.PostgresConfig) (*Store, error) {
	pool, err := pgxpool.New(ctx, cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to Postgres (%s:%d): %w", cfg.Host, cfg.Port, err)
	}
	return &Store{pool: pool}, nil
}

// NewStore builds a Store from an existing pool, for test injection.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the underlying connection pool for cases that need custom queries.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Close closes the connection pool.
func (s *Store) Close() { s.pool.Close() }

// ---------- Strategies ----------

// SaveStrategy inserts or updates a strategy config. An update is only
// allowed when userID matches the existing row's owner (see the WHERE
// clause on the ON CONFLICT branch) -- without this check, a
// forged/replayed request carrying someone else's strategy id could
// directly overwrite that person's strategy content, not just "see" it but
// "rewrite" it. This has to be enforced at this layer; we can't rely on
// upstream business logic always doing an ownership check first. If the id
// already exists but belongs to a different user, this returns ErrNotFound
// -- in normal flow ids are new UUIDs generated server-side, so this branch
// is never really hit; only a forged request would trigger it.
func (s *Store) SaveStrategy(ctx context.Context, cfg types.StrategyConfig) error {
	blob, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal strategy config: %w", err)
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
		return fmt.Errorf("save strategy %s: %w", cfg.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("strategy %s: %w", cfg.ID, ErrNotFound)
	}
	return nil
}

// GetStrategy reads a strategy config by ID. A userID mismatch (the
// strategy exists but belongs to a different user) returns ErrNotFound,
// same as if the strategy didn't exist at all.
func (s *Store) GetStrategy(ctx context.Context, userID, id string) (types.StrategyConfig, error) {
	var blob []byte
	var uid, state string
	var createdAt, updatedAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT config, user_id, state, created_at, updated_at FROM strategies WHERE id = $1 AND user_id = $2`, id, userID).
		Scan(&blob, &uid, &state, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.StrategyConfig{}, fmt.Errorf("strategy %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return types.StrategyConfig{}, fmt.Errorf("read strategy %s: %w", id, err)
	}
	var cfg types.StrategyConfig
	if err := json.Unmarshal(blob, &cfg); err != nil {
		return types.StrategyConfig{}, fmt.Errorf("unmarshal strategy %s: %w", id, err)
	}
	// State, owning user, and created/updated times all come from their own
	// columns, not the embedded config blob: the database maintains these
	// columns itself, while the same fields inside config are just whatever
	// snapshot the caller happened to pass into SaveStrategy -- possibly a
	// zero value or stale. A previous version only overrode State this way
	// and missed CreatedAt/UpdatedAt, which actually caused the UI to show a
	// never-written zero time like "0001-01-01". UserID can't be trusted
	// from a possibly-stale JSONB value either, for the same reason.
	cfg.UserID = uid
	cfg.State = types.StrategyState(state)
	cfg.CreatedAt = createdAt
	cfg.UpdatedAt = updatedAt
	return cfg, nil
}

// DeleteStrategy deletes a strategy and all its associated data (backtest
// results/orders/decisions/state transition records, cascaded via ON DELETE
// CASCADE in the schema -- no need to clean up each table by hand here).
//
// This layer doesn't decide whether deletion is allowed (e.g. only
// permitting deletion of DRAFT strategies) -- that's the caller's job (the
// internal/webui handlers). This layer just executes the delete
// mechanically, the same division of responsibility as SaveStrategy not
// validating state machine legality.
func (s *Store) DeleteStrategy(ctx context.Context, userID, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM strategies WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete strategy %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("strategy %s: %w", id, ErrNotFound)
	}
	return nil
}

// ListStrategies lists all of the user's strategies, ordered by creation
// time ascending. The dashboard needs the full list to show a state machine
// overview, not just strategies filtered to a single state.
func (s *Store) ListStrategies(ctx context.Context, userID string) ([]types.StrategyConfig, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT config, user_id, state, created_at, updated_at FROM strategies WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("query all strategies: %w", err)
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
			return nil, fmt.Errorf("unmarshal strategy: %w", err)
		}
		// See the comment on GetStrategy: these columns are authoritative;
		// the copies embedded in config may be zero-valued or stale.
		cfg.UserID = uid
		cfg.State = types.StrategyState(st)
		cfg.CreatedAt = createdAt
		cfg.UpdatedAt = updatedAt
		out = append(out, cfg)
	}
	return out, rows.Err()
}

// ListStrategiesByState lists all of the user's strategies in the given
// state -- userID here comes from resolving cmd/executor's/
// cmd/signal-engine's -owner-email flag, which draws the boundary "this
// process only serves this one user's strategies, using only this one
// user's credentials"; see the comments in those two commands' main.go.
func (s *Store) ListStrategiesByState(ctx context.Context, userID string, state types.StrategyState) ([]types.StrategyConfig, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT config, user_id, state, created_at, updated_at FROM strategies WHERE user_id = $1 AND state = $2 ORDER BY created_at`,
		userID, string(state))
	if err != nil {
		return nil, fmt.Errorf("query strategies by state: %w", err)
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
			return nil, fmt.Errorf("unmarshal strategy: %w", err)
		}
		cfg.UserID = uid
		cfg.State = types.StrategyState(st)
		cfg.CreatedAt = createdAt
		cfg.UpdatedAt = updatedAt
		out = append(out, cfg)
	}
	return out, rows.Err()
}

// ListStrategiesByStateAllUsers lists strategies in the given state across
// all users, with no user_id filter.
//
// This is the only query method in this package that doesn't filter by
// user_id, provided specifically for system-level "one process serves many
// users" scenarios like cmd/executor/cmd/signal-engine (phase 2 of the
// multi-user concurrent execution rework). It must never be called from
// internal/webui handlers -- every call there must be anchored to the
// currently logged-in user's user_id; calling this method would let one
// user see/affect another user's data.
// SECURITY: cross-user query. If you see this method referenced from
// internal/webui, that's a security bug (internal/webui has a grep guard
// test specifically watching for this).
func (s *Store) ListStrategiesByStateAllUsers(ctx context.Context, state types.StrategyState) ([]types.StrategyConfig, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT config, user_id, state, created_at, updated_at FROM strategies WHERE state = $1 ORDER BY user_id, created_at`,
		string(state))
	if err != nil {
		return nil, fmt.Errorf("query strategies across users by state: %w", err)
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
			return nil, fmt.Errorf("unmarshal strategy: %w", err)
		}
		cfg.UserID = uid
		cfg.State = types.StrategyState(st)
		cfg.CreatedAt = createdAt
		cfg.UpdatedAt = updatedAt
		out = append(out, cfg)
	}
	return out, rows.Err()
}

// GetStrategyAllUsers reads a strategy config by ID, with no user_id filter.
//
// This is the second query method in this package that doesn't filter by
// user_id (the first is ListStrategiesByStateAllUsers, see its comment),
// provided specifically for cmd/notifier: types.Decision only carries a
// StrategyID, not a UserID (see the Decision comment in
// pkg/types/strategy.go), so when notifier consumes a Kafka decision it
// must first look up which user that decision belongs to before it can
// find that user's configured notification channels -- this lookup is
// inherently cross-user, since a single notifier process must serve every
// user's decisions.
// SECURITY: cross-user query. If you see this method referenced from
// internal/webui, that's a security bug (internal/webui has a grep guard
// test specifically watching for this).
func (s *Store) GetStrategyAllUsers(ctx context.Context, id string) (types.StrategyConfig, error) {
	var blob []byte
	var uid, state string
	var createdAt, updatedAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT config, user_id, state, created_at, updated_at FROM strategies WHERE id = $1`, id).
		Scan(&blob, &uid, &state, &createdAt, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.StrategyConfig{}, fmt.Errorf("strategy %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return types.StrategyConfig{}, fmt.Errorf("read strategy %s across users: %w", id, err)
	}
	var cfg types.StrategyConfig
	if err := json.Unmarshal(blob, &cfg); err != nil {
		return types.StrategyConfig{}, fmt.Errorf("unmarshal strategy %s: %w", id, err)
	}
	// See the comment on GetStrategy: state, owning user, and
	// created/updated times all come from their own columns; the fields
	// embedded in config are just whatever snapshot the caller happened to
	// pass, possibly zero-valued or stale.
	cfg.UserID = uid
	cfg.State = types.StrategyState(state)
	cfg.CreatedAt = createdAt
	cfg.UpdatedAt = updatedAt
	return cfg, nil
}

// ---------- Decision audit ----------

// RecordDecision implements engine.Auditor: writes a decision to the audit table.
func (s *Store) RecordDecision(ctx context.Context, d types.Decision) error {
	signals, err := json.Marshal(d.Signals)
	if err != nil {
		return fmt.Errorf("marshal signals: %w", err)
	}
	const q = `
		INSERT INTO decisions (id, strategy_id, symbol, direction, score, triggered, reason, price, signals, bar_time, evaluated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`
	_, err = s.pool.Exec(ctx, q,
		d.ID, d.StrategyID, d.Symbol, string(d.Direction), d.Score, d.Triggered,
		d.Reason, d.Price.String(), signals, d.Timestamp, d.EvaluatedAt)
	if err != nil {
		return fmt.Errorf("write decision audit: %w", err)
	}
	return nil
}

// ListDecisions reads a strategy's decision records in reverse
// chronological order.
// userID is validated by JOINing strategies -- the decisions table itself
// has no user_id column (see the comment in 006_strategy_ownership.sql:
// child table ownership is always resolved by strategy_id, rather than
// maintaining a separate ownership column on every child table that could
// drift out of sync).
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
		return nil, fmt.Errorf("query decision records: %w", err)
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
			return nil, fmt.Errorf("parse decision price %q: %w", price, err)
		}
		if err := json.Unmarshal(signals, &d.Signals); err != nil {
			return nil, fmt.Errorf("unmarshal signals: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---------- State transition audit ----------

// Transition is a single state change record.
type Transition struct {
	StrategyID string
	From       types.StrategyState
	To         types.StrategyState
	// Actor identifies who performed the change: "user:<id>",
	// "system:backtest", etc.
	Actor string
	// Reason explains the basis for the transition.
	Reason string
	// Evidence holds the data supporting this transition (e.g. a backtest
	// metrics snapshot).
	Evidence  map[string]any
	CreatedAt time.Time
}

// RecordTransition appends a state transition audit record. This table is append-only.
func (s *Store) RecordTransition(ctx context.Context, t Transition) error {
	var evidence []byte
	if t.Evidence != nil {
		var err error
		if evidence, err = json.Marshal(t.Evidence); err != nil {
			return fmt.Errorf("marshal transition evidence: %w", err)
		}
	}
	const q = `
		INSERT INTO strategy_state_transitions (strategy_id, from_state, to_state, actor, reason, evidence)
		VALUES ($1, $2, $3, $4, $5, $6)`
	if _, err := s.pool.Exec(ctx, q,
		t.StrategyID, string(t.From), string(t.To), t.Actor, t.Reason, evidence); err != nil {
		return fmt.Errorf("write state transition audit: %w", err)
	}
	return nil
}

// UpdateStrategyState updates the strategy's state column and writes an
// audit record within the same transaction. A userID mismatch (the
// strategy exists but belongs to a different user) goes through the same
// error path as a "concurrent conflict" -- from the caller's perspective
// both just mean "this transition didn't succeed," so there's no need to
// distinguish them with different error types, and it avoids leaking "this
// strategy ID exists, it's just not yours".
//
// State and audit must share a transaction: if the state changed but no
// audit record was left behind, that's lost compliance evidence.
func (s *Store) UpdateStrategyState(ctx context.Context, userID string, t Transition) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // Rollback is a no-op after a successful commit

	// The from_state condition acts as an optimistic lock: under concurrent
	// transitions only one side succeeds; the user_id condition prevents
	// advancing another user's strategy state across users.
	tag, err := tx.Exec(ctx,
		`UPDATE strategies SET state = $1, updated_at = now() WHERE id = $2 AND state = $3 AND user_id = $4`,
		string(t.To), t.StrategyID, string(t.From), userID)
	if err != nil {
		return fmt.Errorf("update strategy state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("strategy %s is no longer in state %s, this transition was rejected (a concurrent operation may have changed it)",
			t.StrategyID, t.From)
	}

	var evidence []byte
	if t.Evidence != nil {
		if evidence, err = json.Marshal(t.Evidence); err != nil {
			return fmt.Errorf("marshal transition evidence: %w", err)
		}
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO strategy_state_transitions (strategy_id, from_state, to_state, actor, reason, evidence)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		t.StrategyID, string(t.From), string(t.To), t.Actor, t.Reason, evidence); err != nil {
		return fmt.Errorf("write state transition audit: %w", err)
	}

	return tx.Commit(ctx)
}

// ListTransitions reads all state transition records for a strategy,
// ordered by time ascending. userID is validated by JOINing strategies,
// same as ListDecisions.
func (s *Store) ListTransitions(ctx context.Context, userID, strategyID string) ([]Transition, error) {
	const q = `
		SELECT t.strategy_id, t.from_state, t.to_state, t.actor, t.reason, t.evidence, t.created_at
		FROM strategy_state_transitions t JOIN strategies s ON s.id = t.strategy_id
		WHERE t.strategy_id = $1 AND s.user_id = $2 ORDER BY t.created_at`
	rows, err := s.pool.Query(ctx, q, strategyID, userID)
	if err != nil {
		return nil, fmt.Errorf("query state transition records: %w", err)
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
				return nil, fmt.Errorf("unmarshal transition evidence: %w", err)
			}
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
