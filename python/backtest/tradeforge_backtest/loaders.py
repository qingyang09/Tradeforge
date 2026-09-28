"""Load backtest inputs from disk: candle CSV, decision JSONL, strategy config JSON."""

from __future__ import annotations

import csv
import json
from datetime import datetime, timedelta
from decimal import Decimal
from pathlib import Path
from typing import Any

from .model import Candle, FeeModel, RiskConfig

# Column names kept in sync with internal/marketdata/csv.go on the Go side.
_REQUIRED_COLUMNS = ("open_time", "open", "high", "low", "close", "volume")

_TIMEFRAME_SECONDS = {
    "1m": 60, "5m": 300, "15m": 900, "1h": 3600, "4h": 14400, "1d": 86400,
}


def load_candles(path: str | Path, timeframe: str = "1h") -> list[Candle]:
    """Read candles from a CSV file.

    Numbers are always constructed with ``Decimal(str)``, never ``Decimal(float)``: the
    latter turns "0.1" from the CSV into 0.1000000000000000055511151231257827, and
    across thousands of candles that's enough drift to change the backtest's
    conclusions.
    """
    path = Path(path)
    with path.open(newline="", encoding="utf-8") as f:
        reader = csv.DictReader(f)
        if reader.fieldnames is None:
            raise ValueError(f"{path} has no header row")
        missing = [c for c in _REQUIRED_COLUMNS if c not in reader.fieldnames]
        if missing:
            raise ValueError(f"{path} is missing required columns: {missing}")

        span = timedelta(seconds=_TIMEFRAME_SECONDS.get(timeframe, 3600))
        candles: list[Candle] = []
        for lineno, row in enumerate(reader, start=2):
            try:
                open_time = _parse_time(row["open_time"])
                close_raw = (row.get("close_time") or "").strip()
                close_time = _parse_time(close_raw) if close_raw else open_time + span
                candles.append(
                    Candle(
                        open_time=open_time,
                        close_time=close_time,
                        open=Decimal(row["open"].strip()),
                        high=Decimal(row["high"].strip()),
                        low=Decimal(row["low"].strip()),
                        close=Decimal(row["close"].strip()),
                        volume=Decimal(row["volume"].strip()),
                    )
                )
            except Exception as exc:  # noqa: BLE001 — re-raised with the line number attached
                raise ValueError(f"{path} failed to parse line {lineno}: {exc}") from exc

    if not candles:
        raise ValueError(f"{path} contains no candles")
    candles.sort(key=lambda c: c.open_time)
    return candles


def load_decisions_jsonl(path: str | Path) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    """Read the JSONL emitted by backtest-runner.

    Returns (list of decision rows, header metadata).
    """
    path = Path(path)
    meta: dict[str, Any] = {}
    rows: list[dict[str, Any]] = []

    with path.open(encoding="utf-8") as f:
        for lineno, line in enumerate(f, start=1):
            line = line.strip()
            if not line:
                continue
            try:
                obj = json.loads(line)
            except json.JSONDecodeError as exc:
                raise ValueError(f"{path} line {lineno} is not valid JSON: {exc}") from exc
            if obj.get("type") == "meta":
                meta = obj
            elif obj.get("type") == "decision":
                rows.append(obj)

    if not rows:
        raise ValueError(f"{path} contains no decision records")
    return rows, meta


def load_strategy(path: str | Path) -> dict[str, Any]:
    """Read the strategy config JSON."""
    return json.loads(Path(path).read_text(encoding="utf-8"))


def risk_from_strategy(cfg: dict[str, Any]) -> RiskConfig:
    """Extract risk-control parameters from a strategy config."""
    risk = cfg.get("risk") or {}
    return RiskConfig(
        max_position_size_quote=_dec(risk.get("max_position_size_quote"), "1000"),
        max_daily_loss_quote=_dec(risk.get("max_daily_loss_quote"), "0"),
        stop_loss_mode=risk.get("stop_loss_mode") or "pct",
        stop_loss_pct=float(risk.get("stop_loss_pct") or 0.0),
        take_profit_mode=risk.get("take_profit_mode") or "pct",
        take_profit_pct=float(risk.get("take_profit_pct") or 0.0),
        max_holding_period_secs=_duration_secs(risk.get("max_holding_period")),
        position_sizing_mode=risk.get("position_sizing_mode") or "fixed_quote",
        account_equity_quote=_dec(risk.get("account_equity_quote"), "0"),
        risk_per_trade_pct=float(risk.get("risk_per_trade_pct") or 0.0),
    )


def fee_model_from_dict(data: dict[str, Any] | None) -> FeeModel:
    """Build a fee model from a dict, falling back to conservative defaults for missing fields."""
    data = data or {}
    return FeeModel(
        maker_fee_rate=_dec(data.get("maker_fee_rate"), "0.0002"),
        taker_fee_rate=_dec(data.get("taker_fee_rate"), "0.0004"),
        slippage_bps=_dec(data.get("slippage_bps"), "5"),
    )


def _dec(value: Any, default: str) -> Decimal:
    if value is None or value == "":
        return Decimal(default)
    return Decimal(str(value))


def _duration_secs(value: Any) -> float:
    """Parse a Go-style duration string ("4h", "90m", "1h30m")."""
    if not value:
        return 0.0
    if isinstance(value, (int, float)):
        # Integer nanoseconds form (how time.Duration serializes).
        return float(value) / 1e9

    text = str(value).strip()
    units = {"ms": 0.001, "s": 1.0, "m": 60.0, "h": 3600.0}
    total = 0.0
    number = ""
    unit = ""
    for ch in text:
        if ch.isdigit() or ch == ".":
            if unit:
                total += _apply_unit(number, unit, units)
                number, unit = "", ""
            number += ch
        else:
            unit += ch
    if number:
        total += _apply_unit(number, unit or "s", units)
    return total


def _apply_unit(number: str, unit: str, units: dict[str, float]) -> float:
    if unit not in units:
        raise ValueError(f"unrecognized duration unit {unit!r}")
    return float(number) * units[unit]


def _parse_time(s: str) -> datetime:
    """Parse an RFC3339 string or Unix timestamp, always returning naive UTC."""
    s = s.strip()
    try:
        dt = datetime.fromisoformat(s.replace("Z", "+00:00"))
        return dt.replace(tzinfo=None) if dt.tzinfo else dt
    except ValueError:
        pass
    n = int(s)
    # Exchange candle APIs generally use milliseconds.
    return datetime.utcfromtimestamp(n / 1000 if n > 1e11 else n)
