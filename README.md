*中文说明: [README.zh-CN.md](README.zh-CN.md)*

---

# TradeForge

A modular cryptocurrency trading strategy platform (MVP). Users describe trading rules in
natural language; an AI Agent translates that into a structured configuration. Every
configuration must pass through **backtest -> paper trading -> manual user unlock** before
it's allowed to go live.

**The platform's only job is to faithfully execute the user's own rules — it never offers
investment advice.** This compliance boundary is enforced in code: the Agent's prompt
explicitly forbids advice-like language, and `internal/agent/compliance.go` hard-blocks any
output that trips it.

---

## Quick start

```bash
# 1. Bring up dependencies (Postgres / Redis / Kafka)
docker compose up -d

# 2. Confirm all three services are reachable
go run ./cmd/healthcheck

# 3. Run the full test suite
go test ./...
cd python/backtest && python -m pytest -q
```

Host ports are deliberately offset from the defaults (Postgres `55432`, Redis `56379`,
Kafka `59200`) to avoid clashing with services that might already be running on your
machine. See `.env.example` for every configuration option.

---

## Architecture

```
Natural language
   │
   ▼
┌──────────────────┐   Strict JSON Schema + restatement confirmation
│  Agent layer      │   internal/agent
└────────┬─────────┘
         │ StrategyConfig (DRAFT)
         ▼
┌──────────────────┐   Mandatory pipeline, no skipping stages
│  Strategy state   │   internal/strategy/statemachine.go
│  machine          │
│  DRAFT → BACKTESTED → PAPER_TRADING → LIVE_ELIGIBLE → LIVE
└────────┬─────────┘
         │
         ▼
┌──────────────────┐   Concurrent module calls + aggregation + isolated degradation
│  Composition      │   internal/engine
│  engine           │
└────────┬─────────┘
         │ Decision → Postgres (audit) + Kafka
         ├──────────────────────┬───────────────────────┐
         ▼                      ▼                       ▼
┌────────────────┐   ┌──────────────────┐   ┌────────────────────┐
│  Signal module  │   │  Backtest engine  │   │  Multi-symbol       │
│  library        │   │  Go replay +       │   │  execution layer    │
│ internal/modules│   │  Python fills &    │   │ internal/execution  │
│                 │   │  performance stats │   │  one worker/symbol  │
└────────────────┘   └──────────────────┘   └────────────────────┘
```

### Layout

| Path | Responsibility |
|---|---|
| `pkg/types/` | Data structures shared across modules (Signal, StrategyConfig, Order…) |
| `internal/modules/` | Signal module library, one independent package per module |
| `internal/engine/` | Composition engine: concurrent evaluation, aggregation, audit, publish |
| `internal/agent/` | Natural language → StrategyConfig translation layer |
| `internal/strategy/` | Strategy config validation + state machine |
| `internal/execution/` | Multi-symbol execution layer, risk controls, order-placement channels |
| `internal/storage/` | Postgres data access |
| `internal/messaging/` | Kafka read/write |
| `internal/marketdata/` | Market data loading (CSV), synthetic data generation, `okx/` live feed |
| `internal/webui/` | Minimal usable interface: wizard, status board, strategy detail |
| `python/backtest/` | Backtest fill simulation, cost modeling, and performance statistics |
| `cmd/` | Entry points for each executable (including `cmd/webui`, `cmd/signal-engine`) |

---

## Key design decisions

### 1. Signal logic has exactly one implementation

Backtest signal computation **reuses the live `internal/engine`** (`cmd/backtest-runner`
replays candle-by-candle and emits decision JSONL); the Python side only does fill
simulation and performance statistics.

If support/resistance, CVD, and the rest of the modules were reimplemented a second time in
Python, the two implementations would inevitably drift — at that point the backtest would
be evaluating a *different* strategy, which is more dangerous than having no backtest at
all.

### 2. The module registry is the single source of truth

"Which modules the platform has, what parameters each one takes, and what their valid
ranges are" is defined exactly once, in `RequiredParams()`. The Agent's JSON Schema, the
module list baked into its prompt, and the engine's parameter validation are all generated
from it on the fly. Adding a new module automatically keeps every one of those in sync, and
the LLM has no room to invent a module that doesn't exist.

### 3. Monetary values always use decimal

Every price, quantity, and monetary field is `decimal.Decimal` (Python side:
`decimal.Decimal` as well) — CSVs are parsed with `NewFromString`, never `ParseFloat`.
Dimensionless statistics like confidence or Sharpe ratio are the only place `float` is
used.

### 4. Reject, don't repair

If the LLM's output doesn't conform to the schema, a parameter is out of range, or it
references a module that doesn't exist — the whole output is rejected outright and
regeneration is requested. There is never any "best-effort repair." Silently correcting a
bad output would make the user believe the system understood their rule, when in fact
something else entirely is about to run.

### 5. Isolation runs all the way through

- Module level: a single module timing out / erroring / panicking degrades to a neutral
  signal without affecting any other module
- Symbol level: each strategy gets its own worker, its own queue, and its own risk-control
  counters — nothing is shared
- Risk-control level: BTC hitting its daily loss cap doesn't stop ETH from continuing to
  trade

---

## Stage-by-stage verification

### Stage 1: Signal modules

```bash
go test ./internal/modules/...
go run ./cmd/module-demo      # prints each module's full Signal output against synthetic market data
```

Implemented: `support_resistance`, `volume_breakout`, `cvd_orderflow`, `macd_rsi`,
`news_sentiment`. CVD's order-flow data source is injected via the `FlowProvider`
interface — wiring in real Coinglass data later just means implementing that interface.
`macd_rsi` ships three modes: `macd_cross` (golden/death cross only), `rsi_reversal`
(overbought/oversold reversal only), and `confluence` (the default — filters out a
golden/death cross when RSI is already in the same-direction extreme zone, to avoid
chasing a move where momentum is already exhausted). Indicator math uses decimal
internally for precision; dimensionless outputs like RSI/confidence are still exposed as
float64. `news_sentiment` scores headlines related to the symbol within the lookback
window, weighted by recency; its sentiment-scoring data source is injected via the
`SentimentProvider` interface. The default implementation, `KeywordSentimentProvider`, is
just a keyword table (counts positive/negative word hits, can't understand negation,
sarcasm, or context) — signals from it are tagged `is_heuristic: true`, and downstream
consumers reject it from going live on that basis. Wiring in a real NLP/LLM sentiment
service later just means implementing that interface.

### Stage 2: Composition engine

```bash
go test ./internal/engine/...
# Real end-to-end path (requires docker compose up -d)
go test -tags=integration ./internal/engine/... -run Live -v
```

Supports the two aggregation modes `ALL` and `WEIGHTED`. Degraded signals count toward the
denominator under `WEIGHTED` — if half the modules go silent, the score is honestly diluted
rather than letting a handful of modules push the score over the threshold on their own.

### Stage 3: Agent translation layer

```bash
go test ./internal/agent/...
go run ./cmd/agent-service -schema        # view the JSON Schema and system prompt
ANTHROPIC_API_KEY=... go run ./cmd/agent-service   # interactive loop
```

Tests cover 10 "natural language input → expected config" contract pairs, 5 ambiguous
inputs (must ask a clarifying question rather than guess), 11 categories of invalid output
being rejected, and compliance-wording interception.

### Stage 4: Backtest engine

```bash
go run ./cmd/gen-testdata -dir testdata
go run ./cmd/backtest-runner -strategy testdata/strategy.json \
    -candles testdata/btcusdt_1h.csv -out testdata/decisions.jsonl -quiet
cd python/backtest && python -m tradeforge_backtest.cli \
    --strategy ../../testdata/strategy.json \
    --candles ../../testdata/btcusdt_1h.csv \
    --decisions ../../testdata/decisions.jsonl --no-save
```

Fill assumptions are deliberately conservative: a signal generated on candle *i*'s close
fills at candle *i+1*'s **open**, plus adverse-direction slippage and taker fees.
`TestReplayHasNoLookAheadBias` verifies there's no look-ahead bias by substituting future
data and comparing the resulting historical decisions.

In-sample and out-of-sample results are tracked separately, and trades are attributed to
**whichever segment they were opened in**. When the out-of-sample segment has no trades at
all, the result is explicitly labeled "unverified" rather than shown as "flat performance."

### Stage 5: State machine

```bash
go test ./internal/strategy/...
```

Every illegal transition (including `DRAFT → LIVE`) is guarded by a test. The backtest gate
only looks at out-of-sample metrics; `LIVE_ELIGIBLE → LIVE` only accepts `ActorUser` and
must record who performed it.

`PaperStats` (paper-trading run duration and trade count) doesn't maintain a separate
counter — `Store.PaperStats` derives it live from the existing `strategy_state_transitions`
+ `orders` tables: the starting point is the strategy's **most recent** transition into
`PAPER_TRADING` (re-entering paper trading after a risk-control pause restarts the clock),
and the trade count only counts `PAPER`-mode `FILLED` orders after that point.
`cmd/executor` has a built-in background loop (`-promotion-interval`, defaulting to 5
minutes) that periodically scans every `PAPER_TRADING` strategy and auto-advances it to
`LIVE_ELIGIBLE` once it clears the gate — the system is allowed to do this step on its own;
`LIVE_ELIGIBLE → LIVE` still always requires a manual user action.

```bash
go test ./cmd/executor/...                                      # unit tests for the promotion logic (in-memory fake store)
go test -tags=integration ./internal/storage/... -run PaperStats -v   # real DB round-trip (requires docker compose up -d)
```

### Stage 6: Execution layer

```bash
go test ./internal/execution/...
go run ./cmd/executor -state PAPER_TRADING
```

`TestIntegrationTwoSymbolsRunIndependently` is the integration test Stage 6 requires: BTC
and ETH run simultaneously with completely different module combinations and risk
parameters — when BTC trips its daily loss cap and gets paused, ETH's positions and trading
are completely unaffected.

### Stage 7: Minimal usable interface

```bash
go run ./cmd/webui                 # listens on TF_HTTP_ADDR (default :8080)
go test ./internal/webui/...
```

Go standard library `net/http` (1.22+ pattern ServeMux) + `html/template` + htmx (pulled
from a CDN, no build step / frontend framework) — no extra dependencies, same as every
other service in this project. Three capabilities:

- **Strategy configuration wizard** (`/wizard`): natural language → Agent restates its
  understanding for confirmation → saved as `DRAFT`. The flow mirrors `cmd/agent-service`'s
  CLI loop verbatim, just encoding the multi-turn conversation state (`agent.Proposal` +
  the `Turn` history) into hidden form fields carried between requests instead of
  maintaining a server-side session — this is a local, single-operator tool, and every
  `agent.Confirm` call before confirmation re-validates against the real module registry
  regardless, so tampering with the hidden field can at most get validation rejected; it
  can't bypass the schema or the compliance checks.
- **Strategy status board** (`/`): every strategy grouped by
  `DRAFT/BACKTESTED/PAPER_TRADING/LIVE_ELIGIBLE/LIVE/SUSPENDED`.
- **Strategy detail** (`/strategies/{id}`): backtest results (out-of-sample results are set
  apart in their own prominent border, and explicitly flagged "doesn't mean this has been
  validated" when there are zero trades), a cumulative-P&L SVG curve rebuilt from
  trade-by-trade data (`backtest_results` doesn't persist a per-candle equity curve, so
  it's the best that can be reconstructed from the existing `Trades`), paper-trading
  statistics, and order/decision/state-transition history.
- **Candle chart** (`/chart/{symbol}`): embeds TradingView's official free Advanced Chart
  widget (pointed at OKX as its data source). Moving averages/RSI/MACD and other common
  technical indicators shown there are the widget's own, not computed by the platform —
  it's purely a reference chart for eyeballing the market, unrelated to and not
  participating in any decision actually computed by `support_resistance`/
  `volume_breakout`/etc. Both the status board and strategy detail pages link to it, and
  the symbol's timeframe is automatically converted to TradingView's interval parameter.

The interface only exposes three write operations — every other transition still goes
through the existing CLI/automation paths: wizard confirmation (saves as `DRAFT`), the
manual `LIVE_ELIGIBLE → LIVE` unlock, and model settings. `LIVE_ELIGIBLE → LIVE` is the one
step the state machine's rules mandate an explicit human nod for; `handleUnlockLive` is
structurally a direct copy of `cmd/executor/promote.go`'s `checkPromotions` (re-fetch the
current state, validate with `CheckTransition`, and echo a failure back as a page message
instead of a 500).

`internal/storage/backtests.go` is a newly added read-only path: the `backtest_results`
table was previously only ever written by `python/backtest` — the Go side had never read
from it before.

#### Model settings (`/settings`) and multi-provider support

The Agent translation layer now supports multiple LLM providers instead of being locked to
Anthropic. The abstraction lives in `internal/agent`:

- The `agent.LLM` interface (`llm.go`) is the single integration point — `Agent`'s
  translate/validate/compliance logic is entirely unaware of which provider sits
  underneath.
- `agent.Provider` (`provider.go`) registers the currently supported providers: `anthropic`
  (`AnthropicLLM`, forced tool-use) and `openai` (`OpenAILLM`, `llm_openai.go`, forced
  function-calling with `strict: true`) — both enforce "structured output" rather than
  merely "please output JSON," and the red line of never letting free-form text enter the
  execution chain directly is the same constraint on both providers; it doesn't loosen just
  because the provider changed. Adding a new provider only requires implementing the `LLM`
  interface and registering one line in `Providers`.
- The `/settings` page lets you pick a provider and fill in an API key (plus an optional
  model override) directly, without touching environment variables or restarting the
  process. `cmd/webui` still auto-configures itself from `ANTHROPIC_API_KEY` at startup if
  it's set, same as before — `/settings` is just an additional path that doesn't require
  touching a shell. **The key is only kept in process memory, never persisted to the
  database** — persisting it would mean storing a plaintext secret in the database, and
  this project has no authentication/encrypted-storage mechanism for that, so the cost
  isn't worth the benefit; restarting the service requires re-entering it, and the UI copy
  says so honestly.

```bash
go test ./internal/agent/...     # provider dispatch, empty-key rejection, etc.
go test ./internal/webui/...     # /settings save/clear/failure doesn't clobber the existing config, etc.
```

### Market data: OKX live/historical candles

```bash
go run ./cmd/signal-engine -source okx -state PAPER_TRADING   # connect to live market data
go test ./internal/marketdata/okx/... ./cmd/signal-engine/...
go test -tags=integration ./internal/marketdata/okx/... -v    # hits real OKX, no key needed
```

`cmd/signal-engine` used to only be able to replay local CSVs — `-source okx` now lets it
genuinely "run live": on startup it backfills a historical window via REST, then subscribes
over WebSocket for a continuous stream of real-time candle closes. OKX was chosen instead
of the Binance mentioned by default in CLAUDE.md because Binance returns 451 (regional
block) from the egress IPs of common cloud dev environments — OKX doesn't have that
problem.

- `internal/marketdata/okx`: `Client.FetchCandles` pulls history, `Client.Subscribe`
  subscribes to the live feed — both paths share the same row-parsing code (REST and WS use
  the same array encoding). The `confirm` field on WebSocket pushes distinguishes "still
  forming" from "closed" — only closed candles are handed to the caller. Triggering a
  decision off a candle that's still changing would mean the live path re-introduces the
  exact look-ahead bias the backtest engine has always been careful to avoid.
- `cmd/signal-engine` subscribes grouped by `(symbol, timeframe)`, one goroutine per group,
  fully isolated across symbols; it reconnects with a fixed backoff after a disconnect.
  While at it, this also fixed a latent bug in the CSV replay path: the old `feeds` map only
  deduplicated by symbol, so if two strategies shared a symbol but used different
  timeframes, the second one would silently read the first one's market data instead of
  erroring out.
- **OKX's candle API doesn't provide a taker-buy/sell split** (no Binance-style
  taker-buy field), so when OKX market data feeds the `cvd_orderflow` module,
  `Candle.TakerBuyVolume` is always zero. The module's built-in `CandleFlowProvider` already
  guards against this ("volume present but zero taker-buy volume" raises an error outright,
  rather than treating the zero as real data) — a strategy using an OKX data source that
  also configures `cvd_orderflow` must explicitly switch to `SyntheticFlowProvider`. This is
  an intentional design decision, not a bug to fix.

---

## Current limitations

The following are known gaps that were deliberately left out of the MVP stage:

- **Live market data only connects to OKX.** The backtest/CSV-replay path is unaffected;
  switching to another exchange (say, actually wiring up Binance) means implementing a new
  adapter shaped like `internal/marketdata/okx` — `cmd/signal-engine` itself needs no
  changes (`historicalSource`/`liveSource` are minimal interfaces the consumer defines
  itself).
- **Order placement and market data don't come from the same exchange.**
  `internal/execution/binance.go` only connects to Binance's testnet for order placement,
  and `internal/marketdata/okx` only connects to OKX for market data — the two are
  currently not tied together. This isn't a big deal while paper trading never places real
  orders, but it's worth flagging: once live, "the price you're seeing" and "the place
  you can actually place an order" are currently two different exchanges, which needs a
  dedicated review before going live for real.
- **Only testnets are wired up.** `NewBinanceTestnetBroker` refuses any non-testnet
  address; connecting a real funded account should be a deliberate, reviewed code change,
  not a config flip.
- **CVD data comes from a candle's taker-buy volume**, which is lower fidelity than
  tick-by-tick order flow. `SyntheticFlowProvider` is a pure placeholder implementation —
  signals from it are tagged `is_synthetic: true`, and downstream consumers can reject it
  from going live on that basis.
- **News sentiment scoring is a keyword-based placeholder**, not real semantic
  understanding — it can't recognize negation, sarcasm, or context.
  `KeywordSentimentProvider` tags its signals `is_heuristic: true`; there's also no real
  news source wired into market data yet, so `MarketData.News` currently has to be
  populated by the caller.
- **`go test -race` hasn't been run successfully on this machine**: it needs a 64-bit gcc,
  and the mingw toolchain in the current environment is 32-bit. The execution layer's
  concurrent paths are protected by `sync.Mutex`; it's worth running this once on a machine
  with a complete toolchain.
- **The wizard's hidden form fields in the UI aren't signed or encrypted** (see the Stage 7
  section) — a deliberate simplification for a local, single-operator tool. This needs to be
  re-evaluated before any multi-user or internet-facing deployment.

---

## Environment variables

See `.env.example`. The most commonly used ones:

| Variable | Default | Description |
|---|---|---|
| `TF_PG_PORT` | `55432` | Postgres port |
| `TF_KAFKA_BROKERS` | `localhost:59200` | Kafka address |
| `ANTHROPIC_API_KEY` | empty | Required for `cmd/agent-service` (the CLI wizard); optional for `cmd/webui` — can be configured for any provider via the `/settings` page instead |
| `TF_AGENT_MODEL` | `claude-opus-5` | Model used by the translation layer |
| `TF_MODULE_TIMEOUT` | `3s` | Per-module timeout; degrades to a neutral signal on timeout |
| `TF_HTTP_ADDR` | `:8080` | `cmd/webui`'s listen address |
