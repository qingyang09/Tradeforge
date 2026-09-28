package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"tradeforge/internal/strategy"
	"tradeforge/pkg/types"
)

// PaperStats recomputes paper trading run statistics from already-persisted
// data, rather than maintaining a separate counter -- a counter and the
// orders table should always agree on the same number; keeping two copies
// only creates a "which one is right" discrepancy and the added burden of
// worrying about when the two drift apart.
//
// The start time is taken from the strategy's most recent transition into
// PAPER_TRADING, not its first: a strategy can be suspended by triggering
// risk controls and then re-enter paper trading (SUSPENDED -> PAPER_TRADING),
// at which point the clock must reset rather than reuse the old start time --
// otherwise a strategy could rely on time accumulated long ago as a
// permanent exemption, defeating the state machine's rule (see its comments)
// that a strategy which has triggered risk controls must prove itself again.
// userID is validated by JOINing strategies, same as ListDecisions -- once
// this step confirms strategyID belongs to userID, counting orders by that
// same strategyID below doesn't need to re-validate ownership.
func (s *Store) PaperStats(ctx context.Context, userID, strategyID string) (strategy.PaperStats, error) {
	var startedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT t.created_at FROM strategy_state_transitions t
		JOIN strategies s ON s.id = t.strategy_id
		WHERE t.strategy_id = $1 AND s.user_id = $2 AND t.to_state = $3
		ORDER BY t.created_at DESC LIMIT 1`,
		strategyID, userID, string(types.StatePaperTrading),
	).Scan(&startedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return strategy.PaperStats{}, fmt.Errorf("strategy %s has never entered paper trading: %w", strategyID, ErrNotFound)
	}
	if err != nil {
		return strategy.PaperStats{}, fmt.Errorf("query paper trading start time: %w", err)
	}

	// Only count FILLED orders: an order request rejected by risk controls
	// or the exchange doesn't mean the strategy actually acted, and counting
	// those toward the paper trading threshold would make "ran enough
	// trades" meaningless.
	var tradeCount int
	err = s.pool.QueryRow(ctx, `
		SELECT count(*) FROM orders
		WHERE strategy_id = $1 AND mode = $2 AND status = $3 AND created_at >= $4`,
		strategyID, string(types.ModePaper), string(types.OrderFilled), startedAt,
	).Scan(&tradeCount)
	if err != nil {
		return strategy.PaperStats{}, fmt.Errorf("count paper trading fills: %w", err)
	}

	return strategy.PaperStats{
		StartedAt:  startedAt,
		Now:        time.Now().UTC(),
		TradeCount: tradeCount,
	}, nil
}
