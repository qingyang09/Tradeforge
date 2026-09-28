// Command agent-service 跑通"一句话 → Agent 复述 → 用户确认 → 配置入库"的完整闭环。
//
// 用法：
//
//	go run ./cmd/agent-service            # 交互模式，需要 ANTHROPIC_API_KEY
//	go run ./cmd/agent-service -schema    # 只打印 Agent 使用的 JSON Schema 与提示词
//
// 未配置 API 密钥时会明确报错并退出，绝不静默降级成"假装翻译"。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"tradeforge/internal/agent"
	"tradeforge/internal/config"
	"tradeforge/internal/modules"
	"tradeforge/internal/storage"
	"tradeforge/pkg/idgen"
	"tradeforge/pkg/types"
)

func main() {
	showSchema := flag.Bool("schema", false, "打印 JSON Schema 与 system prompt 后退出")
	dryRun := flag.Bool("dry-run", false, "确认后只打印配置，不写入数据库")
	flag.Parse()

	cfg := config.Load()
	reg := modules.NewDefaultRegistry()

	if *showSchema {
		printSchemaAndPrompt(reg)
		return
	}

	llm, err := agent.NewAnthropicLLM(cfg.Agent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "无法初始化 Agent：%v\n\n"+
			"请先设置 ANTHROPIC_API_KEY 环境变量，或用 -schema 查看提示词与 Schema。\n", err)
		os.Exit(1)
	}

	a := agent.New(llm, reg, cfg.Agent.MaxRetries)
	ctx := context.Background()

	var store *storage.Store
	if !*dryRun {
		store, err = storage.Open(ctx, cfg.Postgres)
		if err != nil {
			fmt.Fprintf(os.Stderr, "连接数据库失败：%v\n（可加 -dry-run 跳过入库）\n", err)
			os.Exit(1)
		}
		defer store.Close()
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	fmt.Println("用一句话描述你想执行的交易规则（输入空行退出）。")
	fmt.Println("提示：本工具只负责把你的规则翻译成配置，不提供任何投资建议。")

	for {
		fmt.Print("\n> ")
		if !in.Scan() {
			return
		}
		utterance := strings.TrimSpace(in.Text())
		if utterance == "" {
			return
		}

		proposal, history, err := runClarificationLoop(ctx, a, in, utterance)
		if err != nil {
			fmt.Fprintf(os.Stderr, "翻译失败：%v\n", err)
			continue
		}
		if proposal == nil {
			continue // 用户中途放弃
		}
		_ = history

		if !confirmAndSave(ctx, a, in, store, proposal, *dryRun) {
			continue
		}
	}
}

// runClarificationLoop 反复翻译，直到 Agent 不再提问或用户放弃。
func runClarificationLoop(
	ctx context.Context, a *agent.Agent, in *bufio.Scanner, utterance string,
) (*agent.Proposal, []agent.Turn, error) {
	var history []agent.Turn
	current := utterance

	for round := 0; round < 5; round++ {
		p, err := a.Translate(ctx, current, history)
		if err != nil {
			return nil, nil, err
		}
		if !p.NeedsClarification() {
			return p, history, nil
		}

		fmt.Println("\n还需要你补充几点信息：")
		for i, q := range p.Questions {
			fmt.Printf("  %d) %s\n", i+1, q)
		}
		fmt.Print("\n你的回答（空行放弃）> ")
		if !in.Scan() {
			return nil, nil, nil
		}
		answer := strings.TrimSpace(in.Text())
		if answer == "" {
			fmt.Println("已放弃本次翻译。")
			return nil, nil, nil
		}

		history = append(history,
			agent.Turn{Role: "user", Text: current},
			agent.Turn{Role: "assistant", Text: strings.Join(p.Questions, "\n")},
		)
		current = answer
	}
	return nil, nil, errors.New("澄清轮次过多，请把规则描述得更完整一些后重试")
}

// confirmAndSave 展示复述、等待用户确认，确认后才写库。
func confirmAndSave(
	ctx context.Context, a *agent.Agent, in *bufio.Scanner,
	store *storage.Store, p *agent.Proposal, dryRun bool,
) bool {
	fmt.Println("\n" + strings.Repeat("─", 70))
	fmt.Println("我理解你的策略是这样：")
	fmt.Println()
	fmt.Println("  " + strings.ReplaceAll(p.Restatement, "\n", "\n  "))
	fmt.Println()
	printConfigSummary(*p.Config)
	fmt.Println(strings.Repeat("─", 70))
	fmt.Print("确认无误请输入 y，其它任意输入表示重来 > ")

	if !in.Scan() || strings.ToLower(strings.TrimSpace(in.Text())) != "y" {
		fmt.Println("已取消，配置未写入系统。")
		return false
	}

	cfg, err := a.Confirm(p, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "确认失败：%v\n", err)
		return false
	}
	cfg.ID = idgen.NewUUID()

	if dryRun {
		blob, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Printf("\n[dry-run] 未写库。配置内容：\n%s\n", blob)
		return true
	}

	if err := store.SaveStrategy(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "写入数据库失败：%v\n", err)
		return false
	}
	if err := store.RecordTransition(ctx, storage.Transition{
		StrategyID: cfg.ID, From: "", To: types.StateDraft,
		Actor: "user:cli", Reason: "用户确认了 Agent 的翻译结果",
		Evidence: map[string]any{"source_utterance": cfg.SourceUtterance, "attempts": p.Attempts},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "写入审计记录失败：%v\n", err)
	}

	fmt.Printf("\n已保存，策略 ID：%s（当前状态：%s）\n", cfg.ID, cfg.State)
	fmt.Println("下一步：运行回测。未通过回测与模拟盘之前，该策略无法进入实盘。")
	return true
}

func printConfigSummary(c types.StrategyConfig) {
	fmt.Printf("  标的：%s    周期：%s    组合方式：%s", c.Symbol, c.Timeframe, c.Combine)
	if c.Combine == types.CombineWeighted {
		fmt.Printf("（阈值 %.2f）", c.Threshold)
	}
	fmt.Println("\n  模块：")
	for _, m := range c.Modules {
		fmt.Printf("    - %s", m.Module)
		if m.Weight > 0 {
			fmt.Printf("（权重 %.2f）", m.Weight)
		}
		if len(m.Params) > 0 {
			fmt.Printf("  参数 %v", m.Params)
		} else {
			fmt.Print("  参数：全部使用系统默认值")
		}
		fmt.Println()
	}
	fmt.Printf("  风控：单笔上限 %s", c.Risk.MaxPositionSizeQuote)
	if c.Risk.MaxDailyLossQuote.IsPositive() {
		fmt.Printf("，单日亏损上限 %s", c.Risk.MaxDailyLossQuote)
	}
	if c.Risk.StopLossPct > 0 {
		fmt.Printf("，止损 %.2f%%", c.Risk.StopLossPct*100)
	}
	if c.Risk.MaxHoldingPeriod > 0 {
		fmt.Printf("，最长持仓 %s", c.Risk.MaxHoldingPeriod)
	}
	fmt.Println()
}

func printSchemaAndPrompt(reg *modules.Registry) {
	a := agent.New(nil, reg, 0)
	blob, _ := json.MarshalIndent(a.Schema(), "", "  ")
	fmt.Println("=== JSON Schema ===")
	fmt.Println(string(blob))
	fmt.Println("\n=== System Prompt ===")
	fmt.Println(a.SystemPrompt())
}
