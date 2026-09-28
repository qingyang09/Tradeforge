*English version: [README.md](README.md)*

---

# TradeForge

模块化加密货币交易策略平台（MVP）。用户用自然语言描述交易规则，AI Agent 把它翻译成
结构化配置；配置必须走完「回测 → 模拟盘 → 用户手动解锁」才能进入实盘。

**平台只负责忠实执行用户的规则，不提供任何投资建议。** 这条合规红线体现在代码里：
Agent 的 prompt 明确禁止建议措辞，`internal/agent/compliance.go` 会硬拦截命中的输出。

---

## 快速开始

```bash
# 1. 起依赖（Postgres / Redis / Kafka）
docker compose up -d

# 2. 确认三个服务都能连上
go run ./cmd/healthcheck

# 3. 跑全部测试
go test ./...
cd python/backtest && python -m pytest -q
```

宿主机端口刻意避开默认值（Postgres `55432`、Redis `56379`、Kafka `59200`），
以免和机器上已有的服务冲突。全部配置项见 `.env.example`。

---

## 架构

```
自然语言
   │
   ▼
┌──────────────────┐   严格 JSON Schema + 复述确认
│  Agent 翻译层     │   internal/agent
└────────┬─────────┘
         │ StrategyConfig（DRAFT）
         ▼
┌──────────────────┐   强制流程，逐级推进不可跳级
│  策略状态机       │   internal/strategy/statemachine.go
│  DRAFT → BACKTESTED → PAPER_TRADING → LIVE_ELIGIBLE → LIVE
└────────┬─────────┘
         │
         ▼
┌──────────────────┐   并发调用模块 + 聚合 + 降级隔离
│  组合引擎         │   internal/engine
└────────┬─────────┘
         │ Decision → Postgres（审计）+ Kafka
         ├──────────────────────┬───────────────────────┐
         ▼                      ▼                       ▼
┌────────────────┐   ┌──────────────────┐   ┌────────────────────┐
│  信号模块库     │   │   回测引擎        │   │   多标的执行层      │
│ internal/modules│   │ Go 重放 + Python  │   │ internal/execution │
└────────────────┘   │ 撮合与绩效统计     │   │ 每标的一个 worker   │
                     └──────────────────┘   └────────────────────┘
```

### 目录

| 路径 | 职责 |
|---|---|
| `pkg/types/` | 跨模块共享的数据结构（Signal、StrategyConfig、Order…） |
| `internal/modules/` | 信号模块库，每个模块一个独立包 |
| `internal/engine/` | 组合引擎：并发评估、聚合、审计、发布 |
| `internal/agent/` | 自然语言 → StrategyConfig 的翻译层 |
| `internal/strategy/` | 策略配置校验 + 状态机 |
| `internal/execution/` | 多标的执行层、风控、下单通道 |
| `internal/storage/` | Postgres 数据访问 |
| `internal/messaging/` | Kafka 读写 |
| `internal/marketdata/` | 行情加载（CSV）、合成数据生成、`okx/` 实时行情接入 |
| `internal/webui/` | 最小可用界面：向导、状态看板、策略详情 |
| `python/backtest/` | 回测的撮合模拟、成本建模与绩效统计 |
| `cmd/` | 各可执行程序入口（含 `cmd/webui`、`cmd/signal-engine`） |

---

## 关键设计决策

### 1. 信号逻辑只有一份实现

回测里的信号计算**复用实盘的 `internal/engine`**（`cmd/backtest-runner` 逐根重放，
输出决策 JSONL），Python 侧只做撮合模拟与绩效统计。

若在 Python 里把支撑阻力、CVD 等模块重写一遍，两份实现迟早漂移——那时回测评估的
就是另一个策略了，比没有回测更危险。

### 2. 模块注册表是唯一事实来源

「平台有哪些模块、每个模块有哪些参数、取值范围是什么」只在 `RequiredParams()` 里定义一次。
Agent 的 JSON Schema、提示词里的模块清单、引擎的参数校验，全部由它现场生成。
新增模块时这些地方自动跟上，LLM 也就没有发明模块的空间。

### 3. 金额一律用 decimal

所有价格、数量、金额字段都是 `decimal.Decimal`（Python 侧是 `decimal.Decimal`），
读 CSV 时用 `NewFromString` 而非 `ParseFloat`。置信度、夏普这类无量纲统计量才用 float。

### 4. 拒绝而不是修复

LLM 输出不符合 schema、参数越界、引用了不存在的模块——一律整体拒绝并要求重新生成，
绝不做「尽力修复」。静默修正会让用户以为系统理解了他的规则，实际执行的却是别的东西。

### 5. 隔离贯穿始终

- 模块级：单个模块超时/报错/panic → 降级为中性信号，不影响其它模块
- 标的级：每个策略一个 worker、一条队列、一套风控计数，互不共享状态
- 风控级：BTC 打满日亏额度不影响 ETH 继续交易

---

## 各阶段验收

### 阶段 1：信号模块

```bash
go test ./internal/modules/...
go run ./cmd/module-demo      # 打印各模块在构造行情上的完整 Signal 输出
```

已实现 `support_resistance`、`volume_breakout`、`cvd_orderflow`、`macd_rsi`、`news_sentiment`。
CVD 的订单流数据源通过 `FlowProvider` 接口注入，后续接 Coinglass 只需实现该接口。
`macd_rsi` 提供三种模式：`macd_cross`（只看金叉死叉）、`rsi_reversal`（只看超买超卖反转）、
`confluence`（默认，金叉死叉发生时若 RSI 已处于同向极值区间则过滤掉——避免在动能透支的
位置追单）。指标计算内部用 decimal 保证精度，RSI/置信度等无量纲值对外仍以 float64 表示。
`news_sentiment` 对回看窗口内与标的相关的新闻标题打分、按新近程度加权，情绪打分数据源
通过 `SentimentProvider` 接口注入；默认实现 `KeywordSentimentProvider` 只是一份关键词表
（命中正负面词计数，无法理解否定/讽刺/上下文），信号里标注 `is_heuristic: true`，
下游据此拒绝其进入实盘，后续接真实 NLP/LLM 情绪服务只需实现该接口。

### 阶段 2：组合引擎

```bash
go test ./internal/engine/...
# 真链路（需要 docker compose up -d）
go test -tags=integration ./internal/engine/... -run Live -v
```

支持 `ALL` 与 `WEIGHTED` 两种聚合。降级信号在 `WEIGHTED` 下计入分母——
一半模块沉默时分数会如实被稀释，而不是让少数模块独自顶到阈值。

### 阶段 3：Agent 翻译层

```bash
go test ./internal/agent/...
go run ./cmd/agent-service -schema        # 查看 JSON Schema 与 system prompt
ANTHROPIC_API_KEY=... go run ./cmd/agent-service   # 交互式闭环
```

测试覆盖 10 组「自然语言 → 期望配置」的契约对、5 组歧义输入（必须提问而非猜测）、
11 类非法输出的拒绝，以及合规措辞拦截。

### 阶段 4：回测引擎

```bash
go run ./cmd/gen-testdata -dir testdata
go run ./cmd/backtest-runner -strategy testdata/strategy.json \
    -candles testdata/btcusdt_1h.csv -out testdata/decisions.jsonl -quiet
cd python/backtest && python -m tradeforge_backtest.cli \
    --strategy ../../testdata/strategy.json \
    --candles ../../testdata/btcusdt_1h.csv \
    --decisions ../../testdata/decisions.jsonl --no-save
```

成交假设刻意保守：信号在第 i 根收盘产生，成交在第 i+1 根**开盘价**，
再加不利方向的滑点与吃单手续费。`TestReplayHasNoLookAheadBias` 用「替换未来数据、
比对历史决策」的方式验证无前视偏差。

样本内外分开统计，交易按**开仓所在的段**归属。样本外没有交易时，
结果会明确标注为「未经验证」而不是显示为「表现平平」。

### 阶段 5：状态机

```bash
go test ./internal/strategy/...
```

全部非法跳转（含 `DRAFT → LIVE`）都有测试守着。回测门槛只看样本外指标；
`LIVE_ELIGIBLE → LIVE` 只接受 `ActorUser` 且必须记录操作者身份。

`PaperStats`（模拟盘运行时长与成交笔数）不额外维护计数器，而是由
`Store.PaperStats` 从已有的 `strategy_state_transitions` + `orders` 表现算：
起点取该策略**最近一次**进入 `PAPER_TRADING` 的流转记录（被风控暂停后重新进入
模拟盘会重新计时），笔数只认这之后的 `PAPER` 模式 `FILLED` 订单。
`cmd/executor` 内置一个后台循环（`-promotion-interval`，默认 5 分钟）定期扫描
所有 `PAPER_TRADING` 策略并按门槛自动推进到 `LIVE_ELIGIBLE`——这一步系统可以自动做，
`LIVE_ELIGIBLE → LIVE` 仍然必须用户手动操作。

```bash
go test ./cmd/executor/...                                      # 推进逻辑的单元测试（内存假 store）
go test -tags=integration ./internal/storage/... -run PaperStats -v   # 真库往返（需要 docker compose up -d）
```

### 阶段 6：执行层

```bash
go test ./internal/execution/...
go run ./cmd/executor -state PAPER_TRADING
```

`TestIntegrationTwoSymbolsRunIndependently` 是阶段 6 要求的集成测试：
BTC 与 ETH 用完全不同的模块组合和风控参数同时运行，BTC 触发日亏上限被暂停时，
ETH 的持仓与交易完全不受影响。

### 阶段 7：最小可用界面

```bash
go run ./cmd/webui                 # 监听 TF_HTTP_ADDR（默认 :8080）
go test ./internal/webui/...
```

Go 标准库 `net/http`（1.22+ pattern ServeMux）+ `html/template` + htmx（CDN 引入，
不引构建步骤/前端框架），跟其余服务一样不额外拉依赖。三块能力：

- **策略配置向导**（`/wizard`）：自然语言 → Agent 复述确认 → 存为 `DRAFT`。
  流程逐字镜像 `cmd/agent-service` 的 CLI 闭环，只是把多轮对话状态（`agent.Proposal` +
  历史 `Turn`）编码进隐藏表单字段在请求间传递，不维护服务端 session——这是本地
  单操作者工具，且每次确认前 `agent.Confirm` 都会用真实模块注册表重新校验，
  篡改隐藏字段最多导致校验被拒绝，不会绕过 schema 或合规检查。
- **策略状态看板**（`/`）：全部策略按 `DRAFT/BACKTESTED/PAPER_TRADING/LIVE_ELIGIBLE/LIVE/SUSPENDED`
  分组展示。
- **策略详情**（`/strategies/{id}`）：回测结果（样本外单独用醒目边框区隔、
  零交易时明确提示"不代表已验证"）、按逐笔交易重建的累计盈亏 SVG 曲线
  （`backtest_results` 没有持久化逐根权益曲线，只能从已有的 `Trades` 重建）、
  模拟盘统计、订单/决策/状态流转历史。
- **K 线图**（`/chart/{symbol}`）：嵌入 TradingView 官方免费的 Advanced Chart 组件
  （数据源指到 OKX），均线/RSI/MACD 这些常见技术指标是组件自带的，不是平台自己算的——
  纯粹是给人看盘用的参考图，跟 `support_resistance`/`volume_breakout` 等模块实际算出的
  信号没有关系，也不参与任何决策。看板/策略详情页都能跳过去，标的周期会自动换算成
  TradingView 的 interval 参数。

界面只暴露三个写操作，其余流转继续走既有的 CLI/自动化路径：向导确认（存 `DRAFT`）、
`LIVE_ELIGIBLE → LIVE` 的手动解锁、以及模型设置。`LIVE_ELIGIBLE → LIVE` 是状态机规则里
唯一强制要求人显式点头的一步，`handleUnlockLive` 结构上直接照抄
`cmd/executor/promote.go` 的 `checkPromotions`（重新查当前状态、`CheckTransition`
校验、失败回显成页面提示而不是 500）。

`internal/storage/backtests.go` 是新增的只读路径：`backtest_results` 表此前只有
`python/backtest` 会写，Go 侧从未读过。

#### 模型设置（`/settings`）与多供应商支持

Agent 翻译层现在支持多个 LLM 供应商，不再绑死 Anthropic。抽象在 `internal/agent`：

- `agent.LLM` 接口（`llm.go`）是唯一的对接点，`Agent` 的翻译/校验/合规逻辑完全不
  关心底层是哪家供应商。
- `agent.Provider`（`provider.go`）登记当前支持的供应商，目前是 `anthropic`
  （`AnthropicLLM`，强制工具调用）和 `openai`（`OpenAILLM`，`llm_openai.go`，
  强制 function-call 加 `strict: true`）——两边都是"强制结构化输出"而不是
  "请你输出 JSON"，绝不允许自由文本直接进入执行链路这条红线在两个供应商上是
  同一份约束，不因为换供应商就放松。新增供应商只需要实现 `LLM` 接口并在
  `Providers` 里登记一行。
- Web 界面的 `/settings` 页面可以直接选供应商、填 API key（+ 可选的模型覆盖），
  不需要改环境变量重启进程；`cmd/webui` 启动时如果检测到 `ANTHROPIC_API_KEY`
  仍然会像以前一样自动配置好，`/settings` 只是多了一条不用碰 shell 的路径。
  **key 只保存在进程内存里，不落库**——落库等于在数据库里明文存一份密钥，
  这个项目没有认证/加密存储机制，代价和收益不成比例；重启服务需要重新填一次，
  界面文案会如实说明这一点。

```bash
go test ./internal/agent/...     # Provider 派发、空 key 拒绝等
go test ./internal/webui/...     # /settings 的保存/清除/失败不顶替现有配置等
```

### 行情接入：OKX 实时/历史 K 线

```bash
go run ./cmd/signal-engine -source okx -state PAPER_TRADING   # 接实时行情
go test ./internal/marketdata/okx/... ./cmd/signal-engine/...
go test -tags=integration ./internal/marketdata/okx/... -v    # 真打 OKX，不需要 key
```

`cmd/signal-engine` 原来只能重放本地 CSV，现在 `-source okx` 能真正"活"起来：启动时用
REST 回填一段历史窗口，随后订阅 WebSocket 持续喂实时收盘 K 线。选 OKX 而不是 CLAUDE.md
里默认提到的 Binance，是因为从常见的云端开发环境出口 IP 访问 Binance 会返回 451（地区
封锁），OKX 没有这个问题。

- `internal/marketdata/okx`：`Client.FetchCandles` 拉历史、`Client.Subscribe` 订阅实时，
  两条路径共用同一份行数据解析（REST 和 WS 是同一套数组编码）。WebSocket 推送里的
  `confirm` 字段区分"还在走"和"已收盘"，只有已收盘的 K 线会被推给调用方——用一根还在
  变化的 K 线触发决策，等于让实时链路重新踩一遍回测引擎一直小心避开的前视偏差。
- `cmd/signal-engine` 按 `(symbol, timeframe)` 分组订阅，一组一个 goroutine，标的间
  互相隔离；断线后固定退避重连。顺带修了 CSV 重放路径里一个潜藏的小问题：原来的
  `feeds` 只按 symbol 去重，两个策略共用同一个 symbol 但周期不同的话，第二个会悄悄
  读到第一个的行情而不报错。
- **OKX 的 K 线接口不提供主动买卖量拆分**（没有 Binance 那种 taker-buy 字段），所以
  OKX 行情喂给 `cvd_orderflow` 模块时，`Candle.TakerBuyVolume` 恒为零值。模块自带的
  `CandleFlowProvider` 对此已经有防护（"有成交量却没有主动买入量"会直接报错，而不是
  拿零值当真实数据算），用 OKX 数据源的策略如果配了 `cvd_orderflow`，必须显式换成
  `SyntheticFlowProvider`，这是有意的设计，不是需要修的 bug。

---

## 当前限制

以下是明确知道、但 MVP 阶段有意未做的部分：

- **实时行情只接了 OKX**。回测/CSV 重放路径不受影响；换一家交易所（比如真的要接
  Binance）需要照着 `internal/marketdata/okx` 的样子实现一份新的适配器，`cmd/signal-engine`
  这边不需要改（`historicalSource`/`liveSource` 是消费方自己定义的最小接口）。
- **下单和拉行情不是同一家**。`internal/execution/binance.go` 只接了币安测试网下单，
  `internal/marketdata/okx` 只接了 OKX 拉行情，两者目前没有绑在一起——这在"模拟盘不
  下真实单"的阶段问题不大，但要注意实盘阶段"看到的价格"和"能下单的地方"目前是两个
  不同的交易所，需要为此专门评审。
- **只对接测试网**。`NewBinanceTestnetBroker` 会拒绝非测试网地址；接真实资金账户
  应当是一次显式的代码改动加评审，不是改配置。
- **CVD 数据来自 K 线的主动买入量**，精度低于逐笔订单流。`SyntheticFlowProvider`
  是纯占位实现，信号里会标注 `is_synthetic: true`，下游可据此拒绝其进入实盘。
- **新闻情绪打分是关键词占位实现**，不是真正的语义理解，无法识别否定/讽刺/上下文。
  `KeywordSentimentProvider` 会在信号里标注 `is_heuristic: true`；行情数据里也还没有
  真实新闻源接入，`MarketData.News` 目前要靠调用方自己填充。
- **`go test -race` 未在本机跑通**：需要 64 位 gcc，当前环境的 mingw 是 32 位的。
  执行层的并发路径靠 `sync.Mutex` 保护，建议在有完整工具链的机器上补跑一次。
- **界面的向导隐藏字段不签名/不加密**（详见阶段 7 一节），这是本地单操作者工具下
  的刻意简化，多用户/联网部署前需要重新评估。

---

## 环境变量

见 `.env.example`。最常用的几个：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `TF_PG_PORT` | `55432` | Postgres 端口 |
| `TF_KAFKA_BROKERS` | `localhost:59200` | Kafka 地址 |
| `ANTHROPIC_API_KEY` | 空 | `cmd/agent-service`（CLI 向导）必需；`cmd/webui` 可选，不设也能通过 `/settings` 页面配置任意供应商 |
| `TF_AGENT_MODEL` | `claude-opus-5` | 翻译层使用的模型 |
| `TF_MODULE_TIMEOUT` | `3s` | 单模块超时，超时后降级为中性信号 |
| `TF_HTTP_ADDR` | `:8080` | `cmd/webui` 的监听地址 |
