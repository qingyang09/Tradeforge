package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"tradeforge/pkg/types"
)

// ---------- Backtest results ----------
//
// Read-only: the backtest_results table is currently written only by
// save_result() in python/backtest/tradeforge_backtest/store.py. The Go side
// has no corresponding write method; the UI is only responsible for reading
// and displaying existing results.

// ListBacktestResults reads a strategy's backtest results in reverse run-time
// order. userID is validated by JOINing strategies -- the backtest_results
// table itself has no user_id column, see the comment on ListDecisions in
// postgres.go, same principle.
func (s *Store) ListBacktestResults(ctx context.Context, userID, strategyID string, limit int) ([]types.BacktestResult, error) {
	if limit <= 0 {
		limit = 20
	}
	const q = `
		SELECT br.id, br.strategy_id, br.symbol, br.overall, br.in_sample, br.out_of_sample,
		       br.segments, br.fee_model, br.trades, br.equity_curve, br.initial_capital,
		       br.data_start, br.data_end, br.engine_version, br.ran_at
		FROM backtest_results br JOIN strategies s ON s.id = br.strategy_id
		WHERE br.strategy_id = $1 AND s.user_id = $2 ORDER BY br.ran_at DESC LIMIT $3`
	rows, err := s.pool.Query(ctx, q, strategyID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("query backtest results: %w", err)
	}
	defer rows.Close()

	var out []types.BacktestResult
	for rows.Next() {
		r, err := scanBacktestResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestBacktestResult reads a strategy's most recent backtest result;
// returns ErrNotFound if it has never been backtested.
func (s *Store) LatestBacktestResult(ctx context.Context, userID, strategyID string) (types.BacktestResult, error) {
	const q = `
		SELECT br.id, br.strategy_id, br.symbol, br.overall, br.in_sample, br.out_of_sample,
		       br.segments, br.fee_model, br.trades, br.equity_curve, br.initial_capital,
		       br.data_start, br.data_end, br.engine_version, br.ran_at
		FROM backtest_results br JOIN strategies s ON s.id = br.strategy_id
		WHERE br.strategy_id = $1 AND s.user_id = $2 ORDER BY br.ran_at DESC LIMIT 1`
	row := s.pool.QueryRow(ctx, q, strategyID, userID)
	r, err := scanBacktestResult(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return types.BacktestResult{}, fmt.Errorf("strategy %s: %w", strategyID, ErrNotFound)
	}
	if err != nil {
		return types.BacktestResult{}, err
	}
	return r, nil
}

// rowScanner is the minimal subset of pgx.Rows' Scan used here, so both
// queries can share the same parsing logic.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanBacktestResult(row rowScanner) (types.BacktestResult, error) {
	var r types.BacktestResult
	var overall, inSample, outOfSample, segments, feeModel, trades, equityCurve []byte
	var initialCapital string

	if err := row.Scan(
		&r.ID, &r.StrategyID, &r.Symbol, &overall, &inSample, &outOfSample,
		&segments, &feeModel, &trades, &equityCurve, &initialCapital,
		&r.DataStart, &r.DataEnd, &r.EngineVersion, &r.RanAt,
	); err != nil {
		return types.BacktestResult{}, fmt.Errorf("read backtest result: %w", err)
	}

	// total_fees and final_equity inside overall/in_sample/out_of_sample are
	// written by the Python side as strings (str(Decimal(...))).
	// decimal.Decimal's UnmarshalJSON natively supports quoted strings, so no
	// extra conversion is needed.
	if err := json.Unmarshal(overall, &r.Overall); err != nil {
		return types.BacktestResult{}, fmt.Errorf("unmarshal overall metrics: %w", err)
	}
	if err := json.Unmarshal(inSample, &r.InSample); err != nil {
		return types.BacktestResult{}, fmt.Errorf("unmarshal in_sample metrics: %w", err)
	}
	if err := json.Unmarshal(outOfSample, &r.OutOfSample); err != nil {
		return types.BacktestResult{}, fmt.Errorf("unmarshal out_of_sample metrics: %w", err)
	}
	if err := json.Unmarshal(segments, &r.Segments); err != nil {
		return types.BacktestResult{}, fmt.Errorf("unmarshal segments: %w", err)
	}
	if err := json.Unmarshal(feeModel, &r.FeeModel); err != nil {
		return types.BacktestResult{}, fmt.Errorf("unmarshal fee_model: %w", err)
	}
	// The trades column may be empty (NULL or an empty array); neither case is an error.
	if len(trades) > 0 {
		if err := json.Unmarshal(trades, &r.Trades); err != nil {
			return types.BacktestResult{}, fmt.Errorf("unmarshal trades: %w", err)
		}
	}
	// equity_curve is a column added later; historical rows from before
	// 2026-09 have it as NULL -- not an error, the UI just falls back to an
	// approximate curve for those old records.
	if len(equityCurve) > 0 {
		if err := json.Unmarshal(equityCurve, &r.EquityCurve); err != nil {
			return types.BacktestResult{}, fmt.Errorf("unmarshal equity_curve: %w", err)
		}
	}

	cap, err := decimal.NewFromString(initialCapital)
	if err != nil {
		return types.BacktestResult{}, fmt.Errorf("parse initial capital %q: %w", initialCapital, err)
	}
	r.InitialCapital = cap

	return r, nil
}
