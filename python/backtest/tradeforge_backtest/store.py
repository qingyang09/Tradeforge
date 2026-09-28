"""Write backtest results to Postgres, for the state machine and UI to consume."""

from __future__ import annotations

import json
import os
import uuid
from datetime import datetime
from typing import Any

from .model import BacktestResult, Trade


def _iso(dt: datetime) -> str:
    """Serialize the engine's internal naive datetime (no tzinfo, UTC by
    convention) into an RFC3339 string with an explicit timezone suffix.

    Every timestamp in this project is a naive datetime throughout (the
    RFC3339 "...Z" Go emits gets its tzinfo deliberately stripped by
    engine.py's _parse_time() before being stored). A naive datetime's
    .isoformat() never carries a timezone suffix (e.g.
    "2026-09-09T11:00:00"), but Go's time.Time.UnmarshalJSON requires
    RFC3339 to include one — so the moment this data is embedded in a JSONB
    field (segments/trades/equity_curve) and deserialized on the Go side, it
    fails outright. Timestamps bound directly to TIMESTAMPTZ columns like
    data_start/data_end aren't affected (psycopg handles those natively) —
    only timestamps embedded in JSON strings need this step.
    """
    return dt.isoformat() + "Z"


def dsn_from_env() -> str:
    """Build the connection string from environment variables; defaults match the local docker-compose services."""
    host = os.getenv("TF_PG_HOST", "localhost")
    port = os.getenv("TF_PG_PORT", "55432")
    user = os.getenv("TF_PG_USER", "tradeforge")
    password = os.getenv("TF_PG_PASSWORD", "tradeforge")
    database = os.getenv("TF_PG_DATABASE", "tradeforge")
    return f"postgresql://{user}:{password}@{host}:{port}/{database}"


def result_to_row(result: BacktestResult) -> dict[str, Any]:
    """Convert a result object into the field dict matching the backtest_results table.

    Kept as a separate function so "serialization" can be tested on its own,
    without an actual database connection.
    """
    return {
        "id": str(uuid.uuid4()),
        "strategy_id": result.strategy_id,
        "symbol": result.symbol,
        "overall": json.dumps(result.overall.to_dict()),
        "in_sample": json.dumps(result.in_sample.to_dict()),
        "out_of_sample": json.dumps(result.out_of_sample.to_dict()),
        "segments": json.dumps(
            [
                {
                    "label": "in_sample",
                    "start": _iso(result.data_start),
                    "end": _iso(result.split_at),
                },
                {
                    "label": "out_of_sample",
                    "start": _iso(result.split_at),
                    "end": _iso(result.data_end),
                },
            ]
        ),
        "fee_model": json.dumps(
            {
                "maker_fee_rate": float(result.fee_model.maker_fee_rate),
                "taker_fee_rate": float(result.fee_model.taker_fee_rate),
                "slippage_bps": float(result.fee_model.slippage_bps),
            }
        ),
        "trades": json.dumps([_trade_to_dict(t) for t in result.trades]),
        "equity_curve": json.dumps(
            [{"time": _iso(t), "equity": str(e)} for t, e in result.equity_curve]
        ),
        "initial_capital": str(result.initial_capital),
        "data_start": result.data_start,
        "data_end": result.data_end,
        "engine_version": result.engine_version,
    }


def _trade_to_dict(t: Trade) -> dict[str, Any]:
    return {
        "entry_time": _iso(t.entry_time),
        "exit_time": _iso(t.exit_time),
        "direction": t.direction.value,
        "entry_price": str(t.entry_price),
        "exit_price": str(t.exit_price),
        "quantity": str(t.quantity),
        "pnl": str(t.pnl),
        "fees": str(t.fees),
        "exit_reason": t.exit_reason,
        "segment": t.segment.value,
        # Keep only each module's key fields at trigger time — including the
        # full raw payload would bloat the audit table.
        "trigger_signals": [
            {
                "module": s.get("module"),
                "direction": s.get("direction"),
                "confidence": s.get("confidence"),
                "reason": s.get("reason"),
            }
            for s in t.trigger_signals
        ],
    }


def save_result(result: BacktestResult, dsn: str | None = None) -> str:
    """Write the backtest result to the database, returning the record ID.

    psycopg is an optional dependency: when it's not installed, raise a
    clear message instead of letting an ImportError surface from deep in the
    call stack.
    """
    try:
        import psycopg  # type: ignore[import-not-found]
    except ImportError as exc:  # pragma: no cover -- depends on the environment
        raise RuntimeError(
            "Writing results requires psycopg: pip install 'psycopg[binary]'. "
            "Pass --no-save if you just want to see the results."
        ) from exc

    row = result_to_row(result)
    sql = """
        INSERT INTO backtest_results (
            id, strategy_id, symbol, overall, in_sample, out_of_sample,
            segments, fee_model, trades, equity_curve, initial_capital,
            data_start, data_end, engine_version
        ) VALUES (
            %(id)s, %(strategy_id)s, %(symbol)s, %(overall)s, %(in_sample)s,
            %(out_of_sample)s, %(segments)s, %(fee_model)s, %(trades)s, %(equity_curve)s,
            %(initial_capital)s, %(data_start)s, %(data_end)s, %(engine_version)s
        )
    """
    with psycopg.connect(dsn or dsn_from_env()) as conn:
        with conn.cursor() as cur:
            cur.execute(sql, row)
        conn.commit()
    return row["id"]
