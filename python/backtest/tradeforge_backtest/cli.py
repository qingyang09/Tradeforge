"""命令行入口：把决策流与 K 线跑成一份回测结果。

用法::

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
        description="在历史数据上回测一个策略配置（含手续费与滑点建模）",
    )
    p.add_argument("--strategy", required=True, help="策略配置 JSON 路径")
    p.add_argument("--candles", required=True, help="历史 K 线 CSV 路径")
    p.add_argument(
        "--decisions", required=True,
        help="backtest-runner 输出的决策 JSONL 路径",
    )
    p.add_argument(
        "--capital", default=str(DEFAULT_INITIAL_CAPITAL),
        help=f"初始资金，默认 {DEFAULT_INITIAL_CAPITAL}",
    )
    p.add_argument(
        "--split", type=float, default=DEFAULT_SPLIT_RATIO,
        help=f"样本内占比，默认 {DEFAULT_SPLIT_RATIO}（其余为样本外）",
    )
    p.add_argument("--taker-fee", default=None, help="吃单费率，默认 0.0004")
    p.add_argument("--slippage-bps", default=None, help="滑点（基点），默认 5")
    p.add_argument("--no-save", action="store_true", help="只打印结果，不写数据库")
    p.add_argument("--json-out", default=None, help="把完整结果另存为 JSON 文件")
    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)

    strategy = load_strategy(args.strategy)
    timeframe = strategy.get("timeframe", "1h")
    candles = load_candles(args.candles, timeframe=timeframe)
    rows, meta = load_decisions_jsonl(args.decisions)

    # 决策流必须来自同一份行情，否则指标全是错的。
    if meta.get("symbol") and meta["symbol"] != strategy.get("symbol"):
        print(
            f"错误：决策流的标的 {meta['symbol']} 与策略配置的 "
            f"{strategy.get('symbol')} 不一致",
            file=sys.stderr,
        )
        return 2
    if len(rows) != len(candles):
        print(
            f"错误：决策数 {len(rows)} 与 K 线数 {len(candles)} 不一致；"
            "请确认两者由同一份行情生成",
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
            "\n注意：样本外区间没有产生交易，本次回测未对该策略形成有效验证。",
            file=sys.stderr,
        )

    if args.json_out:
        Path(args.json_out).write_text(
            json.dumps(_result_to_json(result), ensure_ascii=False, indent=2),
            encoding="utf-8",
        )
        print(f"\n完整结果已写入 {args.json_out}")

    if not args.no_save:
        from .store import save_result

        try:
            rid = save_result(result)
            print(f"\n回测结果已落库，记录 ID：{rid}")
        except Exception as exc:  # noqa: BLE001 — 落库失败不应吞掉已算出的结果
            print(f"\n写库失败（结果已在上方打印）：{exc}", file=sys.stderr)
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
