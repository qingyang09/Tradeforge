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

// PaperStats 从已持久化的数据重新计算模拟盘运行统计，而不是维护一个独立的计数器——
// 计数器和订单表本来就该是同一个数字，维护两份只会产生"哪个才对"的分歧，
// 还得操心两边何时失步。
//
// 起始时间取该策略最近一次进入 PAPER_TRADING 的流转记录，而不是第一次：
// 策略可能因触发风控被暂停后重新进入模拟盘（SUSPENDED → PAPER_TRADING），
// 此时必须重新计时，不能沿用旧起点——否则等于允许"很久以前跑够过时长"
// 当成永久免检，架空了状态机注释里"触发过风控的策略必须重新证明自己"这条规则。
// userID 通过 JOIN strategies 传递校验归属，同 ListDecisions——一旦这一步确认了
// strategyID 属于 userID，下面按同一个 strategyID 去数 orders 就不用再重复校验一次。
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
		return strategy.PaperStats{}, fmt.Errorf("策略 %s 从未进入过模拟盘：%w", strategyID, ErrNotFound)
	}
	if err != nil {
		return strategy.PaperStats{}, fmt.Errorf("查询模拟盘起始时间失败：%w", err)
	}

	// 只统计成交（FILLED）订单：被风控/交易所拒绝的下单请求不代表策略真的动过手，
	// 拿它们冲抵模拟盘门槛会让"跑够笔数"变得毫无意义。
	var tradeCount int
	err = s.pool.QueryRow(ctx, `
		SELECT count(*) FROM orders
		WHERE strategy_id = $1 AND mode = $2 AND status = $3 AND created_at >= $4`,
		strategyID, string(types.ModePaper), string(types.OrderFilled), startedAt,
	).Scan(&tradeCount)
	if err != nil {
		return strategy.PaperStats{}, fmt.Errorf("统计模拟盘成交笔数失败：%w", err)
	}

	return strategy.PaperStats{
		StartedAt:  startedAt,
		Now:        time.Now().UTC(),
		TradeCount: tradeCount,
	}, nil
}
