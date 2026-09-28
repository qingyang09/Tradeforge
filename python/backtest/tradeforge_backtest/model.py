"""Core data structures for the backtest engine.

Convention: all amounts, prices, and quantities use ``decimal.Decimal`` —
float is forbidden. Performance ratios (Sharpe, win rate, etc.) are
dimensionless statistics and may use float.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime
from decimal import Decimal
from enum import Enum
from typing import Any


class Direction(str, Enum):
    """Direction of a signal or position."""

    LONG = "LONG"
    SHORT = "SHORT"
    NEUTRAL = "NEUTRAL"

    @property
    def sign(self) -> int:
        """+1 for long, -1 for short, 0 for neutral."""
        return {Direction.LONG: 1, Direction.SHORT: -1}.get(self, 0)

    def opposite(self) -> "Direction":
        return {
            Direction.LONG: Direction.SHORT,
            Direction.SHORT: Direction.LONG,
        }.get(self, Direction.NEUTRAL)


class Segment(str, Enum):
    """In-sample vs. out-of-sample marker."""

    IN_SAMPLE = "in_sample"
    OUT_OF_SAMPLE = "out_of_sample"


@dataclass(frozen=True)
class Candle:
    """A single OHLCV candle."""

    open_time: datetime
    close_time: datetime
    open: Decimal
    high: Decimal
    low: Decimal
    close: Decimal
    volume: Decimal


@dataclass(frozen=True)
class Decision:
    """One decision produced by signal replay, corresponding to one candle."""

    index: int
    bar_time: datetime
    direction: Direction
    score: float
    triggered: bool
    price: Decimal
    reason: str
    signals: list[dict[str, Any]] = field(default_factory=list)


@dataclass(frozen=True)
class FeeModel:
    """Fee and slippage model.

    Bare-price backtesting is explicitly forbidden: a backtest that doesn't
    model cost will show a batch of genuinely lossy high-frequency strategies
    as profitable — that kind of result is more harmful than no backtest at
    all.
    """

    maker_fee_rate: Decimal = Decimal("0.0002")
    taker_fee_rate: Decimal = Decimal("0.0004")
    slippage_bps: Decimal = Decimal("5")

    def fill_price(self, mid: Decimal, side: Direction) -> Decimal:
        """Adjust the fill price for slippage.

        Slippage always works against the trader: price moves up on a buy,
        down on a sell.
        """
        slip = mid * self.slippage_bps / Decimal("10000")
        if side is Direction.LONG:
            return mid + slip
        if side is Direction.SHORT:
            return mid - slip
        return mid

    def fee(self, notional: Decimal) -> Decimal:
        """Compute the fee at the taker rate.

        Always using the taker rate is a deliberately conservative choice: a
        market order is a taker fill by definition, and whether a limit order
        would actually fill as a maker can't be known in a backtest —
        assuming maker fills would systematically understate cost.
        """
        return abs(notional) * self.taker_fee_rate


@dataclass(frozen=True)
class RiskConfig:
    """Per-symbol risk parameters.

    stop_loss_mode/take_profit_mode is either "pct" (default, a fixed
    percentage — see *_pct) or "support_resistance" (use the nearest
    support/resistance level the support_resistance module detected at the
    moment of entry as the threshold). This must stay semantically identical
    to Go's pkg/types.RiskConfig — both sides need to understand both modes,
    otherwise the stop-loss/take-profit the backtest computes and what live
    execution actually does are not the same strategy, which is more
    dangerous than not backtesting at all.

    position_sizing_mode is either "fixed_quote" (default, a fixed amount —
    see max_position_size_quote) or "risk_pct" (computed dynamically as
    account_equity_quote × risk_per_trade_pct ÷ stop-loss distance as a
    percentage). This must also stay semantically identical to Go's
    pkg/types.RiskConfig, and the formula must match exactly — if the two
    sides compute different position sizes, the backtest is evaluating a
    different strategy. max_position_size_quote applies in both modes: under
    risk_pct it's a hard cap on the computed position size — exceeding it
    rejects the whole trade rather than silently clamping it.
    """

    max_position_size_quote: Decimal
    max_daily_loss_quote: Decimal = Decimal(0)
    stop_loss_mode: str = "pct"
    stop_loss_pct: float = 0.0
    take_profit_mode: str = "pct"
    take_profit_pct: float = 0.0
    max_holding_period_secs: float = 0.0
    position_sizing_mode: str = "fixed_quote"
    account_equity_quote: Decimal = Decimal(0)
    risk_per_trade_pct: float = 0.0


@dataclass(frozen=True)
class Trade:
    """One complete round-trip trade."""

    entry_time: datetime
    exit_time: datetime
    direction: Direction
    entry_price: Decimal
    exit_price: Decimal
    quantity: Decimal
    pnl: Decimal
    fees: Decimal
    exit_reason: str
    segment: Segment
    trigger_signals: list[dict[str, Any]] = field(default_factory=list)

    @property
    def is_win(self) -> bool:
        return self.pnl > 0

    @property
    def gross_pnl(self) -> Decimal:
        """P&L before fees."""
        return self.pnl + self.fees


@dataclass
class Metrics:
    """Performance metrics over a period."""

    total_return: float = 0.0
    annualized_return: float = 0.0
    sharpe_ratio: float = 0.0
    sortino_ratio: float = 0.0
    max_drawdown: float = 0.0
    win_rate: float = 0.0
    profit_factor: float = 0.0
    trade_count: int = 0
    total_fees: Decimal = Decimal(0)
    final_equity: Decimal = Decimal(0)

    def to_dict(self) -> dict[str, Any]:
        return {
            "total_return": self.total_return,
            "annualized_return": self.annualized_return,
            "sharpe_ratio": self.sharpe_ratio,
            "sortino_ratio": self.sortino_ratio,
            "max_drawdown": self.max_drawdown,
            "win_rate": self.win_rate,
            "profit_factor": self.profit_factor,
            "trade_count": self.trade_count,
            "total_fees": str(self.total_fees),
            "final_equity": str(self.final_equity),
        }


@dataclass
class BacktestResult:
    """The complete output of one backtest run.

    Keeping ``in_sample`` and ``out_of_sample`` separate is a hard
    requirement: only the out-of-sample metrics represent "parameters that
    weren't tuned on this data." Mixing the two when displaying results
    means showing the user an overfit result as if it were real performance.
    """

    strategy_id: str
    symbol: str
    overall: Metrics
    in_sample: Metrics
    out_of_sample: Metrics
    trades: list[Trade]
    fee_model: FeeModel
    initial_capital: Decimal
    data_start: datetime
    data_end: datetime
    split_at: datetime
    engine_version: str
    ran_at: datetime
    equity_curve: list[tuple[datetime, Decimal]] = field(default_factory=list)

    @property
    def parameters_validated_out_of_sample(self) -> bool:
        """Whether the out-of-sample period actually produced any trades.

        When there isn't a single out-of-sample trade, the out-of-sample
        metrics are all zero — that's not "mediocre performance," it's "never
        actually validated." The display layer must be able to tell the two
        apart.
        """
        return self.out_of_sample.trade_count > 0

    def summary(self) -> str:
        """Human-readable summary that deliberately puts out-of-sample first."""
        lines = [
            f"Strategy {self.strategy_id}  Symbol {self.symbol}",
            f"Data range: {self.data_start:%Y-%m-%d} ~ {self.data_end:%Y-%m-%d}"
            f" (in/out-of-sample split at {self.split_at:%Y-%m-%d})",
            f"Fee {self.fee_model.taker_fee_rate}  Slippage {self.fee_model.slippage_bps} bps",
            "",
            "[Out-of-sample] -- the only basis for deciding whether the strategy can enter paper trading",
        ]
        if not self.parameters_validated_out_of_sample:
            lines.append("  No trades were produced in the out-of-sample period; this backtest did not meaningfully validate the strategy.")
        else:
            lines.append(_fmt_metrics(self.out_of_sample))
        lines += [
            "",
            "[In-sample] -- if parameters were tuned, they were tuned on this data; not representative of expected performance",
            _fmt_metrics(self.in_sample),
            "",
            "[Full period]",
            _fmt_metrics(self.overall),
        ]
        return "\n".join(lines)


def _fmt_metrics(m: Metrics) -> str:
    return (
        f"  Total return {m.total_return:+.2%}   Annualized {m.annualized_return:+.2%}   "
        f"Max drawdown {m.max_drawdown:.2%}\n"
        f"  Sharpe {m.sharpe_ratio:.2f}   Sortino {m.sortino_ratio:.2f}   "
        f"Win rate {m.win_rate:.2%}   Profit factor {m.profit_factor:.2f}\n"
        f"  Trades {m.trade_count}   Total fees {m.total_fees}   Final equity {m.final_equity}"
    )
