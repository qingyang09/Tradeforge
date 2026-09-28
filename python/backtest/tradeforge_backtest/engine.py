"""Backtest matching engine: turns a decision stream + candles into trade records and an equity curve."""

from __future__ import annotations

from datetime import datetime, timedelta, timezone
from decimal import Decimal
from typing import Any, Iterable

from .metrics import compute_metrics, metrics_for_segment
from .model import (
    BacktestResult,
    Candle,
    Decision,
    Direction,
    FeeModel,
    RiskConfig,
    Segment,
    Trade,
)

DEFAULT_INITIAL_CAPITAL = Decimal("10000")
# Default in-sample / out-of-sample split ratio: first 70% training, last 30% validation.
DEFAULT_SPLIT_RATIO = 0.7


class BacktestError(Exception):
    """Raised when backtest input is invalid."""


class _Position:
    """Current position. Mutable internally, not exposed externally."""

    __slots__ = (
        "direction", "quantity", "entry_price", "entry_time",
        "entry_fee", "signals", "segment", "stop_loss_price", "take_profit_price",
    )

    def __init__(
        self,
        direction: Direction,
        quantity: Decimal,
        entry_price: Decimal,
        entry_time: datetime,
        entry_fee: Decimal,
        signals: list[dict[str, Any]],
        segment: Segment,
        stop_loss_price: Decimal | None = None,
        take_profit_price: Decimal | None = None,
    ) -> None:
        self.direction = direction
        self.quantity = quantity
        self.entry_price = entry_price
        self.entry_time = entry_time
        self.entry_fee = entry_fee
        self.signals = signals
        # Trade attribution is decided by which segment the DECISION was made in, not
        # which segment it was closed in. A trade opened in-sample and closed
        # out-of-sample, if counted as out-of-sample, would contaminate the
        # out-of-sample metrics with a decision made using in-sample information —
        # which defeats the purpose of the validation.
        self.segment = segment
        # Absolute stop-loss/take-profit price computed at entry time; None means it
        # wasn't set. Regardless of whether the mode was a fixed percentage or
        # support_resistance, once the position is opened it's converted to and stored
        # as an absolute price here — checking exit conditions later only needs a price
        # comparison, not knowledge of which mode produced it (same approach as
        # Position.StopLossPrice in internal/execution/risk.go on the Go side).
        self.stop_loss_price = stop_loss_price
        self.take_profit_price = take_profit_price

    def unrealized(self, price: Decimal) -> Decimal:
        diff = price - self.entry_price
        if self.direction is Direction.SHORT:
            diff = -diff
        return diff * self.quantity


def run_backtest(
    *,
    strategy_id: str,
    symbol: str,
    candles: list[Candle],
    decisions: list[Decision],
    risk: RiskConfig,
    fee_model: FeeModel | None = None,
    initial_capital: Decimal = DEFAULT_INITIAL_CAPITAL,
    split_ratio: float = DEFAULT_SPLIT_RATIO,
    engine_version: str = "unknown",
) -> BacktestResult:
    """Simulate executing a decision stream against historical data.

    Fill assumptions (deliberately conservative — better to underestimate than
    overestimate):

    * A signal is produced at the close of candle i; the fill happens at the open of
      candle i+1. Filling at the close of the same candle would mean trading at a price
      that wasn't actually known yet — the most common and most damaging form of
      look-ahead bias in a backtest.
    * The fill price is then adjusted for slippage, always against the trader.
    * Fees are charged at the taker rate, once on entry and once on exit.
    """
    fee_model = fee_model or FeeModel()
    if not candles:
        raise BacktestError("no candle data")
    if not decisions:
        raise BacktestError("no decision data")
    if len(decisions) != len(candles):
        raise BacktestError(
            f"决策数 {len(decisions)} 与 K 线数 {len(candles)} 不一致，"
            "两者必须逐根对应"
        )
    if initial_capital <= 0:
        raise BacktestError("initial capital must be positive")
    if risk.max_position_size_quote <= 0:
        raise BacktestError("max position size per trade must be positive")

    split_index = _split_index(len(candles), split_ratio)
    split_at = candles[split_index].close_time

    equity = initial_capital
    position: _Position | None = None
    trades: list[Trade] = []
    equity_curve: list[tuple[datetime, Decimal]] = [(candles[0].close_time, equity)]
    # Cumulative realized loss for the day, used to halt trading via risk control.
    day_key: str | None = None
    day_loss = Decimal(0)
    halted_days: set[str] = set()

    for i, candle in enumerate(candles):
        decision = decisions[i]
        today = candle.close_time.strftime("%Y-%m-%d")
        if today != day_key:
            day_key, day_loss = today, Decimal(0)

        # ---- 1. First handle exit conditions for any existing position (stop-loss/take-profit/timeout/opposite signal) ----
        if position is not None:
            exit_reason = _exit_reason(position, candle, decision, risk)
            if exit_reason is not None:
                trade, realized = _close(
                    position, candle, fee_model, exit_reason,
                )
                trades.append(trade)
                equity += realized
                if realized < 0:
                    day_loss += -realized
                position = None

        # ---- 2. Daily-loss risk control: once the cap is hit, no new positions today ----
        if (
            risk.max_daily_loss_quote > 0
            and day_loss >= risk.max_daily_loss_quote
            and today not in halted_days
        ):
            halted_days.add(today)

        # ---- 3. Handle opening a position ----
        can_open = (
            position is None
            and decision.triggered
            and decision.direction is not Direction.NEUTRAL
            and today not in halted_days
            and i + 1 < len(candles)  # need the next candle's open to fill
        )
        if can_open:
            position = _open(
                decision, candles[i + 1], fee_model, risk, equity,
                _segment_of(i, split_index),
            )
            if position is not None:
                equity -= position.entry_fee

        # ---- 4. Record equity (including unrealized P&L) ----
        mark = equity
        if position is not None:
            mark = equity + position.unrealized(candle.close)
        equity_curve.append((candle.close_time, mark))

    # Still holding a position when the data ends — force-close at the last candle's close.
    if position is not None:
        trade, realized = _close(
            position, candles[-1], fee_model, "end_of_data",
        )
        trades.append(trade)
        equity += realized
        equity_curve[-1] = (candles[-1].close_time, equity)

    overall = compute_metrics(equity_curve, trades, initial_capital)
    return BacktestResult(
        strategy_id=strategy_id,
        symbol=symbol,
        overall=overall,
        in_sample=metrics_for_segment(
            equity_curve, trades, Segment.IN_SAMPLE, split_at, initial_capital
        ),
        out_of_sample=metrics_for_segment(
            equity_curve, trades, Segment.OUT_OF_SAMPLE, split_at, initial_capital
        ),
        trades=trades,
        fee_model=fee_model,
        initial_capital=initial_capital,
        data_start=candles[0].open_time,
        data_end=candles[-1].close_time,
        split_at=split_at,
        engine_version=engine_version,
        ran_at=datetime.now(timezone.utc).replace(tzinfo=None),
        equity_curve=equity_curve,
    )


def _split_index(n: int, ratio: float) -> int:
    """Return the last index of the in-sample range."""
    if not 0 < ratio < 1:
        raise BacktestError(f"in-sample ratio must be in (0, 1), got {ratio}")
    idx = int(n * ratio)
    # Both segments must have at least one candle, or "out-of-sample validation" would
    # be validation in name only.
    return max(0, min(idx, n - 2))


def _segment_of(i: int, split_index: int) -> Segment:
    return Segment.IN_SAMPLE if i <= split_index else Segment.OUT_OF_SAMPLE


class _StopLevelUnavailable(Exception):
    """Raised when the absolute stop-loss/take-profit price can't be resolved; _open() uses this to refuse to open the position."""


class _PositionSizeUnavailable(Exception):
    """Raised when position size can't be computed under risk-percent sizing (e.g. stop distance is 0); _open() uses this to skip opening the position."""


def _resolve_position_size_quote(
    risk: RiskConfig, entry: Decimal, stop_loss_price: Decimal | None
) -> Decimal:
    """Compute the notional size (in quote currency) for opening a position. This is
    the same formula as ResolvePositionSizeQuote in internal/execution/risk.go on the
    Go side, and the two must match exactly — otherwise the backtest and live trading
    would be evaluating different strategies.

    This deliberately does NOT clamp the result to max_position_size_quote — that cap
    is checked by the caller (_open), which rejects the whole trade outright rather
    than silently shrinking it, for the same reason as the Go side.
    """
    mode = risk.position_sizing_mode or "fixed_quote"
    if mode == "fixed_quote":
        return risk.max_position_size_quote
    if mode == "risk_pct":
        if not stop_loss_price or stop_loss_price <= 0:
            raise _PositionSizeUnavailable("risk-percent sizing requires a resolvable stop-loss price, but none was set or could be resolved for this trade")
        stop_distance = abs(entry - stop_loss_price)
        if stop_distance <= 0:
            raise _PositionSizeUnavailable("stop-loss price equals entry price (zero stop distance); can't size the position from this")
        stop_distance_pct = stop_distance / entry
        risk_amount = risk.account_equity_quote * Decimal(str(risk.risk_per_trade_pct))
        return risk_amount / stop_distance_pct
    raise _PositionSizeUnavailable(f"unsupported position sizing mode {mode!r}")


def _resolve_level_price(signals: list[dict[str, Any]], want_support: bool, purpose: str) -> Decimal:
    """Pull the nearest support/resistance level detected by the support_resistance
    module out of the decision's signals.

    Same logic as levelPrice in internal/execution/risk.go on the Go side: signals is
    the raw dict deserialized from JSONL, shaped the same as Go's types.Signal
    (module/raw, with nearest_support/nearest_resistance inside raw).
    """
    sig = next((s for s in signals if s.get("module") == "support_resistance"), None)
    if sig is None:
        raise _StopLevelUnavailable(f"{purpose} requires data from the support_resistance module, but this decision's signals don't include it")
    if sig.get("degraded"):
        raise _StopLevelUnavailable(f"{purpose} requires data from the support_resistance module, but that module's signal was degraded this time")

    key, label = ("nearest_support", "support level") if want_support else ("nearest_resistance", "resistance level")
    level = (sig.get("raw") or {}).get(key)
    if not level:
        raise _StopLevelUnavailable(f"{purpose}: no {label} detected near the current price, can't set {purpose} from it")
    price_str = level.get("price")
    if not price_str:
        raise _StopLevelUnavailable(f"{purpose}: {label} data is missing the price field")
    try:
        return Decimal(str(price_str))
    except Exception as exc:  # noqa: BLE001 - convert to a uniform domain exception so callers don't need to care about the specific cause
        raise _StopLevelUnavailable(f"{purpose}: failed to parse {label} price {price_str!r}: {exc}") from exc


def _resolve_poc_price(signals: list[dict[str, Any]], purpose: str) -> Decimal:
    """Pull the volume-profile point of control computed by the poc module out of the
    decision's signals. POC has only one price — it doesn't distinguish long/short or
    stop-loss/take-profit direction. Same logic as pocPrice in
    internal/execution/risk.go on the Go side.
    """
    sig = next((s for s in signals if s.get("module") == "poc"), None)
    if sig is None:
        raise _StopLevelUnavailable(f"{purpose} requires data from the poc module, but this decision's signals don't include it")
    if sig.get("degraded"):
        raise _StopLevelUnavailable(f"{purpose} requires data from the poc module, but that module's signal was degraded this time")
    price_str = (sig.get("raw") or {}).get("poc_price")
    if not price_str:
        raise _StopLevelUnavailable(f"{purpose}: the poc module's signal didn't produce a valid poc_price, can't set {purpose} from it")
    try:
        return Decimal(str(price_str))
    except Exception as exc:  # noqa: BLE001
        raise _StopLevelUnavailable(f"{purpose}: failed to parse POC price {price_str!r}: {exc}") from exc


def _resolve_stop_loss_price(
    risk: RiskConfig, direction: Direction, entry: Decimal, signals: list[dict[str, Any]]
) -> Decimal | None:
    """Compute the absolute stop-loss price at entry time; None means no stop-loss is set (pct is 0)."""
    mode = risk.stop_loss_mode or "pct"
    if mode == "pct":
        if risk.stop_loss_pct <= 0:
            return None
        pct = Decimal(str(risk.stop_loss_pct))
        if direction is Direction.LONG:
            return entry * (Decimal(1) - pct)
        return entry * (Decimal(1) + pct)
    if mode == "support_resistance":
        # Long stop-loss sits at the support level; short stop-loss sits at the resistance level.
        return _resolve_level_price(signals, direction is Direction.LONG, "stop-loss")
    if mode == "poc":
        return _resolve_poc_price(signals, "stop-loss")
    raise _StopLevelUnavailable(f"unsupported stop-loss mode {mode!r}")


def _resolve_take_profit_price(
    risk: RiskConfig, direction: Direction, entry: Decimal, signals: list[dict[str, Any]]
) -> Decimal | None:
    """Compute the absolute take-profit price at entry time; None means no take-profit is set (pct is 0)."""
    mode = risk.take_profit_mode or "pct"
    if mode == "pct":
        if risk.take_profit_pct <= 0:
            return None
        pct = Decimal(str(risk.take_profit_pct))
        if direction is Direction.LONG:
            return entry * (Decimal(1) + pct)
        return entry * (Decimal(1) - pct)
    if mode == "support_resistance":
        # Long take-profit sits at the resistance level; short take-profit sits at the support level — opposite of stop-loss.
        return _resolve_level_price(signals, direction is Direction.SHORT, "take-profit")
    if mode == "poc":
        return _resolve_poc_price(signals, "take-profit")
    raise _StopLevelUnavailable(f"unsupported take-profit mode {mode!r}")


def _open(
    decision: Decision,
    next_candle: Candle,
    fee_model: FeeModel,
    risk: RiskConfig,
    equity: Decimal,
    segment: Segment,
) -> _Position | None:
    """Open a position at the next candle's open price."""
    fill = fee_model.fill_price(next_candle.open, decision.direction)
    if fill <= 0:
        return None

    # If the stop-loss can't be resolved (e.g. support_resistance mode configured but
    # no key level detected nearby), the position must not be opened — the user
    # explicitly asked for stop-loss protection, and opening without it would mean not
    # faithfully executing their rule.
    #
    # This has to happen before sizing: risk_pct sizing needs the stop distance to
    # compute position size. fixed_quote mode doesn't need it, but the order is kept
    # uniform rather than maintaining two separate flows for the two modes (matches the
    # ordering in Worker.openPosition on the Go side).
    try:
        stop_loss_price = _resolve_stop_loss_price(risk, decision.direction, fill, decision.signals)
    except _StopLevelUnavailable:
        return None

    try:
        risk_notional = _resolve_position_size_quote(risk, fill, stop_loss_price)
    except _PositionSizeUnavailable:
        return None

    # The hard cap rejects rather than clamps — silently shrinking the position would
    # break the "this position corresponds to N% equity risk" semantics the user
    # explicitly asked for, consistent with the max_position_size rejection semantics in
    # RiskManager.CheckOpen on the Go side.
    if risk.max_position_size_quote > 0 and risk_notional > risk.max_position_size_quote:
        return None

    # Take the smaller of that and current equity too: as equity shrinks, the position
    # size should shrink with it, or the backtest would be assuming a wallet that never
    # runs dry. This is backtest-specific simulation behavior that predates this
    # change; live trading on the Go side has no equivalent (no account-balance concept
    # at all) — it's kept uniformly across both sizing modes without touching it, and
    # it's not to be conflated with account_equity_quote in risk_pct mode: that's a
    # static number the user declared, while equity is the rolling simulated equity
    # during the backtest — conceptually two different things.
    notional = min(risk_notional, equity)
    if notional <= 0:
        return None

    quantity = notional / fill
    if quantity <= 0:
        return None

    # An unresolvable take-profit does not block opening the position — this trade
    # just won't have a take-profit line. Take-profit isn't a safety mechanism, unlike
    # stop-loss, so the two are asymmetric: a breakout entry is exactly the most common
    # case where there's no nearby resistance level to use as a target. Same decision
    # as Worker.openPosition on the Go side; the two must agree, or the backtest and
    # live trading would be evaluating different strategies.
    try:
        take_profit_price = _resolve_take_profit_price(risk, decision.direction, fill, decision.signals)
    except _StopLevelUnavailable:
        take_profit_price = None

    return _Position(
        direction=decision.direction,
        quantity=quantity,
        stop_loss_price=stop_loss_price,
        take_profit_price=take_profit_price,
        entry_price=fill,
        entry_time=next_candle.open_time,
        entry_fee=fee_model.fee(notional),
        signals=decision.signals,
        segment=segment,
    )


def _exit_reason(
    position: _Position,
    candle: Candle,
    decision: Decision,
    risk: RiskConfig,
) -> str | None:
    """Decide whether the position should be closed, returning the exit reason;
    returns None if it should stay open.

    Priority: stop-loss > take-profit > timeout > opposite signal. Stop-loss ranks
    above take-profit as the conservative choice: when a single candle touches both
    stop-loss and take-profit, there's no way to know which happened first, so assume
    the less favorable one.

    The stop-loss/take-profit thresholds are the absolute prices computed at entry
    time and stored on position (_resolve_stop_loss_price/_resolve_take_profit_price,
    see _open) — regardless of whether the mode was a fixed percentage or
    support_resistance, this only needs to compare the candle's high/low against the
    threshold; both modes share the same check.
    """
    if position.stop_loss_price is not None:
        if position.direction is Direction.LONG:
            if candle.low <= position.stop_loss_price:
                return "stop_loss"
        elif candle.high >= position.stop_loss_price:
            return "stop_loss"

    if position.take_profit_price is not None:
        if position.direction is Direction.LONG:
            if candle.high >= position.take_profit_price:
                return "take_profit"
        elif candle.low <= position.take_profit_price:
            return "take_profit"

    if risk.max_holding_period_secs > 0:
        held = (candle.close_time - position.entry_time).total_seconds()
        if held >= risk.max_holding_period_secs:
            return "max_holding"

    if (
        decision.triggered
        and decision.direction is position.direction.opposite()
    ):
        return "signal"

    return None


def _close(
    position: _Position,
    candle: Candle,
    fee_model: FeeModel,
    reason: str,
) -> tuple[Trade, Decimal]:
    """Close the position and return (trade record, net realized P&L for this trade).

    Net P&L already has both the entry and exit fees deducted. The entry fee was
    already subtracted from equity when the position was opened, so the return value
    here only deducts the exit fee once more, avoiding double-counting.
    """
    exit_price = _exit_price(position, candle, reason, fee_model)
    notional = exit_price * position.quantity
    exit_fee = fee_model.fee(notional)

    diff = exit_price - position.entry_price
    if position.direction is Direction.SHORT:
        diff = -diff
    gross = diff * position.quantity
    net = gross - position.entry_fee - exit_fee

    trade = Trade(
        entry_time=position.entry_time,
        exit_time=candle.close_time,
        direction=position.direction,
        entry_price=position.entry_price,
        exit_price=exit_price,
        quantity=position.quantity,
        pnl=net,
        fees=position.entry_fee + exit_fee,
        exit_reason=reason,
        segment=position.segment,
        trigger_signals=position.signals,
    )
    # The entry fee was already deducted when the position was opened, so this returns
    # gross - exit_fee.
    return trade, gross - exit_fee


def _exit_price(
    position: _Position,
    candle: Candle,
    reason: str,
    fee_model: FeeModel,
) -> Decimal:
    """Determine the fill price based on the exit reason.

    Stop-loss / take-profit fill at the trigger price (the absolute price computed at
    entry time, see _open); everything else fills at the close. All three still go
    through slippage: a real stop order in a volatile market usually fills worse than
    its trigger price.
    """
    if reason == "stop_loss" and position.stop_loss_price is not None:
        raw = position.stop_loss_price
    elif reason == "take_profit" and position.take_profit_price is not None:
        raw = position.take_profit_price
    else:
        raw = candle.close

    # The exit side is opposite the position's direction, so slippage acts in the
    # opposite direction too.
    return fee_model.fill_price(raw, position.direction.opposite())


def decisions_from_iter(raw: Iterable[dict[str, Any]]) -> list[Decision]:
    """Convert a stream of dicts parsed from JSONL into a list of Decision."""
    out: list[Decision] = []
    for item in raw:
        if item.get("type") != "decision":
            continue
        out.append(
            Decision(
                index=item["index"],
                bar_time=_parse_time(item["bar_time"]),
                direction=Direction(item["direction"]),
                score=float(item.get("score", 0.0)),
                triggered=bool(item.get("triggered", False)),
                price=Decimal(str(item.get("price", "0"))),
                reason=item.get("reason", ""),
                signals=item.get("signals") or [],
            )
        )
    return out


def _parse_time(s: str) -> datetime:
    """Parse an RFC3339 timestamp emitted by the Go side."""
    cleaned = s.replace("Z", "+00:00")
    dt = datetime.fromisoformat(cleaned)
    return dt.replace(tzinfo=None) if dt.tzinfo else dt


__all__ = [
    "BacktestError",
    "DEFAULT_INITIAL_CAPITAL",
    "DEFAULT_SPLIT_RATIO",
    "decisions_from_iter",
    "run_backtest",
    "timedelta",
]
