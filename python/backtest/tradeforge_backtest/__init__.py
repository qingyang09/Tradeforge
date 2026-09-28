"""TradeForge 回测引擎。

职责分工（重要）：

* **信号重放在 Go 侧**（``cmd/backtest-runner``），直接复用实盘用的
  ``internal/engine``。如果在这里用 Python 再实现一遍支撑阻力、CVD 等模块，
  两份实现迟早漂移，那时回测评估的就是另一个策略了。
* **本包负责撮合模拟、手续费滑点建模、绩效统计与样本内外切分**——
  也就是 Python 真正擅长的部分。

典型用法::

    from tradeforge_backtest import load_candles, load_decisions_jsonl, run_backtest

    candles = load_candles("btc.csv", timeframe="1h")
    rows, meta = load_decisions_jsonl("decisions.jsonl")
    result = run_backtest(
        strategy_id=meta["strategy_id"],
        symbol=meta["symbol"],
        candles=candles,
        decisions=decisions_from_iter(rows),
        risk=risk,
    )
    print(result.summary())
"""

from .engine import (
    DEFAULT_INITIAL_CAPITAL,
    DEFAULT_SPLIT_RATIO,
    BacktestError,
    decisions_from_iter,
    run_backtest,
)
from .loaders import (
    fee_model_from_dict,
    load_candles,
    load_decisions_jsonl,
    load_strategy,
    risk_from_strategy,
)
from .metrics import compute_metrics, metrics_for_segment
from .model import (
    BacktestResult,
    Candle,
    Decision,
    Direction,
    FeeModel,
    Metrics,
    RiskConfig,
    Segment,
    Trade,
)

__version__ = "1.0.0"

__all__ = [
    "BacktestError",
    "BacktestResult",
    "Candle",
    "DEFAULT_INITIAL_CAPITAL",
    "DEFAULT_SPLIT_RATIO",
    "Decision",
    "Direction",
    "FeeModel",
    "Metrics",
    "RiskConfig",
    "Segment",
    "Trade",
    "compute_metrics",
    "decisions_from_iter",
    "fee_model_from_dict",
    "load_candles",
    "load_decisions_jsonl",
    "load_strategy",
    "metrics_for_segment",
    "risk_from_strategy",
    "run_backtest",
]
