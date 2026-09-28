// Command backtest-runner 在历史 K 线上逐根重放策略，输出决策流（JSONL）。
//
// 重放逻辑本身在 internal/backtest（原本直接写在这个命令里，画板加了"运行回测"
// 按钮后 internal/webui 也需要在内存里跑一遍同样的重放，所以提出来做成一个包共用，
// 不允许 CLI 和 webui 各跑一份可能漂移的重放实现）。
//
// 用法：
//
//	backtest-runner -strategy s.json -candles btc.csv > decisions.jsonl
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"tradeforge/internal/backtest"
	"tradeforge/internal/marketdata"
	"tradeforge/pkg/types"
)

// contextCandlesFlag 收集重复出现的 -context-candles timeframe=path 参数，供多周期
// 策略提供除触发周期以外、模块用到的其它（更慢）周期的历史 K 线。标准库 flag 不支持
// 重复 flag 收集成切片，用 flag.Value 接口自己实现。
type contextCandlesFlag map[types.Timeframe]string

func (f contextCandlesFlag) String() string {
	parts := make([]string, 0, len(f))
	for tf, path := range f {
		parts = append(parts, string(tf)+"="+path)
	}
	return strings.Join(parts, ",")
}

func (f contextCandlesFlag) Set(value string) error {
	tf, path, ok := strings.Cut(value, "=")
	if !ok || tf == "" || path == "" {
		return fmt.Errorf("格式应为 timeframe=path，实际 %q", value)
	}
	f[types.Timeframe(tf)] = path
	return nil
}

func main() {
	strategyPath := flag.String("strategy", "", "策略配置 JSON 文件路径（必填）")
	candlesPath := flag.String("candles", "", "触发周期的历史 K 线 CSV 文件路径（必填）")
	outPath := flag.String("out", "", "输出文件路径，留空则写到标准输出")
	window := flag.Int("window", backtest.DefaultWindow, "喂给模块的最大历史 K 线根数")
	quiet := flag.Bool("quiet", false, "抑制模块降级等警告日志")
	contextCandles := make(contextCandlesFlag)
	flag.Var(contextCandles, "context-candles", "多周期策略里除触发周期外，其它周期的历史 K 线，"+
		"格式 timeframe=path，可重复指定（如 -context-candles 1h=btc_1h.csv）")
	flag.Parse()

	if *strategyPath == "" || *candlesPath == "" {
		flag.Usage()
		os.Exit(2)
	}

	cfg, err := loadStrategy(*strategyPath)
	if err != nil {
		fatal("读取策略配置失败：%v", err)
	}

	md, err := marketdata.LoadCSV(*candlesPath, cfg.Symbol, cfg.Timeframe)
	if err != nil {
		fatal("读取行情失败：%v", err)
	}

	// 校验策略需要的每个周期（触发周期本身除外）都提供了对应的 -context-candles，
	// 缺一个就直接报错退出，而不是悄悄跳过、让那个模块在整场回测里全程降级。
	contextFeeds := make(map[types.Timeframe]types.MarketData, len(contextCandles))
	for _, tf := range cfg.RequiredTimeframes() {
		if tf == cfg.Timeframe {
			continue
		}
		path, ok := contextCandles[tf]
		if !ok {
			fatal("策略需要 %s 周期的行情，但未提供对应的 -context-candles %s=<path>", tf, tf)
		}
		cmd, err := marketdata.LoadCSV(path, cfg.Symbol, tf)
		if err != nil {
			fatal("读取 %s 周期的行情失败：%v", tf, err)
		}
		contextFeeds[tf] = cmd
	}

	logger := slog.Default()
	if *quiet {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	meta, decisions, err := backtest.Replay(context.Background(), cfg, md, contextFeeds, *window, logger)
	if err != nil {
		fatal("重放失败：%v", err)
	}

	out := io.Writer(os.Stdout)
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			fatal("创建输出文件失败：%v", err)
		}
		defer f.Close()
		out = f
	}
	if err := backtest.WriteJSONL(out, meta, decisions); err != nil {
		fatal("写出决策流失败：%v", err)
	}
}

func loadStrategy(path string) (types.StrategyConfig, error) {
	blob, err := os.ReadFile(path)
	if err != nil {
		return types.StrategyConfig{}, err
	}
	var cfg types.StrategyConfig
	dec := json.NewDecoder(bytes.NewReader(blob))
	// 拒绝未知字段：策略文件里多出的字段往往是手改时的笔误，
	// 静默忽略会让人以为改动生效了。
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return types.StrategyConfig{}, fmt.Errorf("解析策略 JSON 失败：%w", err)
	}
	return cfg, nil
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
