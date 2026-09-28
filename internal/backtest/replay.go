// Package backtest 实现信号重放：把一份 StrategyConfig 在历史 K 线上逐根跑一遍
// internal/engine，产出决策流。
//
// 为什么信号重放放在 Go 而不是 Python：回测和实盘必须用同一套信号逻辑。如果
// Python 里再实现一遍支撑阻力、CVD 等模块，两份实现迟早会漂移——那时回测结果就
// 成了对另一个策略的评估，比没有回测更危险。这里直接复用 internal/engine，
// Python 侧（python/backtest）只负责它真正擅长的部分：撮合模拟、手续费滑点建模
// 与绩效统计。
//
// 这份逻辑原本直接写在 cmd/backtest-runner 里，只有 CLI 一个调用方；画板加了
// "运行回测"按钮后（internal/webui），webui 进程需要在内存里直接跑一遍重放，
// 而不是先落盘再拉起子进程去读——所以把它提出来做成一个包，CLI 和 webui
// 共用同一份实现，不允许出现第二份重放循环。
package backtest

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"tradeforge/internal/engine"
	"tradeforge/internal/modules"
	"tradeforge/pkg/types"
)

// EngineVersion 标注决策流由哪一版信号逻辑产出，会写进回测结果——CLI 和 webui
// 触发的回测现在共用同一份 Replay 实现，理应共用同一个版本号。
const EngineVersion = "signal-replay/1.0.0"

// DefaultWindow 是喂给模块的最大历史长度。
//
// 逐根重放时若每次都传入全部历史，复杂度是 O(n²)；
// 而模块的回看窗口有上限（当前最大 1000），给出足够裕量即可。
const DefaultWindow = 1200

// Meta 是一次重放的头部元信息。
type Meta struct {
	Type          string    `json:"type"`
	EngineVersion string    `json:"engine_version"`
	StrategyID    string    `json:"strategy_id"`
	Symbol        string    `json:"symbol"`
	Timeframe     string    `json:"timeframe"`
	Combine       string    `json:"combine"`
	BarCount      int       `json:"bar_count"`
	DataStart     time.Time `json:"data_start"`
	DataEnd       time.Time `json:"data_end"`
	GeneratedAt   time.Time `json:"generated_at"`
}

// DecisionLine 是重放过程中某一根K线产出的决策，字段形状与
// python/backtest/tradeforge_backtest/loaders.py 认得的 JSONL 一行完全对应
// （decision_from_iter 按这些字段名解析），不能改。
type DecisionLine struct {
	Type      string         `json:"type"`
	Index     int            `json:"index"`
	BarTime   time.Time      `json:"bar_time"`
	Direction string         `json:"direction"`
	Score     float64        `json:"score"`
	Triggered bool           `json:"triggered"`
	Price     string         `json:"price"`
	Reason    string         `json:"reason"`
	Signals   []types.Signal `json:"signals"`
}

// Replay 按触发周期逐根 K 线重放，返回头部元信息与每一根的决策。
//
// 关键点：第 i 根触发周期 K 线的决策只使用 candles[:i+1]；contextFeeds 里其它
// （更慢）周期的行情同样只暴露"到这一刻为止真实已收盘"的部分——用
// types.AlignAsOf 按当前这根触发 K 线的收盘时间裁剪，绝不触碰任何周期的未来数据。
// 这是回测能不能信的根本前提，比任何指标都重要。
func Replay(
	ctx context.Context, cfg types.StrategyConfig, md types.MarketData,
	contextFeeds map[types.Timeframe]types.MarketData,
	window int, logger *slog.Logger,
) (Meta, []DecisionLine, error) {
	if len(md.Candles) == 0 {
		return Meta{}, nil, fmt.Errorf("没有K线数据")
	}
	e := engine.New(modules.NewDefaultRegistry(), engine.WithLogger(logger))

	meta := Meta{
		Type:          "meta",
		EngineVersion: EngineVersion,
		StrategyID:    cfg.ID,
		Symbol:        cfg.Symbol,
		Timeframe:     string(cfg.Timeframe),
		Combine:       string(cfg.Combine),
		BarCount:      len(md.Candles),
		DataStart:     md.Candles[0].OpenTime,
		DataEnd:       md.Candles[len(md.Candles)-1].CloseTime,
		GeneratedAt:   time.Now().UTC(),
	}

	decisions := make([]DecisionLine, 0, len(md.Candles))
	for i := range md.Candles {
		start := 0
		if window > 0 && i+1 > window {
			start = i + 1 - window
		}
		cutoff := md.Candles[i].CloseTime
		feeds := map[types.Timeframe]types.MarketData{
			cfg.Timeframe: {
				Symbol:    md.Symbol,
				Timeframe: md.Timeframe,
				Candles:   md.Candles[start : i+1],
			},
		}
		for tf, ctxMD := range contextFeeds {
			aligned := types.AlignAsOf(ctxMD.Candles, cutoff)
			if window > 0 && len(aligned) > window {
				aligned = aligned[len(aligned)-window:]
			}
			feeds[tf] = types.MarketData{Symbol: ctxMD.Symbol, Timeframe: tf, Candles: aligned}
		}

		d, err := e.Evaluate(ctx, cfg, feeds)
		if err != nil {
			return Meta{}, nil, fmt.Errorf("第 %d 根 K 线评估失败：%w", i+1, err)
		}

		decisions = append(decisions, DecisionLine{
			Type:      "decision",
			Index:     i,
			BarTime:   md.Candles[i].CloseTime,
			Direction: string(d.Direction),
			Score:     d.Score,
			Triggered: d.Triggered,
			Price:     d.Price.String(),
			Reason:    d.Reason,
			Signals:   d.Signals,
		})
	}
	return meta, decisions, nil
}
