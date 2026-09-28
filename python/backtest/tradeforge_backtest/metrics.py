"""Performance metrics computation.

All metrics are derived from the per-candle equity curve, not trade-by-trade
P&L — a Sharpe computed from trade P&L ignores mark-to-market swings while a
position is open, which systematically inflates it.
"""

from __future__ import annotations

import math
from datetime import datetime
from decimal import Decimal

from .model import Metrics, Segment, Trade

# Seconds per year, used to annualize returns over an arbitrary period.
SECONDS_PER_YEAR = 365.25 * 24 * 3600


def compute_metrics(
    equity_curve: list[tuple[datetime, Decimal]],
    trades: list[Trade],
    initial_capital: Decimal,
    *,
    risk_free_rate: float = 0.0,
) -> Metrics:
    """Compute all performance metrics from the equity curve and trade log.

    equity_curve needs at least two points to compute a return; if it has
    fewer, this returns zero-valued metrics rather than raising — "too
    little data to draw a conclusion" is a normal outcome, not an error.
    """
    m = Metrics(
        trade_count=len(trades),
        total_fees=sum((t.fees for t in trades), Decimal(0)),
        final_equity=equity_curve[-1][1] if equity_curve else initial_capital,
    )

    if len(equity_curve) < 2 or initial_capital <= 0:
        return m

    m.total_return = float((m.final_equity - initial_capital) / initial_capital)
    m.max_drawdown = _max_drawdown(equity_curve)

    elapsed = (equity_curve[-1][0] - equity_curve[0][0]).total_seconds()
    m.annualized_return = _annualize(m.total_return, elapsed)

    returns = _bar_returns(equity_curve)
    periods_per_year = _periods_per_year(equity_curve)
    m.sharpe_ratio = _sharpe(returns, periods_per_year, risk_free_rate)
    m.sortino_ratio = _sortino(returns, periods_per_year, risk_free_rate)

    if trades:
        wins = [t for t in trades if t.is_win]
        m.win_rate = len(wins) / len(trades)
        m.profit_factor = _profit_factor(trades)

    return m


def metrics_for_segment(
    equity_curve: list[tuple[datetime, Decimal]],
    trades: list[Trade],
    segment: Segment,
    split_at: datetime,
    initial_capital: Decimal,
) -> Metrics:
    """Compute metrics using only one segment's (in- or out-of-sample) data.

    The out-of-sample starting equity is the equity at the split point, not
    the original initial capital — otherwise the out-of-sample return would
    include gains (or losses) made during the in-sample period.
    """
    if segment is Segment.IN_SAMPLE:
        curve = [(t, v) for t, v in equity_curve if t <= split_at]
        seg_trades = [t for t in trades if t.segment is Segment.IN_SAMPLE]
        capital = initial_capital
    else:
        curve = [(t, v) for t, v in equity_curve if t >= split_at]
        seg_trades = [t for t in trades if t.segment is Segment.OUT_OF_SAMPLE]
        capital = curve[0][1] if curve else initial_capital

    return compute_metrics(curve, seg_trades, capital)


def _bar_returns(curve: list[tuple[datetime, Decimal]]) -> list[float]:
    """Simple per-candle return series."""
    out: list[float] = []
    for i in range(1, len(curve)):
        prev = curve[i - 1][1]
        if prev <= 0:
            # No meaningful return once equity has hit zero — stop here.
            break
        out.append(float((curve[i][1] - prev) / prev))
    return out


def _periods_per_year(curve: list[tuple[datetime, Decimal]]) -> float:
    """Infer the annualization factor from the equity curve's actual sampling interval.

    We don't hard-code a constant like "daily = 252": the same backtest
    engine serves every timeframe from 1m to 1d, and only inferring it from
    the data itself avoids getting it wrong when the timeframe changes.
    """
    if len(curve) < 2:
        return 1.0
    total = (curve[-1][0] - curve[0][0]).total_seconds()
    if total <= 0:
        return 1.0
    avg_interval = total / (len(curve) - 1)
    return SECONDS_PER_YEAR / avg_interval


def _annualize(total_return: float, elapsed_secs: float) -> float:
    if elapsed_secs <= 0:
        return 0.0
    years = elapsed_secs / SECONDS_PER_YEAR
    if years <= 0:
        return 0.0
    growth = 1.0 + total_return
    if growth <= 0:
        # Capital wiped out — record annualized return as -100%, don't
        # extrapolate compounding.
        return -1.0
    return growth ** (1.0 / years) - 1.0


def _sharpe(returns: list[float], periods_per_year: float, rf: float) -> float:
    if len(returns) < 2:
        return 0.0
    excess = [r - rf / periods_per_year for r in returns]
    mean = sum(excess) / len(excess)
    var = sum((r - mean) ** 2 for r in excess) / (len(excess) - 1)
    std = math.sqrt(var)
    if std == 0:
        return 0.0
    return mean / std * math.sqrt(periods_per_year)


def _sortino(returns: list[float], periods_per_year: float, rf: float) -> float:
    """Sortino ratio: uses only downside deviation as the denominator.

    Unlike Sharpe, upside volatility doesn't count as risk — fairer for
    strategies with skewed return distributions.
    """
    if len(returns) < 2:
        return 0.0
    excess = [r - rf / periods_per_year for r in returns]
    mean = sum(excess) / len(excess)
    downside = [r for r in excess if r < 0]
    if not downside:
        # No downside deviation at all. Return 0 rather than inf: showing
        # the user an "infinite Sharpe" is just misleading, and usually
        # means the sample is too small.
        return 0.0
    dd = math.sqrt(sum(r**2 for r in downside) / len(downside))
    if dd == 0:
        return 0.0
    return mean / dd * math.sqrt(periods_per_year)


def _max_drawdown(curve: list[tuple[datetime, Decimal]]) -> float:
    """Max drawdown, returned as a positive number (0.2 = a 20% deepest drawdown)."""
    peak = curve[0][1]
    worst = 0.0
    for _, equity in curve:
        if equity > peak:
            peak = equity
        if peak > 0:
            dd = float((peak - equity) / peak)
            worst = max(worst, dd)
    return worst


def _profit_factor(trades: list[Trade]) -> float:
    """Profit factor = total gains / total losses."""
    gains = sum((t.pnl for t in trades if t.pnl > 0), Decimal(0))
    losses = sum((-t.pnl for t in trades if t.pnl < 0), Decimal(0))
    if losses == 0:
        # No losing trades at all. Again, don't return inf — no real
        # strategy actually has this property; when it shows up it's
        # almost always too small a sample.
        return 0.0 if gains == 0 else float(gains)
    return float(gains / losses)
