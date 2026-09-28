//go:build integration

// 需要 docker-compose 起的 Postgres 才能运行：
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

// insertBacktestResultRow 手工拼一行写入 backtest_results，形状对齐
// python/backtest/tradeforge_backtest/store.py 的 result_to_row()——Go 侧目前没有
// 写方法，只有读方法，所以测试数据只能这样构造，而不是调用某个 Store 方法生成。
func insertBacktestResultRow(t *testing.T, ctx context.Context, s *Store, strategyID string) string {
	t.Helper()

	metricsJSON := func(tradeCount int, sharpe float64, totalFees, finalEquity string) []byte {
		// total_fees/final_equity 按字符串写入，跟 Python 端 Metrics.to_dict() 完全一致。
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
		t.Fatalf("写入测试回测结果失败：%v", err)
	}
	return id
}

func TestBacktestResultsRoundTrip(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败（是否已 docker compose up -d？）：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "backtests-roundtrip")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	sc := types.StrategyConfig{
		ID: idgen.NewUUID(), UserID: u.ID, Name: "回测结果集成测试", Symbol: "BTCUSDT",
		Timeframe: types.TF1h, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{{Module: "volume_breakout", Params: map[string]any{}}},
		Risk:    types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State:   types.StateDraft,
	}
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("保存策略失败：%v", err)
	}

	id := insertBacktestResultRow(t, ctx, store, sc.ID)

	latest, err := store.LatestBacktestResult(ctx, u.ID, sc.ID)
	if err != nil {
		t.Fatalf("读取最近一次回测结果失败：%v", err)
	}
	if latest.ID != id {
		t.Errorf("ID = %s，期望 %s", latest.ID, id)
	}
	if latest.OutOfSample.TradeCount != 3 || latest.OutOfSample.SharpeRatio != 0.4 {
		t.Errorf("样本外指标不符：%+v", latest.OutOfSample)
	}
	if latest.InSample.TradeCount != 7 || latest.InSample.SharpeRatio != 2.5 {
		t.Errorf("样本内指标不符：%+v", latest.InSample)
	}
	// total_fees/final_equity 是 Python 端按字符串写入的 decimal 字段，必须精确往返。
	wantFees, _ := decimal.NewFromString("4.50")
	if !latest.OutOfSample.TotalFees.Equal(wantFees) {
		t.Errorf("样本外手续费 = %s，期望 %s", latest.OutOfSample.TotalFees, wantFees)
	}
	if len(latest.Segments) != 2 || latest.Segments[0].Label != "in_sample" {
		t.Errorf("segments 解析不符：%+v", latest.Segments)
	}
	if latest.FeeModel.TakerFeeRate != 0.0004 {
		t.Errorf("fee_model 解析不符：%+v", latest.FeeModel)
	}
	if len(latest.Trades) != 1 {
		t.Fatalf("trades 数量 = %d，期望 1", len(latest.Trades))
	}
	trade := latest.Trades[0]
	if trade.Segment != "out_of_sample" || trade.ExitReason != "signal" {
		t.Errorf("trade 字段不符：%+v", trade)
	}
	wantPnL, _ := decimal.NewFromString("7.125")
	if !trade.PnL.Equal(wantPnL) {
		t.Errorf("trade.PnL = %s，期望 %s（精度必须完整往返）", trade.PnL, wantPnL)
	}
	if len(trade.TriggerSignals) != 1 || trade.TriggerSignals[0].Module != "volume_breakout" {
		t.Errorf("trigger_signals 解析不符：%+v", trade.TriggerSignals)
	}
	wantCapital, _ := decimal.NewFromString("1000.123456789012345678")
	if !latest.InitialCapital.Equal(wantCapital) {
		t.Errorf("initial_capital = %s，期望 %s", latest.InitialCapital, wantCapital)
	}
	if len(latest.EquityCurve) != 3 {
		t.Fatalf("equity_curve 长度 = %d，期望 3", len(latest.EquityCurve))
	}
	wantMidEquity, _ := decimal.NewFromString("1080.00")
	wantSplitAt := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)
	if !latest.EquityCurve[1].Equity.Equal(wantMidEquity) || !latest.EquityCurve[1].Time.Equal(wantSplitAt) {
		t.Errorf("equity_curve[1] = %+v，期望 equity=%s time=%s", latest.EquityCurve[1], wantMidEquity, wantSplitAt)
	}

	list, err := store.ListBacktestResults(ctx, u.ID, sc.ID, 10)
	if err != nil {
		t.Fatalf("列出回测结果失败：%v", err)
	}
	if len(list) != 1 {
		t.Fatalf("回测结果数量 = %d，期望 1", len(list))
	}
}

func TestLatestBacktestResultErrorsWhenNeverRun(t *testing.T) {
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	store, err := Open(ctx, cfg.Postgres)
	if err != nil {
		t.Fatalf("连接 Postgres 失败：%v", err)
	}
	defer store.Close()

	u := createTestUserForStorage(ctx, t, store, "backtests-never-run")
	defer func() {
		if _, err := store.pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, u.ID); err != nil {
			t.Errorf("清理测试数据失败：%v", err)
		}
	}()

	sc := types.StrategyConfig{
		ID: idgen.NewUUID(), UserID: u.ID, Name: "从未回测过", Symbol: "BTCUSDT",
		Timeframe: types.TF1h, Combine: types.CombineAll,
		Modules: []types.ModuleConfig{{Module: "volume_breakout", Params: map[string]any{}}},
		Risk:    types.RiskConfig{MaxPositionSizeQuote: decimal.NewFromInt(1000)},
		State:   types.StateDraft,
	}
	if err := store.SaveStrategy(ctx, sc); err != nil {
		t.Fatalf("保存策略失败：%v", err)
	}

	if _, err := store.LatestBacktestResult(ctx, u.ID, sc.ID); err == nil {
		t.Fatal("从未回测过的策略应当返回错误")
	}
}
