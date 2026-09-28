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

// ---------- 回测结果 ----------
//
// 只读：backtest_results 表目前只由 python/backtest/tradeforge_backtest/store.py 的
// save_result() 写入，Go 侧没有对应的写方法，界面只负责把已有结果读出来展示。

// ListBacktestResults 按运行时间倒序读取某策略的回测结果。userID 通过 JOIN strategies
// 传递校验归属——backtest_results 表本身没有 user_id 列，见 postgres.go 的
// ListDecisions 注释，同一个原则。
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
		return nil, fmt.Errorf("查询回测结果失败：%w", err)
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

// LatestBacktestResult 读取某策略最近一次回测结果；从未回测过时返回 ErrNotFound。
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
		return types.BacktestResult{}, fmt.Errorf("策略 %s：%w", strategyID, ErrNotFound)
	}
	if err != nil {
		return types.BacktestResult{}, err
	}
	return r, nil
}

// rowScanner 是 pgx.Rows 里 Scan 用到的最小子集，便于两个查询共用同一段解析逻辑。
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
		return types.BacktestResult{}, fmt.Errorf("读取回测结果失败：%w", err)
	}

	// overall/in_sample/out_of_sample 里的 total_fees、final_equity 由 Python 端按字符串写入
	// （str(Decimal(...))），decimal.Decimal 的 UnmarshalJSON 原生支持带引号字符串，
	// 不需要额外转换。
	if err := json.Unmarshal(overall, &r.Overall); err != nil {
		return types.BacktestResult{}, fmt.Errorf("反序列化 overall 指标失败：%w", err)
	}
	if err := json.Unmarshal(inSample, &r.InSample); err != nil {
		return types.BacktestResult{}, fmt.Errorf("反序列化 in_sample 指标失败：%w", err)
	}
	if err := json.Unmarshal(outOfSample, &r.OutOfSample); err != nil {
		return types.BacktestResult{}, fmt.Errorf("反序列化 out_of_sample 指标失败：%w", err)
	}
	if err := json.Unmarshal(segments, &r.Segments); err != nil {
		return types.BacktestResult{}, fmt.Errorf("反序列化 segments 失败：%w", err)
	}
	if err := json.Unmarshal(feeModel, &r.FeeModel); err != nil {
		return types.BacktestResult{}, fmt.Errorf("反序列化 fee_model 失败：%w", err)
	}
	// trades 列允许为空（NULL 或空数组），两种情况都不算错误。
	if len(trades) > 0 {
		if err := json.Unmarshal(trades, &r.Trades); err != nil {
			return types.BacktestResult{}, fmt.Errorf("反序列化 trades 失败：%w", err)
		}
	}
	// equity_curve 是后加的列，2026-09 之前的历史记录这一列是 NULL——不是错误，
	// 界面对这些旧记录退回近似曲线即可。
	if len(equityCurve) > 0 {
		if err := json.Unmarshal(equityCurve, &r.EquityCurve); err != nil {
			return types.BacktestResult{}, fmt.Errorf("反序列化 equity_curve 失败：%w", err)
		}
	}

	cap, err := decimal.NewFromString(initialCapital)
	if err != nil {
		return types.BacktestResult{}, fmt.Errorf("解析初始资金 %q 失败：%w", initialCapital, err)
	}
	r.InitialCapital = cap

	return r, nil
}
