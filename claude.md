

## Project Charter

```
You are helping me build a prototype (MVP) of a modular cryptocurrency trading strategy
platform. Core principles:

1. The platform ships several independent, parameterized "trading signal modules"
   (support/resistance, volume breakout, CVD/order flow, MACD/RSI, news sentiment). These
   modules are decoupled from each other and each emits a standardized signal.
2. Users describe trading strategies in natural language; an AI Agent translates that
   sentence into a structured configuration (JSON) describing "which modules, how they're
   combined, and what the parameters are." The Agent must first restate "here's my
   understanding of your strategy" back to the user for confirmation — it must never
   execute anything without that confirmation.
3. Any newly generated strategy configuration must pass through backtesting (historical
   data + fee/slippage modeling) -> paper trading (run for a period of time) -> the user
   manually unlocking it before it can go live. This is a mandatory pipeline that cannot be
   skipped.
4. Different trading symbols (e.g. BTC, ETH) can have completely different module
   combinations — this is a first-class capability from day one, not bolted on later.
5. The system's only job is to "faithfully execute the user's rules." It must never
   generate copy like "you should buy this" or "this combination would work better" — this
   is a compliance red line. Both the translation layer's prompt design and every
   user-facing piece of copy must hold this boundary.

Tech stack:
- Backend: Go (module engine, execution layer, gRPC services), Python (backtest engine,
  using vectorbt or a custom implementation)
- Message queue: Kafka (decouples signal propagation between modules)
- AI Agent layer: calls an LLM API, constrains output with a strict JSON Schema, never
  allows free-form text to enter the execution chain directly
- Data sources: start with exchanges' public REST/WebSocket APIs (e.g. Binance); CVD data
  can later be sourced from Coinglass
- Storage: PostgreSQL (strategy configs, backtest results, trade records), Redis
  (real-time signal cache)

Code style requirements:
- Go code follows the standard project layout (cmd/ internal/ pkg/), one package per module
- All monetary amounts/prices use a decimal type — float64 is forbidden for financial math
- Every stage must end with runnable unit tests; don't write code that merely "looks like
  it works"
```

---

## Stage 0: Project scaffolding

```
Initialize a Go project with module name tradeforge (can be customized). Requirements:

1. Set up the directory structure following the standard Go project layout:
   - cmd/ (entry points for each executable: signal-engine, agent-service,
     backtest-runner, executor)
   - internal/modules/ (trading signal modules)
   - internal/engine/ (composition engine / DAG executor)
   - internal/agent/ (AI Agent translation layer)
   - internal/backtest/ (backtest engine; can leave a Python subdirectory
     python/backtest/ for now)
   - internal/execution/ (execution layer)
   - internal/storage/ (database access layer)
   - pkg/types/ (data structures shared across modules: signals, strategy configs,
     orders, etc.)
   - configs/ (config file templates)
   - docker-compose.yml (spins up Postgres + Redis + Kafka locally)

2. Define the core data structures in pkg/types (structs only for now, no logic):
   - Signal (module output: direction, confidence, timestamp, source module, raw data)
   - ModuleConfig (module name + parameter map)
   - StrategyConfig (symbol + module combination + combination logic + risk parameters)
   - BacktestResult (return, Sharpe ratio, max drawdown, win rate, etc.)

3. Write a minimal working docker-compose that brings up Postgres/Redis/Kafka, and a
   health-check script confirming all three services are reachable.

Finish this step first — get docker-compose and the directory structure working before
moving on. Don't write business logic ahead of time.
```

---

## Stage 1: Trading signal module library

```
Implement the first 3 signal modules under internal/modules/. Every module must implement
a unified interface:

  type SignalModule interface {
      Name() string
      RequiredParams() []ParamSpec  // param name, type, default value, valid range
      Evaluate(ctx context.Context, marketData MarketData, params map[string]any) (Signal, error)
  }

Implement in this order:

1. support_resistance module: clusters recent N candles' highs/lows to compute
   support/resistance levels. Parameters include timeframe, lookback window, and
   clustering tolerance. Emits a signal when price touches/breaks a key level.

2. volume_breakout module: computes volume as a multiple of its recent average, emitting a
   signal when the multiple exceeds a threshold. Parameters include the averaging window
   and the multiple threshold.

3. cvd_orderflow module: computes cumulative volume delta (CVD) and detects divergence or
   imbalance. Parameters include the computation window and the imbalance threshold. Start
   with a simulated/placeholder data source; leave an extension point at the interface
   layer for wiring in real Coinglass data later.

For each module:
- Write unit tests against real or simulated historical data, covering the normal case,
  edge cases (insufficient data), and invalid input
- Run Evaluate once against fake data and print the resulting Signal struct to manually
  confirm the logic is sound

Don't implement all the modules at once — build these 3 first and get the tests passing;
I'll confirm the logic is sound before we add MACD/RSI and news sentiment.
```

---

## Stage 2: Composition engine

```
Implement the module composition engine under internal/engine/. Responsibilities:

1. Read a StrategyConfig (symbol + a set of ModuleConfigs + combination logic)
2. Concurrently call each corresponding SignalModule.Evaluate and collect all Signals
3. Aggregate them into a final decision per the combination logic. Support the two
   simplest aggregation modes first:
   - ALL: triggers only if every module emits a signal in the same direction
   - WEIGHTED: each module has a weight; triggers once the weighted confidence exceeds a
     threshold
4. Write the aggregated result to a Kafka topic (for the execution layer/backtest engine
   to consume) and persist it to Postgres for audit purposes

Requirements:
- A single module erroring out or timing out must not take down the whole engine — there
  must be timeout and degradation handling (that module's signal is recorded as
  neutral/skipped, with a log entry)
- Write an integration test: construct a StrategyConfig with 3 modules, run the full
  chain against fake data, and verify the aggregation logic is correct
```

---

## Stage 3: AI Agent translation layer

```
Implement the natural-language-to-StrategyConfig translation layer under internal/agent/.
This is the most critical, most carefully-designed part of the entire project.

1. Design a strict JSON Schema constraining the LLM's output to a valid StrategyConfig
   (symbol, module list, each module's parameters, combination logic, risk parameters).
   Any output that doesn't conform to the schema is rejected outright — no "best-effort
   repair."

2. System prompt design points:
   - Only allow selecting from the registered module list (the ones implemented in Stage
     1); the LLM must never invent modules that don't exist
   - Every module's parameters must fall within the range defined by RequiredParams —
     out-of-range values must be rejected or clamped, with the user explicitly informed
   - The output and any copy shown to the user must never contain investment-advice
     language like "I suggest," "I recommend," or "this would work better" — the Agent's
     role must always be "translating the user's rules," never "giving the user advice"

3. Implement a "confirmation loop": after the Agent generates a config, it must first
   restate "here's my understanding of your strategy" in plain language for the user to
   see. Only after the user confirms or requests changes does the config get formally
   written into the system — it must never be generated and then executed directly.

4. Write test cases: at least 10 "natural language input -> expected StrategyConfig"
   contract pairs, including a few deliberately vague/ambiguous inputs, verifying that the
   Agent asks a clarifying question rather than guessing parameters on its own when faced
   with ambiguity.

Wire this up against only the 3 modules implemented in Stage 1 first, and get the full
"one sentence -> confirm -> config" loop working end to end before considering hooking up
more modules.
```

---

## Stage 4: Backtest engine

```
Implement the backtest engine in python/backtest/ (or another language if you judge it
more suitable after evaluation):

1. Input: StrategyConfig + historical market data (first support pulling from local CSV /
   an exchange's historical candle API)
2. Strictly model fees and slippage — this must not be a "bare price" backtest
3. Output a BacktestResult: total return, annualized return, Sharpe ratio, Sortino ratio,
   max drawdown, win rate, profit factor, trade count
4. Implement "out-of-sample testing": split historical data into a training segment and a
   test segment, and explicitly label whether the strategy's parameters were validated
   separately on the test segment — this prevents an overfit result from being mistaken
   for "real performance" when shown to the user
5. Persist results to Postgres, for later display and for the "unlock paper trading" flow
   to use

Write an end-to-end test: run a full backtest against one real strategy config generated
in Stage 3, and manually verify a handful of key trade points' signal triggers match
expectations (don't just look at the summary metrics — spot-check actual trades).
```

---

## Stage 5: Verification loop (paper-trading gate)

```
Implement a strategy state machine that governs the mandatory pipeline a strategy follows
from "draft" to "live":

  DRAFT -> BACKTESTED -> PAPER_TRADING -> LIVE_ELIGIBLE -> LIVE

Rules:
1. A new strategy must first pass the Stage 4 backtest, and the backtest result must clear
   a configurable minimum bar (e.g. out-of-sample Sharpe > 0) before it can enter
   PAPER_TRADING
2. During PAPER_TRADING, the strategy trades against real-time market data in simulation
   (no real orders are placed). It must run for at least a configurable minimum duration
   (e.g. 7 days) or a minimum number of trades before the state is allowed to advance to
   LIVE_ELIGIBLE
3. LIVE_ELIGIBLE -> LIVE must be an explicit, manual action by the user — the system must
   never advance this automatically
4. Every state transition must be recorded in an audit log (who, when, and based on what
   data the transition was made)

Write tests covering every illegal transition in the state machine (e.g. attempting to
jump straight from DRAFT to LIVE) — all of them must be rejected.
```

---

## Stage 6: Multi-symbol execution layer

```
Implement the execution layer under internal/execution/. The core requirement: different
symbols can have completely independent module combinations and risk parameters, with zero
cross-impact.

1. Every StrategyConfig that enters the LIVE state is bound to its own independent
   execution instance (a goroutine or a dedicated worker); execution across symbols is
   fully isolated — an exception on one symbol must not affect any other symbol
2. Risk parameters are configured independently per symbol: max position size per trade,
   max daily loss, max holding time, etc. Tripping a risk control immediately closes the
   position and pauses that symbol's strategy (without affecting other symbols)
3. First integrate against one exchange's testnet/paper account API (e.g. Binance
   Testnet) — don't connect to a real funded account from the start
4. Every order execution must record "which module's which signal, with what parameters,
   triggered this" to satisfy the explainability requirement mentioned in Stage 2 — this
   record must be displayable to the user in the UI later on

Write an integration test: simulate two symbols running simultaneously (BTC using
combination A, ETH using combination B), and verify they're fully isolated from each other
and that risk controls take effect independently.
```

---

## Stage 7 (optional, revisit based on remaining effort): Minimal usable interface

```
Use a simple web interface (can start with Go + HTMX, or a lightweight frontend framework)
to display:
1. A strategy configuration wizard: natural-language input -> show the Agent's restated
   understanding -> confirm/revise
2. A strategy status board: which stage of the state machine every strategy currently sits
   in
3. A single strategy's detail view: backtest result charts, paper/live trade records, and
   which module + parameters triggered each trade

This stage has lower priority than Stages 0-6 — get the backend loop working end to end
first; the UI can start out as bare-bones as long as it can show real data.
```

---
