"""回测撮合引擎的单元测试。

重点覆盖三件容易出错、出错后果又最严重的事：
1. 前视偏差（用未来数据成交）
2. 成本建模（手续费与滑点必须真的扣掉）
3. 样本内/外切分（样本外指标不能掺进样本内的结果）
"""

from __future__ import annotations

from datetime import datetime, timedelta
from decimal import Decimal

import pytest

from tradeforge_backtest import (
    BacktestError,
    Candle,
    Decision,
    Direction,
    FeeModel,
    RiskConfig,
    Segment,
    run_backtest,
)

START = datetime(2025, 1, 1)
HOUR = timedelta(hours=1)


def make_candles(closes: list[str], *, opens: list[str] | None = None) -> list[Candle]:
    """按收盘价序列构造 K 线。未指定开盘价时，用上一根的收盘价。"""
    out: list[Candle] = []
    for i, close in enumerate(closes):
        c = Decimal(close)
        o = Decimal(opens[i]) if opens else (Decimal(closes[i - 1]) if i else c)
        hi, lo = max(o, c), min(o, c)
        out.append(
            Candle(
                open_time=START + i * HOUR,
                close_time=START + (i + 1) * HOUR,
                open=o,
                high=hi,
                low=lo,
                close=c,
                volume=Decimal("1000"),
            )
        )
    return out


def make_decisions(
    n: int, triggers: dict[int, Direction] | None = None
) -> list[Decision]:
    """构造 n 条决策，只有 triggers 指定的下标会触发。"""
    triggers = triggers or {}
    out: list[Decision] = []
    for i in range(n):
        direction = triggers.get(i, Direction.NEUTRAL)
        out.append(
            Decision(
                index=i,
                bar_time=START + (i + 1) * HOUR,
                direction=direction,
                score=0.8 if direction is not Direction.NEUTRAL else 0.0,
                triggered=direction is not Direction.NEUTRAL,
                price=Decimal("100"),
                reason="test",
                signals=[{"module": "volume_breakout", "direction": direction.value}],
            )
        )
    return out


def default_risk(**kwargs) -> RiskConfig:
    params = {"max_position_size_quote": Decimal("1000")}
    params.update(kwargs)
    return RiskConfig(**params)


def run(candles, decisions, risk=None, **kwargs):
    return run_backtest(
        strategy_id="test",
        symbol="BTCUSDT",
        candles=candles,
        decisions=decisions,
        risk=risk or default_risk(),
        **kwargs,
    )


# ---------- 前视偏差 ----------


def test_entry_uses_next_bar_open_not_signal_bar_close():
    """信号在第 i 根收盘时产生，成交必须在第 i+1 根的开盘价上。

    如果用第 i 根的收盘价成交，就等于用了下单时还不知道的价格——
    这是回测里最常见也最致命的前视偏差。
    """
    # 第 3 根收盘 100，第 4 根开盘 150（跳空）。
    candles = make_candles(
        ["100", "100", "100", "100", "150", "150", "150", "150", "150", "150"],
        opens=["100", "100", "100", "100", "150", "150", "150", "150", "150", "150"],
    )
    decisions = make_decisions(10, {3: Direction.LONG})

    result = run(candles, decisions, default_risk(), fee_model=FeeModel(
        maker_fee_rate=Decimal(0), taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)
    ))

    assert len(result.trades) == 1
    # 用第 4 根的开盘价 150 成交，而不是第 3 根的收盘价 100。
    assert result.trades[0].entry_price == Decimal("150")


def test_no_entry_on_final_bar():
    """最后一根 K 线上触发的信号无法成交——没有"下一根"可用。"""
    candles = make_candles(["100"] * 5)
    decisions = make_decisions(5, {4: Direction.LONG})
    result = run(candles, decisions)
    assert result.trades == []


# ---------- 成本建模 ----------


def test_fees_and_slippage_are_actually_charged():
    """裸价格回测是被禁止的：成本必须真的从盈亏里扣掉。"""
    candles = make_candles(["100"] * 12)
    decisions = make_decisions(12, {2: Direction.LONG})

    fee = FeeModel(
        maker_fee_rate=Decimal("0.0002"),
        taker_fee_rate=Decimal("0.001"),
        slippage_bps=Decimal("10"),
    )
    result = run(candles, decisions, fee_model=fee)

    assert len(result.trades) == 1
    trade = result.trades[0]
    # 价格全程不动，因此毛利只可能来自滑点，且必然为负。
    assert trade.gross_pnl < 0, "滑点必须朝不利方向作用"
    assert trade.fees > 0, "手续费必须被计入"
    assert trade.pnl < trade.gross_pnl, "净盈亏必须比毛盈亏更差"
    # 一个完全不动的市场做一次往返必然亏钱——这正是成本建模的意义。
    assert result.overall.total_return < 0


def test_opposite_signal_reverses_position_in_place():
    """反向信号先平掉旧仓，再按新方向开仓——两笔独立的交易。"""
    candles = make_candles(["100"] * 12)
    decisions = make_decisions(12, {2: Direction.LONG, 7: Direction.SHORT})
    result = run(candles, decisions)

    assert len(result.trades) == 2
    assert result.trades[0].direction is Direction.LONG
    assert result.trades[0].exit_reason == "signal"
    assert result.trades[1].direction is Direction.SHORT
    # 反手开仓同样要等到下一根 K 线的开盘价，不得在当根收盘价上成交。
    assert result.trades[1].entry_time > result.trades[0].exit_time - HOUR


def test_slippage_direction_always_hurts():
    fee = FeeModel(slippage_bps=Decimal("100"))  # 1%
    mid = Decimal("100")
    assert fee.fill_price(mid, Direction.LONG) == Decimal("101"), "买入价格上浮"
    assert fee.fill_price(mid, Direction.SHORT) == Decimal("99"), "卖出价格下压"


def test_zero_cost_model_is_opt_in_only():
    """默认费率必须非零：忘了配置时应当保守，而不是变成裸价格回测。"""
    default = FeeModel()
    assert default.taker_fee_rate > 0
    assert default.slippage_bps > 0


# ---------- 风控 ----------


def test_stop_loss_exits_at_trigger_price():
    # 建仓价 100，之后跌到 90；止损 5% 应在 95 附近离场，而不是等到 90。
    candles = make_candles(["100", "100", "100", "100", "90", "90", "90", "90"])
    decisions = make_decisions(8, {1: Direction.LONG})
    result = run(
        candles, decisions,
        default_risk(stop_loss_pct=0.05),
        fee_model=FeeModel(taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)),
    )

    assert len(result.trades) == 1
    trade = result.trades[0]
    assert trade.exit_reason == "stop_loss"
    assert trade.exit_price == Decimal("95")


def test_take_profit_exits_at_trigger_price():
    candles = make_candles(["100", "100", "100", "100", "120", "120", "120", "120"])
    decisions = make_decisions(8, {1: Direction.LONG})
    result = run(
        candles, decisions,
        default_risk(take_profit_pct=0.10),
        fee_model=FeeModel(taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)),
    )
    assert result.trades[0].exit_reason == "take_profit"
    assert result.trades[0].exit_price == Decimal("110")


def test_stop_loss_wins_when_both_triggered_in_same_bar():
    """一根 K 线同时触及止损与止盈时，必须假设成不利的那个。

    我们无从知道哪个先到；假设成止盈会系统性高估策略表现。
    """
    candles = make_candles(["100"] * 8)
    # 第 4 根是长十字星，上下都被扫到。
    wide = Candle(
        open_time=START + 4 * HOUR,
        close_time=START + 5 * HOUR,
        open=Decimal("100"),
        high=Decimal("120"),
        low=Decimal("80"),
        close=Decimal("100"),
        volume=Decimal("1000"),
    )
    candles[4] = wide
    decisions = make_decisions(8, {1: Direction.LONG})

    result = run(
        candles, decisions,
        default_risk(stop_loss_pct=0.05, take_profit_pct=0.05),
        fee_model=FeeModel(taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)),
    )
    assert result.trades[0].exit_reason == "stop_loss"


# ---------- 支撑/阻力位止损止盈 ----------
#
# 跟 Go 侧 internal/execution/risk_test.go 是同一份场景，验证的是同一套语义：
# 止损/止盈的价格来自开仓那一刻 support_resistance 模块检测到的关键位，不是固定百分比。
# 这两边算法必须一致——回测跟实盘用的不是同一套判断逻辑，回测就没有意义。


def _support_resistance_signal(support: str | None, resistance: str | None, degraded: bool = False) -> dict:
    return {
        "module": "support_resistance",
        "degraded": degraded,
        "raw": {
            "nearest_support": {"price": support, "touches": 3} if support else None,
            "nearest_resistance": {"price": resistance, "touches": 3} if resistance else None,
        },
    }


def make_decisions_with_signal(
    n: int, triggers: dict[int, Direction], signal: dict
) -> list[Decision]:
    """跟 make_decisions 一样，但触发那条决策带上指定的 support_resistance 信号。"""
    out: list[Decision] = []
    for i in range(n):
        direction = triggers.get(i, Direction.NEUTRAL)
        signals = [signal] if i in triggers else []
        out.append(
            Decision(
                index=i,
                bar_time=START + (i + 1) * HOUR,
                direction=direction,
                score=0.8 if direction is not Direction.NEUTRAL else 0.0,
                triggered=direction is not Direction.NEUTRAL,
                price=Decimal("100"),
                reason="test",
                signals=signals,
            )
        )
    return out


def test_support_resistance_stop_loss_exits_at_resolved_level():
    # 支撑位在 95（不是固定百分比）；跌到 90 时应该在 95 附近离场。
    candles = make_candles(["100", "100", "100", "100", "90", "90", "90", "90"])
    decisions = make_decisions_with_signal(
        8, {1: Direction.LONG}, _support_resistance_signal("95", "110")
    )
    result = run(
        candles, decisions,
        default_risk(stop_loss_mode="support_resistance"),
        fee_model=FeeModel(taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)),
    )
    assert len(result.trades) == 1
    trade = result.trades[0]
    assert trade.exit_reason == "stop_loss"
    assert trade.exit_price == Decimal("95")


def test_support_resistance_take_profit_exits_at_resolved_level():
    candles = make_candles(["100", "100", "100", "100", "120", "120", "120", "120"])
    decisions = make_decisions_with_signal(
        8, {1: Direction.LONG}, _support_resistance_signal("95", "110")
    )
    result = run(
        candles, decisions,
        default_risk(take_profit_mode="support_resistance"),
        fee_model=FeeModel(taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)),
    )
    assert result.trades[0].exit_reason == "take_profit"
    assert result.trades[0].exit_price == Decimal("110")


def test_short_position_stop_loss_uses_resistance_take_profit_uses_support():
    candles = make_candles(["100", "100", "100", "100", "112", "112", "112", "112"])
    decisions = make_decisions_with_signal(
        8, {1: Direction.SHORT}, _support_resistance_signal("95", "110")
    )
    result = run(
        candles, decisions,
        default_risk(stop_loss_mode="support_resistance", take_profit_mode="support_resistance"),
        fee_model=FeeModel(taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)),
    )
    assert len(result.trades) == 1
    # 空头止损设在阻力位（110），价格涨到 112 应该已经触发。
    assert result.trades[0].exit_reason == "stop_loss"
    assert result.trades[0].exit_price == Decimal("110")


def test_support_resistance_mode_skips_entry_when_level_unavailable():
    # 附近没有探测到支撑位（比如价格处于历史低点）：不该在没有止损保护的情况下开仓。
    candles = make_candles(["100"] * 8)
    decisions = make_decisions_with_signal(
        8, {1: Direction.LONG}, _support_resistance_signal(None, "110")
    )
    result = run(candles, decisions, default_risk(stop_loss_mode="support_resistance"))
    assert len(result.trades) == 0


def test_support_resistance_mode_opens_without_take_profit_when_resistance_unavailable():
    # 止盈不是安全机制，跟止损不对称：有支撑、没有阻力（突破型入场最常见的情形）时
    # 不该拒绝这笔开仓，只是没有止盈线——止损照常生效。
    candles = make_candles(["100", "100", "100", "100", "90", "90", "90", "90"])
    decisions = make_decisions_with_signal(
        8, {1: Direction.LONG}, _support_resistance_signal("95", None)
    )
    result = run(
        candles, decisions,
        default_risk(stop_loss_mode="support_resistance", take_profit_mode="support_resistance"),
        fee_model=FeeModel(taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)),
    )
    assert len(result.trades) == 1
    assert result.trades[0].exit_reason == "stop_loss"
    assert result.trades[0].exit_price == Decimal("95")


def test_support_resistance_mode_skips_entry_when_signal_degraded():
    candles = make_candles(["100"] * 8)
    decisions = make_decisions_with_signal(
        8, {1: Direction.LONG}, _support_resistance_signal("95", "110", degraded=True)
    )
    result = run(candles, decisions, default_risk(stop_loss_mode="support_resistance"))
    assert len(result.trades) == 0


# ---------- POC（成交量分布重心）止损止盈 ----------
#
# 跟 Go 侧 internal/execution/risk_test.go 是同一份场景：POC 只有一个价格，不区分多空，
# 止损止盈用的是同一个值。


def test_risk_pct_sizing_uses_stop_distance_and_equity():
    # 权益 10000、单笔风险 1%（=100）、止损距离 5% → 仓位 = 100 / 0.05 = 2000，
    # 数量 = 2000 / 建仓价 100 = 20。
    candles = make_candles(["100", "100", "100", "100", "90", "90", "90", "90"])
    decisions = make_decisions(8, {1: Direction.LONG})
    result = run(
        candles, decisions,
        default_risk(
            max_position_size_quote=Decimal("1000000"),  # 上限调高，不让默认的 1000 挡住
            stop_loss_pct=0.05,
            position_sizing_mode="risk_pct",
            account_equity_quote=Decimal("10000"),
            risk_per_trade_pct=0.01,
        ),
        fee_model=FeeModel(taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)),
    )
    assert len(result.trades) == 1
    trade = result.trades[0]
    assert trade.quantity == Decimal("20")
    assert trade.exit_reason == "stop_loss"
    assert trade.exit_price == Decimal("95")


def test_risk_pct_sizing_skipped_when_max_position_cap_exceeded():
    # 同样会算出仓位 2000，但硬上限只给 500——应该整笔拒绝，不是缩小到 500。
    candles = make_candles(["100"] * 8)
    decisions = make_decisions(8, {1: Direction.LONG})
    result = run(
        candles, decisions,
        default_risk(
            max_position_size_quote=Decimal("500"),
            stop_loss_pct=0.05,
            position_sizing_mode="risk_pct",
            account_equity_quote=Decimal("10000"),
            risk_per_trade_pct=0.01,
        ),
    )
    assert len(result.trades) == 0


def test_risk_pct_sizing_skips_entry_when_stop_loss_unresolvable():
    # support_resistance 止损模式，但附近没有探测到支撑位——算不出止损距离，
    # risk_pct 模式应该跟 fixed_quote 模式一样拒绝开仓。
    candles = make_candles(["100"] * 8)
    decisions = make_decisions_with_signal(
        8, {1: Direction.LONG}, _support_resistance_signal(None, "110")
    )
    result = run(
        candles, decisions,
        default_risk(
            stop_loss_mode="support_resistance",
            position_sizing_mode="risk_pct",
            account_equity_quote=Decimal("10000"),
            risk_per_trade_pct=0.01,
        ),
    )
    assert len(result.trades) == 0


def test_risk_pct_sizing_zero_stop_distance_skips_entry():
    # 止损价等于入场价（距离为 0）：公式除零，应该拒绝开仓而不是抛异常。
    candles = make_candles(["100"] * 8)
    decisions = make_decisions_with_signal(
        8, {1: Direction.LONG}, _support_resistance_signal("100", "110")
    )
    result = run(
        candles, decisions,
        default_risk(
            stop_loss_mode="support_resistance",
            position_sizing_mode="risk_pct",
            account_equity_quote=Decimal("10000"),
            risk_per_trade_pct=0.01,
        ),
    )
    assert len(result.trades) == 0


def test_risk_pct_sizing_still_capped_by_available_equity():
    # risk_pct 模式算出的仓位（2000）仍然要受"当前权益"这道既有的回测特有上限约束：
    # 起始资金只给 1000，仓位应该被压到 1000（等价于 min(risk_notional, equity)）,
    # 而不是无视权益直接用算出来的 2000。
    candles = make_candles(["100"] * 8)
    decisions = make_decisions(8, {1: Direction.LONG})
    result = run(
        candles, decisions,
        default_risk(
            max_position_size_quote=Decimal("1000000"),
            stop_loss_pct=0.05,
            position_sizing_mode="risk_pct",
            account_equity_quote=Decimal("10000"),
            risk_per_trade_pct=0.01,
        ),
        fee_model=FeeModel(taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)),
        initial_capital=Decimal("1000"),
    )
    assert len(result.trades) == 1
    assert result.trades[0].quantity == Decimal("10")  # 1000 权益 / 建仓价 100


def _poc_signal(poc_price: str | None, degraded: bool = False) -> dict:
    return {
        "module": "poc",
        "degraded": degraded,
        "raw": {"poc_price": poc_price, "is_approximate": True},
    }


def test_poc_stop_loss_exits_at_resolved_level():
    candles = make_candles(["100", "100", "100", "100", "90", "90", "90", "90"])
    decisions = make_decisions_with_signal(8, {1: Direction.LONG}, _poc_signal("95"))
    result = run(
        candles, decisions,
        default_risk(stop_loss_mode="poc"),
        fee_model=FeeModel(taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)),
    )
    assert len(result.trades) == 1
    trade = result.trades[0]
    assert trade.exit_reason == "stop_loss"
    assert trade.exit_price == Decimal("95")


def test_poc_mode_uses_same_price_for_long_and_short():
    # 多头止损用 POC；空头止损也用同一个 POC——不像支撑/阻力位分上下两个。
    candles = make_candles(["100", "100", "100", "100", "112", "112", "112", "112"])
    decisions = make_decisions_with_signal(8, {1: Direction.SHORT}, _poc_signal("110"))
    result = run(
        candles, decisions,
        default_risk(stop_loss_mode="poc"),
        fee_model=FeeModel(taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)),
    )
    assert len(result.trades) == 1
    assert result.trades[0].exit_reason == "stop_loss"
    assert result.trades[0].exit_price == Decimal("110")


def test_poc_mode_skips_entry_when_price_unavailable():
    candles = make_candles(["100"] * 8)
    decisions = make_decisions_with_signal(8, {1: Direction.LONG}, _poc_signal(None))
    result = run(candles, decisions, default_risk(stop_loss_mode="poc"))
    assert len(result.trades) == 0


def test_poc_mode_skips_entry_when_signal_degraded():
    candles = make_candles(["100"] * 8)
    decisions = make_decisions_with_signal(8, {1: Direction.LONG}, _poc_signal("95", degraded=True))
    result = run(candles, decisions, default_risk(stop_loss_mode="poc"))
    assert len(result.trades) == 0


def test_max_holding_period_forces_exit():
    candles = make_candles(["100"] * 12)
    decisions = make_decisions(12, {1: Direction.LONG})
    result = run(candles, decisions, default_risk(max_holding_period_secs=3 * 3600))

    assert len(result.trades) == 1
    trade = result.trades[0]
    assert trade.exit_reason == "max_holding"
    assert (trade.exit_time - trade.entry_time).total_seconds() >= 3 * 3600


def test_opposite_signal_closes_position():
    candles = make_candles(["100"] * 12)
    decisions = make_decisions(12, {1: Direction.LONG, 6: Direction.SHORT})
    result = run(candles, decisions)
    assert result.trades[0].exit_reason == "signal"


def test_daily_loss_limit_halts_new_entries_that_day():
    """单日亏损达上限后，当天不得再开新仓。"""
    # 每次开仓后立刻大跌，快速累积亏损。
    closes = ["100", "100", "80", "80", "100", "100", "80", "80", "100", "100"]
    candles = make_candles(closes, opens=closes)
    decisions = make_decisions(
        10, {1: Direction.LONG, 4: Direction.LONG, 7: Direction.LONG}
    )
    result = run(
        candles, decisions,
        default_risk(max_daily_loss_quote=Decimal("50"), stop_loss_pct=0.1),
    )
    # 所有 K 线都在同一天，触发日亏上限后应当停止开新仓。
    assert len(result.trades) < 3, "达到单日亏损上限后不应继续开仓"


def test_position_size_capped_by_risk_config():
    candles = make_candles(["100"] * 8)
    decisions = make_decisions(8, {1: Direction.LONG})
    result = run(
        candles, decisions,
        default_risk(max_position_size_quote=Decimal("200")),
        fee_model=FeeModel(taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)),
    )
    trade = result.trades[0]
    notional = trade.entry_price * trade.quantity
    assert notional <= Decimal("200")


# ---------- 样本内 / 样本外 ----------


def test_in_and_out_of_sample_are_separated():
    candles = make_candles(["100"] * 20)
    decisions = make_decisions(20, {2: Direction.LONG, 15: Direction.LONG})
    # 加上持仓时限，让第一笔在第二个信号之前就平掉，两段各得一笔交易。
    result = run(
        candles, decisions,
        default_risk(max_holding_period_secs=2 * 3600),
        split_ratio=0.5,
    )

    segments = {t.segment for t in result.trades}
    assert Segment.IN_SAMPLE in segments
    assert Segment.OUT_OF_SAMPLE in segments

    in_trades = [t for t in result.trades if t.segment is Segment.IN_SAMPLE]
    out_trades = [t for t in result.trades if t.segment is Segment.OUT_OF_SAMPLE]
    assert result.in_sample.trade_count == len(in_trades)
    assert result.out_of_sample.trade_count == len(out_trades)
    # 每笔交易只能属于一段。
    assert len(in_trades) + len(out_trades) == len(result.trades)


def test_flags_when_out_of_sample_has_no_trades():
    """样本外没有交易时，必须能被识别为"未经验证"而不是"表现平平"。"""
    candles = make_candles(["100"] * 20)
    decisions = make_decisions(20, {2: Direction.LONG})  # 只在样本内触发
    result = run(candles, decisions, split_ratio=0.5)

    assert result.out_of_sample.trade_count == 0
    assert not result.parameters_validated_out_of_sample
    assert "没有产生任何交易" in result.summary()


def test_summary_puts_out_of_sample_first():
    """摘要必须把样本外放在最显眼的位置，防止用户误读样本内指标。"""
    candles = make_candles(["100"] * 20)
    decisions = make_decisions(20, {2: Direction.LONG, 15: Direction.LONG})
    text = run(
        candles, decisions,
        default_risk(max_holding_period_secs=2 * 3600),
        split_ratio=0.5,
    ).summary()
    assert text.index("【样本外】") < text.index("【样本内】")


def test_trade_belongs_to_segment_where_decision_was_made():
    """跨越分割点的交易归属于开仓时所在的那一段。

    若按平仓时间归属，一笔用样本内信息做出的决策会被算进样本外，
    样本外验证也就名存实亡了。
    """
    candles = make_candles(["100"] * 20)
    # 在样本内开仓，一路持有到数据结束（落在样本外）。
    decisions = make_decisions(20, {2: Direction.LONG})
    result = run(candles, decisions, split_ratio=0.5)

    assert len(result.trades) == 1
    trade = result.trades[0]
    assert trade.exit_time > result.split_at, "该笔交易确实在样本外平仓"
    assert trade.segment is Segment.IN_SAMPLE, "但它应归属于开仓所在的样本内"
    assert result.out_of_sample.trade_count == 0


def test_split_ratio_must_leave_both_segments_nonempty():
    candles = make_candles(["100"] * 10)
    decisions = make_decisions(10)
    with pytest.raises(BacktestError):
        run(candles, decisions, split_ratio=1.5)


# ---------- 输入校验 ----------


def test_rejects_mismatched_lengths():
    with pytest.raises(BacktestError, match="不一致"):
        run(make_candles(["100"] * 10), make_decisions(5))


def test_rejects_empty_inputs():
    with pytest.raises(BacktestError):
        run([], make_decisions(5))
    with pytest.raises(BacktestError):
        run(make_candles(["100"] * 5), [])


def test_rejects_nonpositive_capital():
    with pytest.raises(BacktestError):
        run(make_candles(["100"] * 5), make_decisions(5), initial_capital=Decimal(0))


# ---------- 可解释性 ----------


def test_trades_keep_trigger_signals():
    """每笔交易都要能回答"是哪个模块的什么信号触发的"。"""
    candles = make_candles(["100"] * 8)
    decisions = make_decisions(8, {1: Direction.LONG})
    result = run(candles, decisions)

    assert result.trades[0].trigger_signals
    assert result.trades[0].trigger_signals[0]["module"] == "volume_breakout"


def test_equity_curve_tracks_every_bar():
    candles = make_candles(["100"] * 10)
    result = run(candles, make_decisions(10))
    # 初始点 + 每根 K 线一个点。
    assert len(result.equity_curve) == len(candles) + 1
