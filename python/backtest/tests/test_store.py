"""Serialization tests for result_to_row() -- no database connection, just checks field shapes."""

from __future__ import annotations

import json
from datetime import datetime, timedelta

from decimal import Decimal

from tradeforge_backtest import Candle, Decision, Direction, RiskConfig, run_backtest
from tradeforge_backtest.store import result_to_row

START = datetime(2025, 1, 1)
HOUR = timedelta(hours=1)


def make_candles(closes: list[str]) -> list[Candle]:
    out: list[Candle] = []
    for i, close in enumerate(closes):
        c = Decimal(close)
        o = Decimal(closes[i - 1]) if i else c
        hi, lo = max(o, c), min(o, c)
        out.append(Candle(
            open_time=START + i * HOUR, close_time=START + (i + 1) * HOUR,
            open=o, high=hi, low=lo, close=c, volume=Decimal("1000"),
        ))
    return out


def make_decisions(n: int, triggers: dict[int, Direction] | None = None) -> list[Decision]:
    triggers = triggers or {}
    out: list[Decision] = []
    for i in range(n):
        direction = triggers.get(i, Direction.NEUTRAL)
        out.append(Decision(
            index=i, bar_time=START + (i + 1) * HOUR, direction=direction,
            score=0.8 if direction is not Direction.NEUTRAL else 0.0,
            triggered=direction is not Direction.NEUTRAL, price=Decimal("100"),
            reason="test", signals=[{"module": "volume_breakout", "direction": direction.value}],
        ))
    return out


def default_risk(**kwargs) -> RiskConfig:
    params = {"max_position_size_quote": Decimal("1000")}
    params.update(kwargs)
    return RiskConfig(**params)


def test_result_to_row_includes_equity_curve_matching_source():
    candles = make_candles(["100", "101", "102", "103", "104", "103", "102"])
    decisions = make_decisions(len(candles), {0: Direction.LONG})
    result = run_backtest(
        strategy_id="s1", symbol="BTCUSDT", candles=candles, decisions=decisions,
        risk=default_risk(), split_ratio=0.7,
    )

    row = result_to_row(result)
    curve = json.loads(row["equity_curve"])

    assert len(curve) == len(result.equity_curve)
    for point, (want_time, want_equity) in zip(curve, result.equity_curve):
        # Serialized output must carry a "Z" timezone suffix -- Go's
        # time.Time.UnmarshalJSON requires RFC3339 to be explicitly
        # timezone-aware, and a bare timestamp (a naive datetime's
        # .isoformat()) fails to parse. This reproduces a real bug found
        # during real end-to-end verification.
        assert point["time"] == want_time.isoformat() + "Z"
        assert point["equity"] == str(want_equity)


def test_result_to_row_timestamps_are_timezone_aware_rfc3339():
    # A bug reproduced during real end-to-end verification: if timestamps embedded in
    # segments/trades/equity_curve lack a timezone suffix, Go's time.Time.UnmarshalJSON
    # rejects the whole record outright -- asserting on all three here guards against
    # the same oversight recurring in any one of them.
    candles = make_candles(["100", "101", "102", "103", "104", "103", "102"])
    decisions = make_decisions(len(candles), {0: Direction.LONG, 4: Direction.SHORT})
    result = run_backtest(
        strategy_id="s3", symbol="BTCUSDT", candles=candles, decisions=decisions,
        risk=default_risk(), split_ratio=0.5,
    )
    row = result_to_row(result)

    segments = json.loads(row["segments"])
    for seg in segments:
        assert seg["start"].endswith("Z"), seg
        assert seg["end"].endswith("Z"), seg

    trades = json.loads(row["trades"])
    assert trades, "this scenario should produce at least one trade, or the test proves nothing"
    for t in trades:
        assert t["entry_time"].endswith("Z"), t
        assert t["exit_time"].endswith("Z"), t

    curve = json.loads(row["equity_curve"])
    assert curve, "equity curve should not be empty"
    for point in curve:
        assert point["time"].endswith("Z"), point


def test_result_to_row_equity_curve_empty_list_serializes_cleanly():
    # With a single candle the equity curve still has at least one point (the starting
    # equity), but we deliberately construct a degenerate scenario to cover "curve is an
    # empty list" too -- it must not raise; json.dumps([]) should simply give back "[]".
    candles = make_candles(["100"])
    decisions = make_decisions(1)
    result = run_backtest(
        strategy_id="s2", symbol="BTCUSDT", candles=candles, decisions=decisions,
        risk=default_risk(), split_ratio=0.5,
    )
    result.equity_curve = []

    row = result_to_row(result)
    assert json.loads(row["equity_curve"]) == []
