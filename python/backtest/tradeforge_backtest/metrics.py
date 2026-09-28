"""绩效指标计算。

所有指标都基于逐根 K 线的权益曲线，而不是逐笔交易的盈亏——
后者算出的夏普会忽略持仓期间的浮动波动，系统性偏高。
"""

from __future__ import annotations

import math
from datetime import datetime
from decimal import Decimal

from .model import Metrics, Segment, Trade

# 每年的秒数，用于把任意周期的收益折算成年化。
SECONDS_PER_YEAR = 365.25 * 24 * 3600


def compute_metrics(
    equity_curve: list[tuple[datetime, Decimal]],
    trades: list[Trade],
    initial_capital: Decimal,
    *,
    risk_free_rate: float = 0.0,
) -> Metrics:
    """由权益曲线与交易明细计算全部绩效指标。

    equity_curve 至少要有两个点才能算出收益；不足时返回零值指标，
    而不是抛异常——"数据太少所以没有结论"是正常情况。
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
    """只用某一段（样本内或样本外）的数据计算指标。

    样本外的起始权益取分割点当时的权益，而不是最初的本金——
    否则样本外收益里会掺进样本内赚到（或亏掉）的部分。
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
    """逐根 K 线的简单收益率序列。"""
    out: list[float] = []
    for i in range(1, len(curve)):
        prev = curve[i - 1][1]
        if prev <= 0:
            # 权益归零后再谈收益率没有意义，直接截断。
            break
        out.append(float((curve[i][1] - prev) / prev))
    return out


def _periods_per_year(curve: list[tuple[datetime, Decimal]]) -> float:
    """由权益曲线的实际采样间隔推断年化因子。

    不硬编码"日线 = 252"这类常数：同一个回测引擎要服务 1m 到 1d 的所有周期，
    从数据本身推断才不会在换周期时算错。
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
        # 本金亏光，年化收益记为 -100%，不做复利外推。
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
    """索提诺比率：只用下行波动做分母。

    与夏普的区别在于，向上的波动不算作风险——这对偏度大的策略更公平。
    """
    if len(returns) < 2:
        return 0.0
    excess = [r - rf / periods_per_year for r in returns]
    mean = sum(excess) / len(excess)
    downside = [r for r in excess if r < 0]
    if not downside:
        # 没有任何下行波动。返回 0 而不是 inf：一个"无穷大夏普"展示给用户
        # 只会造成误导，通常意味着样本太少。
        return 0.0
    dd = math.sqrt(sum(r**2 for r in downside) / len(downside))
    if dd == 0:
        return 0.0
    return mean / dd * math.sqrt(periods_per_year)


def _max_drawdown(curve: list[tuple[datetime, Decimal]]) -> float:
    """最大回撤，返回正数（0.2 表示最深回撤 20%）。"""
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
    """盈亏比 = 总盈利 / 总亏损。"""
    gains = sum((t.pnl for t in trades if t.pnl > 0), Decimal(0))
    losses = sum((-t.pnl for t in trades if t.pnl < 0), Decimal(0))
    if losses == 0:
        # 一笔亏损都没有。同样不返回 inf——真实策略不存在这种情况，
        # 出现时几乎总是样本太少。
        return 0.0 if gains == 0 else float(gains)
    return float(gains / losses)
