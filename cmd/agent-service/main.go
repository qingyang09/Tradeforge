// Command agent-service runs the full loop: "one sentence -> Agent
// restates it -> user confirms -> config saved."
//
// Usage:
//
//	go run ./cmd/agent-service            # interactive mode, needs ANTHROPIC_API_KEY
//	go run ./cmd/agent-service -schema    # just print the Agent's JSON Schema and prompt
//
// If no API key is configured, it fails loudly and exits — it never
// silently falls back to "pretend to translate."
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
	showSchema := flag.Bool("schema", false, "print the JSON Schema and system prompt, then exit")
	dryRun := flag.Bool("dry-run", false, "after confirmation, only print the config instead of writing it to the database")
	flag.Parse()

	cfg := config.Load()
	reg := modules.NewDefaultRegistry()

	if *showSchema {
		printSchemaAndPrompt(reg)
		return
	}

	llm, err := agent.NewAnthropicLLM(cfg.Agent)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize Agent: %v\n\n"+
			"Set the ANTHROPIC_API_KEY environment variable first, or use -schema to view the prompt and schema.\n", err)
		os.Exit(1)
	}

	a := agent.New(llm, reg, cfg.Agent.MaxRetries)
	ctx := context.Background()

	var store *storage.Store
	if !*dryRun {
		store, err = storage.Open(ctx, cfg.Postgres)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to connect to database: %v\n(add -dry-run to skip saving)\n", err)
			os.Exit(1)
		}
		defer store.Close()
	}

	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	fmt.Println("Describe the trading rule you want to run in one sentence (enter a blank line to quit).")
	fmt.Println("Note: this tool only translates your rule into a config; it does not offer any investment advice.")

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
			fmt.Fprintf(os.Stderr, "Translation failed: %v\n", err)
			continue
		}
		if proposal == nil {
			continue // user gave up partway through
		}
		_ = history

		if !confirmAndSave(ctx, a, in, store, proposal, *dryRun) {
			continue
		}
	}
}

// runClarificationLoop translates repeatedly until the Agent stops asking
// questions or the user gives up.
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

		fmt.Println("\nA few more details are needed:")
		for i, q := range p.Questions {
			fmt.Printf("  %d) %s\n", i+1, q)
		}
		fmt.Print("\nYour answer (blank line to give up) > ")
		if !in.Scan() {
			return nil, nil, nil
		}
		answer := strings.TrimSpace(in.Text())
		if answer == "" {
			fmt.Println("Translation abandoned.")
			return nil, nil, nil
		}

		history = append(history,
			agent.Turn{Role: "user", Text: current},
			agent.Turn{Role: "assistant", Text: strings.Join(p.Questions, "\n")},
		)
		current = answer
	}
	return nil, nil, errors.New("too many clarification rounds; describe the rule more completely and try again")
}

// confirmAndSave shows the restatement, waits for user confirmation, and
// only saves to the database after confirmation.
func confirmAndSave(
	ctx context.Context, a *agent.Agent, in *bufio.Scanner,
	store *storage.Store, p *agent.Proposal, dryRun bool,
) bool {
	fmt.Println("\n" + strings.Repeat("─", 70))
	fmt.Println("Here's my understanding of your strategy:")
	fmt.Println()
	fmt.Println("  " + strings.ReplaceAll(p.Restatement, "\n", "\n  "))
	fmt.Println()
	printConfigSummary(*p.Config)
	fmt.Println(strings.Repeat("─", 70))
	fmt.Print("Enter y to confirm, anything else to start over > ")

	if !in.Scan() || strings.ToLower(strings.TrimSpace(in.Text())) != "y" {
		fmt.Println("Cancelled; the config was not saved.")
		return false
	}

	cfg, err := a.Confirm(p, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Confirmation failed: %v\n", err)
		return false
	}
	cfg.ID = idgen.NewUUID()

	if dryRun {
		blob, _ := json.MarshalIndent(cfg, "", "  ")
		fmt.Printf("\n[dry-run] Not saved. Config:\n%s\n", blob)
		return true
	}

	if err := store.SaveStrategy(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save to database: %v\n", err)
		return false
	}
	if err := store.RecordTransition(ctx, storage.Transition{
		StrategyID: cfg.ID, From: "", To: types.StateDraft,
		Actor: "user:cli", Reason: "user confirmed the Agent's translation result",
		Evidence: map[string]any{"source_utterance": cfg.SourceUtterance, "attempts": p.Attempts},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to write audit record: %v\n", err)
	}

	fmt.Printf("\nSaved. Strategy ID: %s (current state: %s)\n", cfg.ID, cfg.State)
	fmt.Println("Next step: run a backtest. The strategy can't go live until it passes backtesting and paper trading.")
	return true
}

func printConfigSummary(c types.StrategyConfig) {
	fmt.Printf("  Symbol: %s    Timeframe: %s    Combine: %s", c.Symbol, c.Timeframe, c.Combine)
	if c.Combine == types.CombineWeighted {
		fmt.Printf(" (threshold %.2f)", c.Threshold)
	}
	fmt.Println("\n  Modules:")
	for _, m := range c.Modules {
		fmt.Printf("    - %s", m.Module)
		if m.Weight > 0 {
			fmt.Printf(" (weight %.2f)", m.Weight)
		}
		if len(m.Params) > 0 {
			fmt.Printf("  params %v", m.Params)
		} else {
			fmt.Print("  params: all system defaults")
		}
		fmt.Println()
	}
	fmt.Printf("  Risk: max position %s", c.Risk.MaxPositionSizeQuote)
	if c.Risk.MaxDailyLossQuote.IsPositive() {
		fmt.Printf(", max daily loss %s", c.Risk.MaxDailyLossQuote)
	}
	if c.Risk.StopLossPct > 0 {
		fmt.Printf(", stop loss %.2f%%", c.Risk.StopLossPct*100)
	}
	if c.Risk.MaxHoldingPeriod > 0 {
		fmt.Printf(", max holding period %s", c.Risk.MaxHoldingPeriod)
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
