"""把回测结果写入 Postgres，供状态机与界面消费。"""

from __future__ import annotations

import json
import os
import uuid
from datetime import datetime
from typing import Any

from .model import BacktestResult, Trade


def _iso(dt: datetime) -> str:
    """把引擎内部用的裸时间（没有 tzinfo，按约定就是 UTC）序列化成带时区后缀的
    RFC3339 字符串。

    这个项目里所有时间戳全程用 naive datetime（Go 侧输出的 RFC3339 "...Z" 由
    engine.py 的 _parse_time() 特意去掉 tzinfo 后再存），naive datetime 的
    .isoformat() 从不带时区后缀（如 "2026-09-09T11:00:00"），而 Go 的
    time.Time.UnmarshalJSON 要求 RFC3339 必须带时区——这份数据一旦嵌进 JSONB
    字段（segments/trades/equity_curve）被 Go 那边反序列化，就会直接报错。
    data_start/data_end 这类直接绑定到 TIMESTAMPTZ 列的时间戳不受影响（psycopg
    原生处理），只有嵌在 JSON 字符串里的时间戳需要这一步。
    """
    return dt.isoformat() + "Z"


def dsn_from_env() -> str:
    """按环境变量拼出连接串，默认值对应 docker-compose 起的本地服务。"""
    host = os.getenv("TF_PG_HOST", "localhost")
    port = os.getenv("TF_PG_PORT", "55432")
    user = os.getenv("TF_PG_USER", "tradeforge")
    password = os.getenv("TF_PG_PASSWORD", "tradeforge")
    database = os.getenv("TF_PG_DATABASE", "tradeforge")
    return f"postgresql://{user}:{password}@{host}:{port}/{database}"


def result_to_row(result: BacktestResult) -> dict[str, Any]:
    """把结果对象转成与 backtest_results 表对应的字段字典。

    单独抽出来是为了让"序列化"能被独立测试，不必真的连数据库。
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
        # 只保留触发时各模块的关键信息，避免整段 raw 把审计表撑爆。
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
    """把回测结果写库，返回记录 ID。

    psycopg 是可选依赖：没装时给出明确提示，而不是让 ImportError
    在调用栈深处炸出来。
    """
    try:
        import psycopg  # type: ignore[import-not-found]
    except ImportError as exc:  # pragma: no cover — 取决于环境
        raise RuntimeError(
            "写库需要 psycopg：pip install 'psycopg[binary]'。"
            "只想看结果可以加 --no-save 跳过。"
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
