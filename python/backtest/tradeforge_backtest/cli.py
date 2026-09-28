"""CLI entry point: run a decision stream against candles to produce a backtest result.

Usage::

    python -m tradeforge_backtest.cli \\
        --strategy strategy.json --candles btc.csv --decisions decisions.jsonl
"""

from __future__ import annotations

import argparse
import json
import sys
from decimal import Decimal
from pathlib import Path

from .engine import DEFAULT_INITIAL_CAPITAL, DEFAULT_SPLIT_RATIO, decisions_from_iter, run_backtest
from .loaders import (
    fee_model_from_dict,
    load_candles,
    load_decisions_jsonl,
    load_strategy,
    risk_from_strategy,
)


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="tradeforge-backtest",
        description="Backtest a strategy configuration against historical data (with fee and slippage modeling)",
    )
    p.add_argument("--strategy", required=True, help="Path to strategy config JSON")
    p.add_argument("--candles", required=True, help="Path to historical candle CSV")
    p.add_argument(
        "--decisions", required=True,
        help="Path to the decision JSONL emitted by backtest-runner",
    )
    p.add_argument(
        "--capital", default=str(DEFAULT_INITIAL_CAPITAL),
        help=f"Initial capital, default {DEFAULT_INITIAL_CAPITAL}",
    )
    p.add_argument(
        "--split", type=float, default=DEFAULT_SPLIT_RATIO,
        help=f"In-sample fraction, default {DEFAULT_SPLIT_RATIO} (remainder is out-of-sample)",
    )
    p.add_argument("--taker-fee", default=None, help="Taker fee rate, default 0.0004")
    p.add_argument("--slippage-bps", default=None, help="Slippage (basis points), default 5")
    p.add_argument("--no-save", action="store_true", help="Only print the result, don't write to the database")
    p.add_argument("--json-out", default=None, help="Save the full result to a separate JSON file")
    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)

    strategy = load_strategy(args.strategy)
    timeframe = strategy.get("timeframe", "1h")
    candles = load_candles(args.candles, timeframe=timeframe)
    rows, meta = load_decisions_jsonl(args.decisions)

    # The decision stream must come from the same market data, or every metric will be wrong.
    if meta.get("symbol") and meta["symbol"] != strategy.get("symbol"):
        print(
            f"Error: decision stream symbol {meta['symbol']} does not match strategy "
            f"config symbol {strategy.get('symbol')}",
            file=sys.stderr,
        )
        return 2
    if len(rows) != len(candles):
        print(
            f"Error: decision count {len(rows)} does not match candle count {len(candles)}; "
            "make sure both were generated from the same market data",
            file=sys.stderr,
        )
        return 2

    fee_overrides = {}
    if args.taker_fee is not None:
        fee_overrides["taker_fee_rate"] = args.taker_fee
    if args.slippage_bps is not None:
        fee_overrides["slippage_bps"] = args.slippage_bps

    result = run_backtest(
        strategy_id=strategy.get("id") or meta.get("strategy_id", ""),
        symbol=strategy.get("symbol", meta.get("symbol", "")),
        candles=candles,
        decisions=decisions_from_iter(rows),
        risk=risk_from_strategy(strategy),
        fee_model=fee_model_from_dict(fee_overrides),
        initial_capital=Decimal(args.capital),
        split_ratio=args.split,
        engine_version=meta.get("engine_version", "unknown"),
    )

    print(result.summary())

    if not result.parameters_validated_out_of_sample:
        print(
            "\nNote: no trades were produced in the out-of-sample segment; "
            "this backtest did not meaningfully validate the strategy.",
            file=sys.stderr,
        )

    if args.json_out:
        Path(args.json_out).write_text(
            json.dumps(_result_to_json(result), ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        print(f"\nFull result written to {args.json_out}")

    if not args.no_save:
        from .store import save_result

        try:
            rid = save_result(result)
            print(f"\nBacktest result saved, record ID: {rid}")
        except Exception as exc:  # noqa: BLE001 — a save failure shouldn't swallow the already-computed result
            print(f"\nFailed to save to database (result was already printed above): {exc}", file=sys.stderr)
            return 1

    return 0


def _result_to_json(result) -> dict:  # noqa: ANN001
    from .store import result_to_row

    row = result_to_row(result)
    return {
        k: (v.isoformat() if hasattr(v, "isoformat") else v)
        for k, v in row.items()
    }


if __name__ == "__main__":
    raise SystemExit(main())
