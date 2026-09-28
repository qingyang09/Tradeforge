"""回测撮合引擎：把决策流 + K 线变成交易明细与权益曲线。"""

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
# 样本内 / 样本外的默认切分比例：前 70% 训练，后 30% 验证。
DEFAULT_SPLIT_RATIO = 0.7


class BacktestError(Exception):
    """回测输入有问题时抛出。"""


class _Position:
    """当前持仓。内部可变，不对外暴露。"""

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
        # 交易归属由"决策在哪一段做出"决定，而不是它在哪一段平仓。
        # 一笔样本内开仓、样本外平仓的交易若算进样本外，
        # 样本外指标就掺进了用样本内信息做出的决策，验证也就失效了。
        self.segment = segment
        # 开仓那一刻算好的绝对止损/止盈价格，None 表示未设置。不管当初是固定百分比还是
        # support_resistance 模式，一旦开仓都换算成绝对价格存在这里——后续判断退出条件
        # 只需要比较价格，不需要知道当初是哪种模式算出来的（跟 Go 侧
        # internal/execution/risk.go 的 Position.StopLossPrice 是同一个思路）。
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
    """在历史数据上模拟执行决策流。

    成交假设（刻意保守，宁可低估也不要高估）：

    * 信号在第 i 根 K 线收盘时产生，成交发生在第 i+1 根 K 线的开盘价上。
      在同一根 K 线的收盘价上成交等于用当时还不知道的价格下单，
      这是回测里最常见也最致命的前视偏差。
    * 成交价再按滑点调整，方向永远对交易者不利。
    * 手续费按吃单费率计，开仓平仓各收一次。
    """
    fee_model = fee_model or FeeModel()
    if not candles:
        raise BacktestError("没有 K 线数据")
    if not decisions:
        raise BacktestError("没有决策数据")
    if len(decisions) != len(candles):
        raise BacktestError(
            f"决策数 {len(decisions)} 与 K 线数 {len(candles)} 不一致，"
            "两者必须逐根对应"
        )
    if initial_capital <= 0:
        raise BacktestError("初始资金必须为正")
    if risk.max_position_size_quote <= 0:
        raise BacktestError("单笔最大仓位必须为正")

    split_index = _split_index(len(candles), split_ratio)
    split_at = candles[split_index].close_time

    equity = initial_capital
    position: _Position | None = None
    trades: list[Trade] = []
    equity_curve: list[tuple[datetime, Decimal]] = [(candles[0].close_time, equity)]
    # 单日累计已实现亏损，用于风控暂停。
    day_key: str | None = None
    day_loss = Decimal(0)
    halted_days: set[str] = set()

    for i, candle in enumerate(candles):
        decision = decisions[i]
        today = candle.close_time.strftime("%Y-%m-%d")
        if today != day_key:
            day_key, day_loss = today, Decimal(0)

        # ---- 1. 先处理已有持仓的退出条件（止损/止盈/超时/反向信号）----
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

        # ---- 2. 单日亏损风控：达到上限后当天不再开新仓 ----
        if (
            risk.max_daily_loss_quote > 0
            and day_loss >= risk.max_daily_loss_quote
            and today not in halted_days
        ):
            halted_days.add(today)

        # ---- 3. 处理开仓 ----
        can_open = (
            position is None
            and decision.triggered
            and decision.direction is not Direction.NEUTRAL
            and today not in halted_days
            and i + 1 < len(candles)  # 需要下一根 K 线的开盘价来成交
        )
        if can_open:
            position = _open(
                decision, candles[i + 1], fee_model, risk, equity,
                _segment_of(i, split_index),
            )
            if position is not None:
                equity -= position.entry_fee

        # ---- 4. 记录权益（含浮动盈亏）----
        mark = equity
        if position is not None:
            mark = equity + position.unrealized(candle.close)
        equity_curve.append((candle.close_time, mark))

    # 数据结束时仍有持仓，按最后一根收盘价强制平仓。
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
    """返回样本内区间的最后一个下标。"""
    if not 0 < ratio < 1:
        raise BacktestError(f"样本内比例必须落在 (0, 1)，当前为 {ratio}")
    idx = int(n * ratio)
    # 两段都至少要有一根 K 线，否则"样本外验证"名不副实。
    return max(0, min(idx, n - 2))


def _segment_of(i: int, split_index: int) -> Segment:
    return Segment.IN_SAMPLE if i <= split_index else Segment.OUT_OF_SAMPLE


class _StopLevelUnavailable(Exception):
    """止损/止盈的绝对价格算不出来时抛出，_open() 据此拒绝开仓。"""


class _PositionSizeUnavailable(Exception):
    """按风险百分比无法算出仓位时抛出（比如止损距离为 0），_open() 据此不开仓。"""


def _resolve_position_size_quote(
    risk: RiskConfig, entry: Decimal, stop_loss_price: Decimal | None
) -> Decimal:
    """算出开仓的名义金额（计价货币）。跟 Go 侧 internal/execution/risk.go 的
    ResolvePositionSizeQuote 是同一份公式，两边必须一字不差地一致——否则回测跟
    实盘算的不是同一个策略。

    这里刻意不对结果做 max_position_size_quote 上限的裁剪——上限检查交给调用方
    (_open) 做，超限就整笔拒绝，不做静默缩小，理由跟 Go 侧完全一样。
    """
    mode = risk.position_sizing_mode or "fixed_quote"
    if mode == "fixed_quote":
        return risk.max_position_size_quote
    if mode == "risk_pct":
        if not stop_loss_price or stop_loss_price <= 0:
            raise _PositionSizeUnavailable("按风险百分比开仓需要先有可用的止损价，但本次止损未设置或算不出来")
        stop_distance = abs(entry - stop_loss_price)
        if stop_distance <= 0:
            raise _PositionSizeUnavailable("止损价与入场价相同，止损距离为 0，无法据此计算仓位")
        stop_distance_pct = stop_distance / entry
        risk_amount = risk.account_equity_quote * Decimal(str(risk.risk_per_trade_pct))
        return risk_amount / stop_distance_pct
    raise _PositionSizeUnavailable(f"不支持的仓位模式 {mode!r}")


def _resolve_level_price(signals: list[dict[str, Any]], want_support: bool, purpose: str) -> Decimal:
    """从决策的信号里取 support_resistance 模块检测到的最近支撑/阻力位。

    跟 Go 侧 internal/execution/risk.go 的 levelPrice 是同一份逻辑：signals 是
    JSONL 反序列化出的原始字典，形状跟 Go 的 types.Signal 一致
    （module/raw，raw 里是 nearest_support/nearest_resistance）。
    """
    sig = next((s for s in signals if s.get("module") == "support_resistance"), None)
    if sig is None:
        raise _StopLevelUnavailable(f"{purpose} 需要 support_resistance 模块的数据，但本次决策的信号里没有")
    if sig.get("degraded"):
        raise _StopLevelUnavailable(f"{purpose} 需要 support_resistance 模块的数据，但该模块本次是降级信号")

    key, label = ("nearest_support", "支撑位") if want_support else ("nearest_resistance", "阻力位")
    level = (sig.get("raw") or {}).get(key)
    if not level:
        raise _StopLevelUnavailable(f"{purpose}：当前价格附近没有检测到{label}，无法据此设置{purpose}")
    price_str = level.get("price")
    if not price_str:
        raise _StopLevelUnavailable(f"{purpose}：{label}数据里缺少 price 字段")
    try:
        return Decimal(str(price_str))
    except Exception as exc:  # noqa: BLE001 - 转成统一的领域异常，调用方不用关心具体原因
        raise _StopLevelUnavailable(f"{purpose}：解析{label}价格 {price_str!r} 失败：{exc}") from exc


def _resolve_poc_price(signals: list[dict[str, Any]], purpose: str) -> Decimal:
    """从决策的信号里取 poc 模块算出的成交量分布重心。POC 只有一个价格，不区分多空、
    不区分止损止盈方向，跟 Go 侧 internal/execution/risk.go 的 pocPrice 是同一份逻辑。
    """
    sig = next((s for s in signals if s.get("module") == "poc"), None)
    if sig is None:
        raise _StopLevelUnavailable(f"{purpose} 需要 poc 模块的数据，但本次决策的信号里没有")
    if sig.get("degraded"):
        raise _StopLevelUnavailable(f"{purpose} 需要 poc 模块的数据，但该模块本次是降级信号")
    price_str = (sig.get("raw") or {}).get("poc_price")
    if not price_str:
        raise _StopLevelUnavailable(f"{purpose}：poc 模块的信号里没有算出有效的 poc_price，无法据此设置{purpose}")
    try:
        return Decimal(str(price_str))
    except Exception as exc:  # noqa: BLE001
        raise _StopLevelUnavailable(f"{purpose}：解析 POC 价格 {price_str!r} 失败：{exc}") from exc


def _resolve_stop_loss_price(
    risk: RiskConfig, direction: Direction, entry: Decimal, signals: list[dict[str, Any]]
) -> Decimal | None:
    """开仓时算出止损的绝对价格，None 表示未设置止损（pct 为 0）。"""
    mode = risk.stop_loss_mode or "pct"
    if mode == "pct":
        if risk.stop_loss_pct <= 0:
            return None
        pct = Decimal(str(risk.stop_loss_pct))
        if direction is Direction.LONG:
            return entry * (Decimal(1) - pct)
        return entry * (Decimal(1) + pct)
    if mode == "support_resistance":
        # 多头止损设在支撑位，空头止损设在阻力位。
        return _resolve_level_price(signals, direction is Direction.LONG, "止损")
    if mode == "poc":
        return _resolve_poc_price(signals, "止损")
    raise _StopLevelUnavailable(f"不支持的止损模式 {mode!r}")


def _resolve_take_profit_price(
    risk: RiskConfig, direction: Direction, entry: Decimal, signals: list[dict[str, Any]]
) -> Decimal | None:
    """开仓时算出止盈的绝对价格，None 表示未设置止盈（pct 为 0）。"""
    mode = risk.take_profit_mode or "pct"
    if mode == "pct":
        if risk.take_profit_pct <= 0:
            return None
        pct = Decimal(str(risk.take_profit_pct))
        if direction is Direction.LONG:
            return entry * (Decimal(1) + pct)
        return entry * (Decimal(1) - pct)
    if mode == "support_resistance":
        # 多头止盈设在阻力位，空头止盈设在支撑位——跟止损方向相反。
        return _resolve_level_price(signals, direction is Direction.SHORT, "止盈")
    if mode == "poc":
        return _resolve_poc_price(signals, "止盈")
    raise _StopLevelUnavailable(f"不支持的止盈模式 {mode!r}")


def _open(
    decision: Decision,
    next_candle: Candle,
    fee_model: FeeModel,
    risk: RiskConfig,
    equity: Decimal,
    segment: Segment,
) -> _Position | None:
    """在下一根 K 线开盘价建仓。"""
    fill = fee_model.fill_price(next_candle.open, decision.direction)
    if fill <= 0:
        return None

    # 止损算不出来（比如配了 support_resistance 模式但附近没探测到关键位）就不该
    # 开仓——用户明确要求了止损保护，没有保护地开仓等于没忠实执行他的规则。
    #
    # 这一步必须在算仓位之前：risk_pct 仓位模式需要止损距离才能算出仓位大小，
    # fixed_quote 模式虽然不需要，但统一顺序，不为两种模式分别维护一套流程
    # （跟 Go 侧 Worker.openPosition 的顺序对齐）。
    try:
        stop_loss_price = _resolve_stop_loss_price(risk, decision.direction, fill, decision.signals)
    except _StopLevelUnavailable:
        return None

    try:
        risk_notional = _resolve_position_size_quote(risk, fill, stop_loss_price)
    except _PositionSizeUnavailable:
        return None

    # 硬上限用拒绝而不是裁剪——静默缩小仓位会破坏"这笔仓位对应 N% 权益风险"这个
    # 用户明确要的语义，跟 Go 侧 RiskManager.CheckOpen 的 max_position_size 拒绝
    # 语义一致。
    if risk.max_position_size_quote > 0 and risk_notional > risk.max_position_size_quote:
        return None

    # 仓位再取一次"当前权益"的较小值：权益缩水后仓位要跟着缩，否则回测会假设一个
    # 永远打不完的钱包。这是回测特有的模拟行为，早于本次改动就存在，Go 实盘没有
    # 等价物（没有任何账户余额概念）——两种仓位模式下都统一保留，不去动它，也不让
    # 它跟 risk_pct 模式的 account_equity_quote 混为一谈：那是用户声明的静态数字，
    # equity 是回测过程里滚动的模拟权益，概念上是两码事。
    notional = min(risk_notional, equity)
    if notional <= 0:
        return None

    quantity = notional / fill
    if quantity <= 0:
        return None

    # 止盈算不出来不拒绝开仓，只是这一笔没有止盈线——止盈不是安全机制，跟止损不对称：
    # 突破型入场恰恰是最常见的"附近没有阻力位可当止盈目标"的情形。跟 Go 侧
    # Worker.openPosition 是同一个决定，两边必须一致，否则回测跟实盘算的不是同一个策略。
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
    """判断是否应当平仓，返回平仓原因；不平仓返回 None。

    优先级：止损 > 止盈 > 超时 > 反向信号。
    止损排在止盈之前是保守取向：当一根 K 线同时触及止损与止盈时，
    我们无从知道哪个先到，假设成不利的那个。

    止损/止盈阈值是开仓那一刻就算好存在 position 上的绝对价格（_resolve_stop_loss_price/
    _resolve_take_profit_price，见 _open），不管当初是固定百分比还是 support_resistance
    模式，这里只需要拿 K 线的最高/最低价跟阈值比，两种模式共用同一段判断逻辑。
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
    """平仓并返回 (交易记录, 本次实现的净盈亏)。

    净盈亏已扣除开仓与平仓两笔手续费；开仓费在建仓时已从权益中扣过，
    所以返回值里只再扣一次平仓费，避免重复计算。
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
    # 开仓费已在建仓时扣过，这里返回 gross - exit_fee。
    return trade, gross - exit_fee


def _exit_price(
    position: _Position,
    candle: Candle,
    reason: str,
    fee_model: FeeModel,
) -> Decimal:
    """按平仓原因确定成交价。

    止损 / 止盈按触发价（开仓时算好的绝对价格，见 _open）成交，其余按收盘价。
    三者都要再过一次滑点：真实的止损单在剧烈行情里往往成交得比触发价更差。
    """
    if reason == "stop_loss" and position.stop_loss_price is not None:
        raw = position.stop_loss_price
    elif reason == "take_profit" and position.take_profit_price is not None:
        raw = position.take_profit_price
    else:
        raw = candle.close

    # 平仓方向与持仓方向相反，滑点因此也朝相反方向作用。
    return fee_model.fill_price(raw, position.direction.opposite())


def decisions_from_iter(raw: Iterable[dict[str, Any]]) -> list[Decision]:
    """把 JSONL 解析出的字典流转成 Decision 列表。"""
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
    """解析 Go 侧输出的 RFC3339 时间。"""
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
