//go:build integration

// Requires a Postgres started via docker-compose to run:
//
//	docker compose up -d
//	go test -tags=integration ./internal/storage/... -run Backtest -v
package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/config"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

// insertBacktestResultRow hand-assembles a row to insert into
// backtest_results, shaped to match result_to_row() in
// python/backtest/tradeforge_backtest/store.py -- the Go side currently has
// no write method, only read methods, so test data has to be constructed
// this way instead of via some Store method.
func insertBacktestResultRow(t *testing.T, ctx context.Context, s *Store, strategyID string) string {
	t.Helper()

	metricsJSON := func(tradeCount int, sharpe float64, totalFees, finalEquity string) []byte {
		// total_fees/final_equity are written as strings, exactly matching the Python side's Metrics.to_dict().
		b, err := json.Marshal(map[string]any{
			"total_return": 0.12, "annualized_return": 0.5, "sharpe_ratio": sharpe,
			"sortino_ratio": 1.1, "max_drawdown": 0.08, "win_rate": 0.6,
			"profit_factor": 1.8, "trade_count": tradeCount,
			"total_fees": totalFees, "final_equity": finalEquity,
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	dataStart := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	splitAt := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	dataEnd := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)

	overall := metricsJSON(10, 1.2, "12.50", "1050.00")
	inSample := metricsJSON(7, 2.5, "8.00", "1080.00")
	outOfSample := metricsJSON(3, 0.4, "4.50", "1050.00")

	segments, err := json.Marshal([]map[string]any{
		{"label": "in_sample", "start": dataStart, "end": splitAt},
		{"label": "out_of_sample", "start": splitAt, "end": dataEnd},
	})
	if err != nil {
		t.Fatal(err)
	}
	feeModel, err := json.Marshal(map[string]any{
		"maker_fee_rate": 0.0002, "taker_fee_rate": 0.0004, "slippage_bps": 5.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	trades, err := json.Marshal([]map[string]any{
		{
			"entry_time": splitAt.Add(time.Hour), "exit_time": splitAt.Add(2 * time.Hour),
			"direction": "LONG", "entry_price": "100.5", "exit_price": "105.25",
			"quantity": "1.5", "pnl": "7.125", "fees": "0.1",
			"exit_reason": "signal", "segment": "out_of_sample",
			"trigger_signals": []map[string]any{
				{"module": "volume_breakout", "direction": "LONG", "confidence": 0.8, "reason": "放量突破"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	equityCurve, err := json.Marshal([]map[string]any{
		{"time": dataStart, "equity": "1000.123456789012345678"},
		{"time": splitAt, "equity": "1080.00"},
		{"time": dataEnd, "equity": "1050.00"},
	})
	if err != nil {
		t.Fatal(err)
	}

	id := idgen.NewUUID()
	const q = `
		INSERT INTO backtest_results (
			id, strategy_id, symbol, overall, in_sample, out_of_sample,
			segments, fee_model, trades, equity_curve, initial_capital,
			data_start, data_end, engine_version
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`
	_, err = s.pool.Exec(ctx, q, id, strategyID, "BTCUSDT", overall, inSample, outOfSample,
		segments, feeModel, trades, equityCurve, "1000.123456789012345678", dataStart, dataEnd, "signal-replay/test")
	if err != nil {
		t.Fatalf("insert test backtest result: %v", err)
	}
	return id
}

func TestBacktestResultsRoundTrip(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connect to Postgres (did you run docker compose up -d?): %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "backtests-roundtrip")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("clean up test data: %v", err)
		}
	}()

	sc := types.StrategyConfig{
		ID: idgen.NewUUID(), UserID: u.ID, Name: "backtest results integration test", Symbol: "BTCUSDT",
		Timeframe: types.TF1h, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{{Module: "volume_breakout", Params: map[string]any{}}},
		Risk:    types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State:   types.StateDraft,
	}
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("save strategy: %v", err)
	}

	id := insertBacktestResultRow(t, ctx, store, sc.ID)

	latest, err := store.LatestBacktestResult(ctx, u.ID, sc.ID)
	if err != nil {
		t.Fatalf("read latest backtest result: %v", err)
	}
	if latest.ID != id {
		t.Errorf("ID = %s, want %s", latest.ID, id)
	}
	if latest.OutOfSample.TradeCount != 3 || latest.OutOfSample.SharpeRatio != 0.4 {
		t.Errorf("out-of-sample metrics mismatch: %+v", latest.OutOfSample)
	}
	if latest.InSample.TradeCount != 7 || latest.InSample.SharpeRatio != 2.5 {
		t.Errorf("in-sample metrics mismatch: %+v", latest.InSample)
	}
	// total_fees/final_equity are decimal fields written as strings by the Python side; they must round-trip exactly.
	wantFees, _ := decimal.NewFromString("4.50")
	if !latest.OutOfSample.TotalFees.Equal(wantFees) {
		t.Errorf("out-of-sample fees = %s, want %s", latest.OutOfSample.TotalFees, wantFees)
	}
	if len(latest.Segments) != 2 || latest.Segments[0].Label != "in_sample" {
		t.Errorf("segments parsed incorrectly: %+v", latest.Segments)
	}
	if latest.FeeModel.TakerFeeRate != 0.0004 {
		t.Errorf("fee_model parsed incorrectly: %+v", latest.FeeModel)
	}
	if len(latest.Trades) != 1 {
		t.Fatalf("trades count = %d, want 1", len(latest.Trades))
	}
	trade := latest.Trades[0]
	if trade.Segment != "out_of_sample" || trade.ExitReason != "signal" {
		t.Errorf("trade fields mismatch: %+v", trade)
	}
	wantPnL, _ := decimal.NewFromString("7.125")
	if !trade.PnL.Equal(wantPnL) {
		t.Errorf("trade.PnL = %s, want %s (precision must round-trip exactly)", trade.PnL, wantPnL)
	}
	if len(trade.TriggerSignals) != 1 || trade.TriggerSignals[0].Module != "volume_breakout" {
		t.Errorf("trigger_signals parsed incorrectly: %+v", trade.TriggerSignals)
	}
	wantCapital, _ := decimal.NewFromString("1000.123456789012345678")
	if !latest.InitialCapital.Equal(wantCapital) {
		t.Errorf("initial_capital = %s, want %s", latest.InitialCapital, wantCapital)
	}
	if len(latest.EquityCurve) != 3 {
		t.Fatalf("equity_curve length = %d, want 3", len(latest.EquityCurve))
	}
	wantMidEquity, _ := decimal.NewFromString("1080.00")
	wantSplitAt := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	if !latest.EquityCurve[1].Equity.Equal(wantMidEquity) || !latest.EquityCurve[1].Time.Equal(wantSplitAt) {
		t.Errorf("equity_curve[1] = %+v, want equity=%s time=%s", latest.EquityCurve[1], wantMidEquity, wantSplitAt)
	}

	list, err := store.ListBacktestResults(ctx, u.ID, sc.ID, 10)
	if err != nil {
		t.Fatalf("list backtest results: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("backtest results count = %d, want 1", len(list))
	}
}

func TestLatestBacktestResultErrorsWhenNeverRun(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("connect to Postgres: %v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "backtests-never-run")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("clean up test data: %v", err)
		}
	}()

	sc := types.StrategyConfig{
		ID: idgen.NewUUID(), UserID: u.ID, Name: "never backtested", Symbol: "BTCUSDT",
		Timeframe: types.TF1h, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{{Module: "volume_breakout", Params: map[string]any{}}},
		Risk:    types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State:   types.StateDraft,
	}
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("save strategy: %v", err)
	}

	if _, err := store.LatestBacktestResult(ctx, u.ID, sc.ID); err == nil {
		t.Fatal("a strategy that has never been backtested should return an error")
	}
}
