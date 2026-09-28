"""TradeForge backtesting engine.

Division of responsibility (important):

* **Signal replay lives on the Go side** (``cmd/backtest-runner``), reusing
  ``internal/engine`` directly — the same code path used in live trading. Re-implementing
  support/resistance, CVD, etc. here in Python would let the two implementations drift
  apart over time, and at that point the backtest would be evaluating a different
  strategy than the one actually running live.
* **This package handles fill simulation, fee/slippage modeling, performance
  statistics, and in/out-of-sample splitting** — the part Python is actually good at.

Typical usage::

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
