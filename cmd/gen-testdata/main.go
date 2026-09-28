// Command gen-testdata 生成端到端回测所需的样例数据：
// 一份合成 K 线 CSV 和一份策略配置 JSON。
//
// 合成而非真实数据是有意的：端到端测试要验证的是"链路是否跑通、
// 关键点位是否按预期触发"，用可复现的数据才能把断言写死。
// 接真实交易所数据是另一件事，属于数据源模块的职责。
//
// 用法：go run ./cmd/gen-testdata -dir testdata
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shopspring/decimal"

	"tradeforge/internal/marketdata"
	"tradeforge/internal/marketdata/synth"
	"tradeforge/pkg/types"
)

func main() {
	dir := flag.String("dir", "testdata", "输出目录")
	seed := flag.Int64("seed", 20250101, "随机游走的种子，固定种子保证结果可复现")
	flag.Parse()

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fatal("创建目录失败：%v", err)
	}

	md := buildMarketData(*seed)
	csvPath := filepath.Join(*dir, "btcusdt_1h.csv")
	f, err := os.Create(csvPath)
	if err != nil {
		fatal("创建 CSV 失败：%v", err)
	}
	if err := marketdata.WriteCSV(f, md); err != nil {
		f.Close()
		fatal("写 CSV 失败：%v", err)
	}
	f.Close()

	cfg := buildStrategy()
	jsonPath := filepath.Join(*dir, "strategy.json")
	blob, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		fatal("序列化策略失败：%v", err)
	}
	if err := os.WriteFile(jsonPath, blob, 0o644); err != nil {
		fatal("写策略文件失败：%v", err)
	}

	fmt.Printf("已生成：\n  %s（%d 根 K 线，%s ~ %s）\n  %s\n",
		csvPath, len(md.Candles),
		md.Candles[0].OpenTime.Format("2006-01-02"),
		md.Candles[len(md.Candles)-1].CloseTime.Format("2006-01-02"),
		jsonPath)
}

// buildMarketData 构造一段结构明确的行情，让端到端断言有据可依。
//
// 刻意安排了两个完全一样的触发形态，一个落在样本内、一个落在样本外
// （按默认 70/30 切分，分割点在第 350 根）：
//
//	  0~199：在 100~110 之间震荡，形成被反复触及的关键位
//	200~218：主动买盘占优的推升，把 CVD 失衡度拉起来
//	    219：放量 3 倍并突破 110  ← 触发点 #1（样本内）
//	220~279：随机游走
//	280~399：在 120~130 之间震荡，形成新的关键位
//	400~418：再一次主动买盘占优的推升
//	    419：放量 3 倍并突破 130  ← 触发点 #2（样本外）
//	420~499：随机游走
func buildMarketData(seed int64) types.MarketData {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	b := synth.New("BTCUSDT", types.TF1h, start)

	b.Oscillate(200, 100, 110, 1000)
	b.Trend(19, 100, 110, 1000, 0.85)
	b.AddBar(110, 113, 3000, 0.85) // 触发点 #1，下标 219
	b.RandomWalk(60, 113, 0.008, 1000, seed)

	b.Oscillate(120, 120, 130, 1000)
	b.Trend(19, 120, 130, 1000, 0.85)
	b.AddBar(130, 134, 3000, 0.85) // 触发点 #2，下标 419
	b.RandomWalk(80, 134, 0.008, 1000, seed+1)

	return b.Build()
}

// buildStrategy 是一份"三模块 ALL 组合"的策略配置，
// 形态与 Agent 翻译层的产出一致。
func buildStrategy() types.StrategyConfig {
	return types.StrategyConfig{
		ID:        "9f8e7d6c-5b4a-4392-8180-1a2b3c4d5e6f",
		Name:      "BTC 突破放量订单流三重确认",
		Symbol:    "BTCUSDT",
		Timeframe: types.TF1h,
		Combine:   types.CombineAll,
		Modules: []types.ModuleConfig{
			{Module: "support_resistance", Params: map[string]any{
				"pivot_strength": 1, "min_touches": 2,
			}},
			{Module: "volume_breakout", Params: map[string]any{
				"window": 20, "multiplier": 2.0,
			}},
			{Module: "cvd_orderflow", Params: map[string]any{
				"window": 50, "imbalance_threshold": 0.2,
			}},
		},
		Risk: types.RiskConfig{
			MaxPositionSizeQuote: decimal.NewFromInt(1000),
			MaxDailyLossQuote:    decimal.NewFromInt(200),
			StopLossPct:          0.03,
			TakeProfitPct:        0.06,
			MaxHoldingPeriod:     types.D(24 * time.Hour),
		},
		State:           types.StateDraft,
		SourceUtterance: "BTC 一小时线，突破关键阻力位、同时放量到均量两倍、且主动买盘占优时做多；单笔最多 1000U，止损 3%，止盈 6%，最多持仓一天",
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
