"""result_to_row() 的序列化测试——不连数据库，只验证字段形状。"""

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
        # 序列化后应该带上 "Z" 时区后缀——Go 那边的 time.Time.UnmarshalJSON 要求
        # RFC3339 必须显式带时区，裸时间戳（naive datetime 的 .isoformat()）会
        # 解析失败，这是本次真实端到端验证时复现到的一个真实 bug。
        assert point["time"] == want_time.isoformat() + "Z"
        assert point["equity"] == str(want_equity)


def test_result_to_row_timestamps_are_timezone_aware_rfc3339():
    # 真实端到端验证时复现到的 bug：segments/trades/equity_curve 里嵌的时间戳如果
    # 没有时区后缀，Go 端 time.Time.UnmarshalJSON 会直接报错拒绝整条记录——
    # 这里对三处都做断言，防止同样的疏漏在其中任何一处重新出现。
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
    assert trades, "这个场景应该至少有一笔交易，测试才有意义"
    for t in trades:
        assert t["entry_time"].endswith("Z"), t
        assert t["exit_time"].endswith("Z"), t

    curve = json.loads(row["equity_curve"])
    assert curve, "权益曲线不应为空"
    for point in curve:
        assert point["time"].endswith("Z"), point


def test_result_to_row_equity_curve_empty_list_serializes_cleanly():
    # 只有一根K线时权益曲线至少有一个点（起始权益），但构造一个刻意退化的场景
    # 覆盖"曲线为空列表"也不该报错——json.dumps([]) 应该老老实实给出 "[]"。
    candles = make_candles(["100"])
    decisions = make_decisions(1)
    result = run_backtest(
        strategy_id="s2", symbol="BTCUSDT", candles=candles, decisions=decisions,
        risk=default_risk(), split_ratio=0.5,
    )
    result.equity_curve = []

    row = result_to_row(result)
    assert json.loads(row["equity_curve"]) == []
