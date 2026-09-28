"""回测的核心数据结构。

约定：所有金额、价格、数量一律使用 ``decimal.Decimal``，禁止 float。
绩效比率（夏普、胜率等）是无量纲统计量，允许使用 float。
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime
from decimal import Decimal
from enum import Enum
from typing import Any


class Direction(str, Enum):
    """信号或持仓方向。"""

    LONG = "LONG"
    SHORT = "SHORT"
    NEUTRAL = "NEUTRAL"

    @property
    def sign(self) -> int:
        """多头 +1、空头 -1、中性 0。"""
        return {Direction.LONG: 1, Direction.SHORT: -1}.get(self, 0)

    def opposite(self) -> "Direction":
        return {
            Direction.LONG: Direction.SHORT,
            Direction.SHORT: Direction.LONG,
        }.get(self, Direction.NEUTRAL)


class Segment(str, Enum):
    """样本内 / 样本外标记。"""

    IN_SAMPLE = "in_sample"
    OUT_OF_SAMPLE = "out_of_sample"


@dataclass(frozen=True)
class Candle:
    """一根 K 线。"""

    open_time: datetime
    close_time: datetime
    open: Decimal
    high: Decimal
    low: Decimal
    close: Decimal
    volume: Decimal


@dataclass(frozen=True)
class Decision:
    """信号重放产出的一条决策，对应一根 K 线。"""

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
    """手续费与滑点建模。

    裸价格回测是被明确禁止的：不建模成本的回测会把一批实际亏损的高频策略
    显示成盈利，这类结果比没有回测更有害。
    """

    maker_fee_rate: Decimal = Decimal("0.0002")
    taker_fee_rate: Decimal = Decimal("0.0004")
    slippage_bps: Decimal = Decimal("5")

    def fill_price(self, mid: Decimal, side: Direction) -> Decimal:
        """按滑点调整成交价。

        滑点永远对交易者不利：买入时价格上浮，卖出时价格下压。
        """
        slip = mid * self.slippage_bps / Decimal("10000")
        if side is Direction.LONG:
            return mid + slip
        if side is Direction.SHORT:
            return mid - slip
        return mid

    def fee(self, notional: Decimal) -> Decimal:
        """按吃单费率计算手续费。

        统一按 taker 计算是有意的保守取向：市价单本来就是吃单，
        而限价单能否成交在回测里无从判断，假设成 maker 会系统性低估成本。
        """
        return abs(notional) * self.taker_fee_rate


@dataclass(frozen=True)
class RiskConfig:
    """标的级别的风控参数。

    stop_loss_mode/take_profit_mode 是 "pct"（默认，固定百分比，见 *_pct）或
    "support_resistance"（用 support_resistance 模块在开仓那一刻检测到的最近支撑/阻力位
    当阈值）。跟 Go 侧 pkg/types.RiskConfig 保持同一套语义——两边都要理解这两种模式，
    否则回测算出来的止损止盈跟实盘执行的不是同一个策略，比没有回测更危险。

    position_sizing_mode 是 "fixed_quote"（默认，固定金额，见 max_position_size_quote）
    或 "risk_pct"（按 account_equity_quote × risk_per_trade_pct ÷ 止损距离百分比动态算）。
    跟 Go 侧 pkg/types.RiskConfig 保持同一套语义，公式必须一字不差地一致——两边算出来
    不是同一个仓位，回测就是在评估另一个策略。max_position_size_quote 在两种模式下都
    生效：risk_pct 模式下它是算出来的仓位的硬上限，超过就整笔拒绝，不做静默裁剪。
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
    """一笔完整的往返交易。"""

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
        """未扣手续费的毛盈亏。"""
        return self.pnl + self.fees


@dataclass
class Metrics:
    """一段区间上的绩效指标。"""

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
    """一次回测的完整产物。

    ``in_sample`` 与 ``out_of_sample`` 分开保存是硬性要求：
    只有样本外指标才代表"参数没有在这段数据上被调过"。
    展示时混用两者，等于把过拟合的结果当成真实表现给用户看。
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
        """样本外区间是否真的产生了交易。

        样本外一笔交易都没有时，样本外指标全是零值——那不是"表现平平"，
        而是"根本没有验证过"。展示层必须能区分这两种情况。
        """
        return self.out_of_sample.trade_count > 0

    def summary(self) -> str:
        """人类可读的摘要，刻意把样本外放在最显眼的位置。"""
        lines = [
            f"策略 {self.strategy_id}  标的 {self.symbol}",
            f"数据区间：{self.data_start:%Y-%m-%d} ~ {self.data_end:%Y-%m-%d}"
            f"（样本内/外分割点 {self.split_at:%Y-%m-%d}）",
            f"手续费 {self.fee_model.taker_fee_rate}  滑点 {self.fee_model.slippage_bps} bps",
            "",
            "【样本外】——判断策略能否进入模拟盘的唯一依据",
        ]
        if not self.parameters_validated_out_of_sample:
            lines.append("  样本外区间内没有产生任何交易，本次回测未对策略形成有效验证。")
        else:
            lines.append(_fmt_metrics(self.out_of_sample))
        lines += [
            "",
            "【样本内】——参数若经过调优，是在这段数据上调的，不可作为预期表现",
            _fmt_metrics(self.in_sample),
            "",
            "【全区间】",
            _fmt_metrics(self.overall),
        ]
        return "\n".join(lines)


def _fmt_metrics(m: Metrics) -> str:
    return (
        f"  总收益 {m.total_return:+.2%}   年化 {m.annualized_return:+.2%}   "
        f"最大回撤 {m.max_drawdown:.2%}\n"
        f"  夏普 {m.sharpe_ratio:.2f}   索提诺 {m.sortino_ratio:.2f}   "
        f"胜率 {m.win_rate:.2%}   盈亏比 {m.profit_factor:.2f}\n"
        f"  交易 {m.trade_count} 笔   手续费合计 {m.total_fees}   期末权益 {m.final_equity}"
    )
