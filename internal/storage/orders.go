package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/shopspring/decimal"

	"tradeforge/internal/execution"
	"tradeforge/pkg/types"
)

// RecordOrder implements execution.OrderRecorder: writes an order to the audit table.
//
// provenance is a mandatory field: every order must be able to answer "which
// module's which signal triggered this."
func (s *Store) RecordOrder(ctx context.Context, o types.Order) error {
	prov, err := json.Marshal(o.Provenance)
	if err != nil {
		return fmt.Errorf("marshal order provenance: %w", err)
	}

	const q = `
		INSERT INTO orders (
			id, strategy_id, symbol, side, type, mode, quantity, price,
			filled_price, fee, status, exchange_order_id, reject_reason,
			provenance, created_at, filled_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
		)`

	var filledAt any
	if !o.FilledAt.IsZero() {
		filledAt = o.FilledAt
	}

	_, err = s.pool.Exec(ctx, q,
		o.ID, o.StrategyID, o.Symbol, string(o.Side), string(o.Type), string(o.Mode),
		o.Quantity.String(), nullableDecimal(o.Price), nullableDecimal(o.FilledPrice),
		nullableDecimal(o.Fee), string(o.Status), nullableString(o.ExchangeOrderID),
		nullableString(o.RejectReason), prov, o.CreatedAt, filledAt,
	)
	if err != nil {
		return fmt.Errorf("write order %s: %w", o.ID, err)
	}
	return nil
}

// RecordRiskEvent implements execution.RiskEventRecorder.
func (s *Store) RecordRiskEvent(ctx context.Context, ev execution.RiskEvent) error {
	detail, err := json.Marshal(ev.Detail)
	if err != nil {
		return fmt.Errorf("marshal risk event detail: %w", err)
	}
	const q = `
		INSERT INTO risk_events (strategy_id, symbol, rule, detail, action, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`
	if _, err := s.pool.Exec(ctx, q,
		ev.StrategyID, ev.Symbol, ev.Rule, detail, ev.Action, ev.CreatedAt); err != nil {
		return fmt.Errorf("write risk event: %w", err)
	}
	return nil
}

// ListOrders reads a strategy's orders in reverse chronological order.
// userID is validated by JOINing strategies -- the orders table itself has
// no user_id column, see the comment on ListDecisions in postgres.go, same
// principle.
func (s *Store) ListOrders(ctx context.Context, userID, strategyID string, limit int) ([]types.Order, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `
		SELECT o.id, o.strategy_id, o.symbol, o.side, o.type, o.mode, o.quantity,
		       o.filled_price, o.fee, o.status, o.exchange_order_id, o.provenance, o.created_at
		FROM orders o JOIN strategies s ON s.id = o.strategy_id
		WHERE o.strategy_id = $1 AND s.user_id = $2 ORDER BY o.created_at DESC LIMIT $3`

	rows, err := s.pool.Query(ctx, q, strategyID, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("query orders: %w", err)
	}
	defer rows.Close()

	var out []types.Order
	for rows.Next() {
		var o types.Order
		var side, typ, mode, status string
		var qty string
		var filledPrice, fee *string
		var exchangeID *string
		var prov []byte

		if err := rows.Scan(&o.ID, &o.StrategyID, &o.Symbol, &side, &typ, &mode,
			&qty, &filledPrice, &fee, &status, &exchangeID, &prov, &o.CreatedAt); err != nil {
			return nil, err
		}

		o.Side, o.Type = types.OrderSide(side), types.OrderType(typ)
		o.Mode, o.Status = types.TradingMode(mode), types.OrderStatus(status)
		if o.Quantity, err = decimal.NewFromString(qty); err != nil {
			return nil, fmt.Errorf("parse order quantity %q: %w", qty, err)
		}
		if filledPrice != nil {
			if o.FilledPrice, err = decimal.NewFromString(*filledPrice); err != nil {
				return nil, fmt.Errorf("parse filled price: %w", err)
			}
		}
		if fee != nil {
			if o.Fee, err = decimal.NewFromString(*fee); err != nil {
				return nil, fmt.Errorf("parse fee: %w", err)
			}
		}
		if exchangeID != nil {
			o.ExchangeOrderID = *exchangeID
		}
		if err := json.Unmarshal(prov, &o.Provenance); err != nil {
			return nil, fmt.Errorf("unmarshal order provenance: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func nullableDecimal(d decimal.Decimal) any {
	if d.IsZero() {
		return nil
	}
	return d.String()
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
