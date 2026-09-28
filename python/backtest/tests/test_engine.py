"""Unit tests for the backtest matching engine.

Focused on the three things most prone to bugs, and most damaging when
they occur:
1. Look-ahead bias (filling on data that wasn't actually available yet)
2. Cost modeling (fees and slippage must actually be deducted)
3. In-sample/out-of-sample split (out-of-sample metrics must not leak
   in-sample results)
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
    """Build candles from a series of close prices. Without explicit opens, each candle opens at the previous close."""
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
    """Build n decisions; only the indexes named in triggers actually fire."""
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


# ---------- Look-ahead bias ----------


def test_entry_uses_next_bar_open_not_signal_bar_close():
    """A signal generated at candle i's close must fill at candle i+1's open.

    Filling at candle i's own close would mean trading at a price that
    wasn't actually known yet at order time -- the most common and most
    damaging form of look-ahead bias in a backtest.
    """
    # Candle 3 closes at 100, candle 4 opens at 150 (a gap).
    candles = make_candles(
        ["100", "100", "100", "100", "150", "150", "150", "150", "150", "150"],
        opens=["100", "100", "100", "100", "150", "150", "150", "150", "150", "150"],
    )
    decisions = make_decisions(10, {3: Direction.LONG})

    result = run(candles, decisions, default_risk(), fee_model=FeeModel(
        maker_fee_rate=Decimal(0), taker_fee_rate=Decimal(0), slippage_bps=Decimal(0)
    ))

    assert len(result.trades) == 1
    # Fills at candle 4's open of 150, not candle 3's close of 100.
    assert result.trades[0].entry_price == Decimal("150")


def test_no_entry_on_final_bar():
    """A signal triggered on the final candle can't be filled -- there's no "next candle" to fill on."""
    candles = make_candles(["100"] * 5)
    decisions = make_decisions(5, {4: Direction.LONG})
    result = run(candles, decisions)
    assert result.trades == []


# ---------- Cost modeling ----------


def test_fees_and_slippage_are_actually_charged():
    """Bare-price backtesting is forbidden: cost must actually be deducted from P&L."""
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
    # Price never moves, so gross P&L can only come from slippage, and must be negative.
    assert trade.gross_pnl < 0, "slippage must work against the trader"
    assert trade.fees > 0, "fees must be charged"
    assert trade.pnl < trade.gross_pnl, "net P&L must be worse than gross P&L"
    # A round trip in a perfectly flat market must lose money -- that's the whole point of cost modeling.
    assert result.overall.total_return < 0


def test_opposite_signal_reverses_position_in_place():
    """An opposite signal first closes the existing position, then opens a new one in the new direction -- two separate trades."""
    candles = make_candles(["100"] * 12)
    decisions = make_decisions(12, {2: Direction.LONG, 7: Direction.SHORT})
    result = run(candles, decisions)

    assert len(result.trades) == 2
    assert result.trades[0].direction is Direction.LONG
    assert result.trades[0].exit_reason == "signal"
    assert result.trades[1].direction is Direction.SHORT
    # Reversing likewise has to wait for the next candle's open -- it must not fill at the current candle's close.
    assert result.trades[1].entry_time > result.trades[0].exit_time - HOUR


def test_slippage_direction_always_hurts():
    fee = FeeModel(slippage_bps=Decimal("100"))  # 1%
    mid = Decimal("100")
    assert fee.fill_price(mid, Direction.LONG) == Decimal("101"), "buy price should move up"
    assert fee.fill_price(mid, Direction.SHORT) == Decimal("99"), "sell price should move down"


def test_zero_cost_model_is_opt_in_only():
    """Default fee rates must be nonzero: forgetting to configure them should be conservative, not silently degrade into a bare-price backtest."""
    default = FeeModel()
    assert default.taker_fee_rate > 0
    assert default.slippage_bps > 0


# ---------- Risk controls ----------


def test_stop_loss_exits_at_trigger_price():
    # Entry at 100, then drops to 90; a 5% stop-loss should exit around 95, not wait for 90.
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
    """When a single candle touches both stop-loss and take-profit, must assume the unfavorable one.

    We have no way to know which one hit first; assuming take-profit would
    systematically overstate the strategy's performance.
    """
    candles = make_candles(["100"] * 8)
    # Candle 4 is a wide doji -- swept on both sides.
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


# ---------- Support/resistance-based stop-loss/take-profit ----------
#
# Mirrors the same scenarios as Go's internal/execution/risk_test.go, verifying
# the same semantics: stop-loss/take-profit prices come from the key level the
# support_resistance module detected at the moment of entry, not a fixed
# percentage. The two sides' logic must match -- if backtest and live execution
# don't use the same decision logic, the backtest is meaningless.


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
    """Same as make_decisions, but the triggering decision carries the given support_resistance signal."""
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
    # Support level at 95 (not a fixed percentage); dropping to 90 should exit around 95.
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
    # Short stop-loss is set at the resistance level (110); a rise to 112 should have already triggered it.
    assert result.trades[0].exit_reason == "stop_loss"
    assert result.trades[0].exit_price == Decimal("110")


def test_support_resistance_mode_skips_entry_when_level_unavailable():
    # No support level detected nearby (e.g. price at a historical low): must not open a position without stop-loss protection.
    candles = make_candles(["100"] * 8)
    decisions = make_decisions_with_signal(
        8, {1: Direction.LONG}, _support_resistance_signal(None, "110")
    )
    result = run(candles, decisions, default_risk(stop_loss_mode="support_resistance"))
    assert len(result.trades) == 0


def test_support_resistance_mode_opens_without_take_profit_when_resistance_unavailable():
    # Take-profit isn't a safety mechanism, unlike stop-loss: with a support level but no
    # resistance level (the most common case for a breakout entry), this trade must not be
    # rejected -- it just has no take-profit line -- stop-loss still applies normally.
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


# ---------- POC (volume profile point of control) stop-loss/take-profit ----------
#
# Mirrors the same scenario as Go's internal/execution/risk_test.go: POC is a
# single price that doesn't distinguish long/short -- stop-loss and take-profit
# use the same value.


def test_risk_pct_sizing_uses_stop_distance_and_equity():
    # Equity 10000, risk per trade 1% (=100), stop distance 5% -> position = 100 / 0.05 = 2000,
    # quantity = 2000 / entry price 100 = 20.
    candles = make_candles(["100", "100", "100", "100", "90", "90", "90", "90"])
    decisions = make_decisions(8, {1: Direction.LONG})
    result = run(
        candles, decisions,
        default_risk(
            max_position_size_quote=Decimal("1000000"),  # raise the cap so the default 1000 doesn't block it
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
    # Same computed position of 2000, but the hard cap is only 500 -- the whole trade should be rejected, not shrunk to 500.
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
    # support_resistance stop-loss mode, but no support level detected nearby -- the stop
    # distance can't be computed, so risk_pct mode should reject the entry just like fixed_quote does.
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
    # Stop-loss price equals entry price (distance = 0): the formula would divide by zero, so this should reject the entry rather than raise.
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
    # The position risk_pct mode computes (2000) is still subject to the existing
    # backtest-specific "current equity" cap: with only 1000 in starting capital, the
    # position should be clamped to 1000 (equivalent to min(risk_notional, equity)),
    # not the raw computed 2000 regardless of equity.
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
    assert result.trades[0].quantity == Decimal("10")  # 1000 equity / entry price 100


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
    # Long stop-loss uses POC; short stop-loss uses the same POC -- unlike support/resistance, which has separate upper/lower levels.
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
    """Once the daily loss limit is hit, no new positions may open that day."""
    # Each entry is immediately followed by a sharp drop, quickly accumulating losses.
    closes = ["100", "100", "80", "80", "100", "100", "80", "80", "100", "100"]
    candles = make_candles(closes, opens=closes)
    decisions = make_decisions(
        10, {1: Direction.LONG, 4: Direction.LONG, 7: Direction.LONG}
    )
    result = run(
        candles, decisions,
        default_risk(max_daily_loss_quote=Decimal("50"), stop_loss_pct=0.1),
    )
    # All candles fall on the same day; once the daily loss cap is hit, new entries should stop.
    assert len(result.trades) < 3, "should not keep opening positions after hitting the daily loss limit"


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


# ---------- In-sample / out-of-sample ----------


def test_in_and_out_of_sample_are_separated():
    candles = make_candles(["100"] * 20)
    decisions = make_decisions(20, {2: Direction.LONG, 15: Direction.LONG})
    # Add a max holding period so the first trade closes before the second signal fires, giving each segment one trade.
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
    # Each trade belongs to exactly one segment.
    assert len(in_trades) + len(out_trades) == len(result.trades)


def test_flags_when_out_of_sample_has_no_trades():
    """When out-of-sample produces no trades, it must be flagged as "unvalidated" rather than "mediocre performance"."""
    candles = make_candles(["100"] * 20)
    decisions = make_decisions(20, {2: Direction.LONG})  # only triggers in-sample
    result = run(candles, decisions, split_ratio=0.5)

    assert result.out_of_sample.trade_count == 0
    assert not result.parameters_validated_out_of_sample
    assert "No trades were produced" in result.summary()


def test_summary_puts_out_of_sample_first():
    """The summary must put out-of-sample in the most prominent spot, so users don't misread in-sample metrics as real performance."""
    candles = make_candles(["100"] * 20)
    decisions = make_decisions(20, {2: Direction.LONG, 15: Direction.LONG})
    text = run(
        candles, decisions,
        default_risk(max_holding_period_secs=2 * 3600),
        split_ratio=0.5,
    ).summary()
    assert text.index("[Out-of-sample]") < text.index("[In-sample]")


def test_trade_belongs_to_segment_where_decision_was_made():
    """A trade spanning the split point belongs to the segment it was entered in.

    If it were attributed by exit time instead, a decision made with
    in-sample information would get counted as out-of-sample, making the
    out-of-sample validation meaningless.
    """
    candles = make_candles(["100"] * 20)
    # Enters in-sample, holds all the way to the end of the data (which falls out-of-sample).
    decisions = make_decisions(20, {2: Direction.LONG})
    result = run(candles, decisions, split_ratio=0.5)

    assert len(result.trades) == 1
    trade = result.trades[0]
    assert trade.exit_time > result.split_at, "this trade does exit out-of-sample"
    assert trade.segment is Segment.IN_SAMPLE, "but it should belong to the in-sample segment where it was entered"
    assert result.out_of_sample.trade_count == 0


def test_split_ratio_must_leave_both_segments_nonempty():
    candles = make_candles(["100"] * 10)
    decisions = make_decisions(10)
    with pytest.raises(BacktestError):
        run(candles, decisions, split_ratio=1.5)


# ---------- Input validation ----------


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


# ---------- Explainability ----------


def test_trades_keep_trigger_signals():
    """Every trade must be able to answer "which module's which signal triggered this"."""
    candles = make_candles(["100"] * 8)
    decisions = make_decisions(8, {1: Direction.LONG})
    result = run(candles, decisions)

    assert result.trades[0].trigger_signals
    assert result.trades[0].trigger_signals[0]["module"] == "volume_breakout"


def test_equity_curve_tracks_every_bar():
    candles = make_candles(["100"] * 10)
    result = run(candles, make_decisions(10))
    # Initial point + one point per candle.
    assert len(result.equity_curve) == len(candles) + 1
