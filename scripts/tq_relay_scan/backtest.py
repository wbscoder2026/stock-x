"""回测入口：趋势中继箱体突破的 walk-forward 统计（TqSdk 只负责拉齐数据）。

    python3 scripts/tq_relay_scan/backtest.py --period 15 --from 2024-01-01 --to 2026-09-01 --out bt.csv
    python3 scripts/tq_relay_scan/backtest.py --period 60 --hold-bars 6 --only JM,RB

实现约束（与方案一致）:

* 判定全部走纯函数 ``box.detect_box`` / ``trade.run_symbol``：第 i 根只用 ``<= i`` 的数据
* 主连换月：``|open - prev_close| > 3×ATR`` 的根不开仓，持仓按前收平（``reason=roll``）
* 逐个品种串行，单品种缺数据/出错只记数跳过，不中断全市场
"""

from __future__ import annotations

import argparse
import csv
import os
import sys
import time
from dataclasses import dataclass, field, replace
from datetime import date, timedelta
from typing import Dict, List, Optional, Sequence, Tuple

import numpy as np
import pandas as pd

import box as box_mod
import trend as trend_mod
import universe
from box import BoxParams
from trade import (
    SUMMARY_COLUMNS,
    TRADE_COLUMNS,
    Trade,
    TradeParams,
    run_symbol,
    summarize,
    trade_rows,
)

PERIOD_CHOICES = ("5", "15", "30", "60", "120", "1d")

#: 回测里多等几根才判定箱体失效（与 trade 默认一致）
DEFAULT_MAX_EXTRA_BARS = 10

SUBSCRIBE_BATCH = 20
SUBSCRIBE_WAIT_SECONDS = 30


def parse_args(argv: Optional[Sequence[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="中继箱体突破回测（TqSdk）")
    parser.add_argument("--period", default="15", choices=PERIOD_CHOICES)
    parser.add_argument("--from", dest="from_", required=True, help="起始日期 YYYY-MM-DD")
    parser.add_argument("--to", required=True, help="结束日期 YYYY-MM-DD")
    parser.add_argument("--only", default=None, help="按品种码过滤，如 JM,RB")
    parser.add_argument("--out", default=None, help="明细 CSV 路径")
    parser.add_argument("--summary-out", default=None, help="汇总 CSV 路径")
    parser.add_argument("--trades", action="store_true", help="控制台打印明细")
    parser.add_argument("--n-bars", type=int, default=None, help="周期 K 线订阅根数（默认查表）")
    parser.add_argument("--hold-bars", type=int, default=8, help="固定持有根数，0 = 只靠止损止盈")
    parser.add_argument("--stop-atr", type=float, default=1.0, help="止损 = 入场根 ATR 的几倍")
    parser.add_argument("--tp-atr", type=float, default=2.0, help="止盈 = 入场根 ATR 的几倍")
    parser.add_argument("--rr", type=float, default=None, help="止盈/止损盈亏比，给定则 tp = rr × stop（覆盖 --tp-atr）")
    parser.add_argument("--sweep-rr", default=None, help="逗号分隔的盈亏比列表，逐个跑并输出对比表，如 1,1.5,2,3")
    parser.add_argument("--fee-bps", type=float, default=0.0)
    parser.add_argument("--strict", action="store_true", help="趋势加强：近 20 根不破 EMA20")
    parser.add_argument("--atr-period", type=int, default=box_mod.ATR_PERIOD)
    parser.add_argument("--atr-lo", type=float, default=box_mod.ATR_LO)
    parser.add_argument("--atr-hi", type=float, default=box_mod.ATR_HI)
    parser.add_argument("--impulse-n", type=int, default=box_mod.IMPULSE_N)
    parser.add_argument("--impulse-min-atr", type=float, default=box_mod.IMPULSE_MIN_ATR)
    parser.add_argument("--w-min", type=int, default=box_mod.W_MIN)
    parser.add_argument("--w-max", type=int, default=box_mod.W_MAX)
    parser.add_argument("--break-buf-atr", type=float, default=box_mod.BREAK_BUF_ATR)
    parser.add_argument("--retracement-max", type=float, default=box_mod.RETRACEMENT_MAX)
    return parser.parse_args(argv)


def parse_ratios(text: Optional[str]) -> List[float]:
    """``"1,1.5,2"`` → ``[1.0, 1.5, 2.0]``；空/非法/非正数直接抛 ``ValueError``。"""
    ratios: List[float] = []
    for token in str(text or "").split(","):
        token = token.strip()
        if not token:
            continue
        try:
            value = float(token)
        except ValueError:
            raise ValueError("非法盈亏比 %s" % token) from None
        if value <= 0:
            raise ValueError("盈亏比必须 > 0：%s" % token)
        ratios.append(value)
    if not ratios:
        raise ValueError("空盈亏比列表")
    return ratios


def with_rr(params: TradeParams, rr: float) -> TradeParams:
    """按盈亏比派生止盈：``tp = rr × stop``。"""
    if rr <= 0:
        raise ValueError("盈亏比必须 > 0：%s" % rr)
    if params.stop_atr <= 0:
        raise ValueError("盈亏比模式需要 --stop-atr > 0")
    return replace(params, tp_atr=rr * params.stop_atr)


def build_params(args: argparse.Namespace) -> Tuple[TradeParams, BoxParams]:
    """命令行 → (交易参数, 箱体参数)。非法组合直接抛 ``ValueError``。"""
    box_params = BoxParams(
        atr_period=args.atr_period,
        atr_lo=args.atr_lo,
        atr_hi=args.atr_hi,
        impulse_n=args.impulse_n,
        impulse_min_atr=args.impulse_min_atr,
        w_min=args.w_min,
        w_max=args.w_max,
        break_buf_atr=args.break_buf_atr,
        retracement_max=args.retracement_max,
    )
    trade_params = TradeParams(
        hold_bars=args.hold_bars,
        stop_atr=args.stop_atr,
        tp_atr=args.tp_atr,
        fee_bps=args.fee_bps,
    )
    if args.rr is not None:  # --rr 覆盖 --tp-atr
        trade_params = with_rr(trade_params, args.rr)
    trade_params.validate()  # hold-bars=0 且止损止盈都关 → 报错
    if args.hold_bars < 0:
        raise ValueError("hold-bars 不能为负")
    if box_params.w_min < 2 or box_params.w_max < box_params.w_min:
        raise ValueError("w-min / w-max 非法")
    if args.sweep_rr:
        parse_ratios(args.sweep_rr)
    start = date.fromisoformat(args.from_)
    end = date.fromisoformat(args.to)
    if start >= end:
        raise ValueError("--from 必须早于 --to")
    universe.period_seconds(args.period)
    return trade_params, box_params


# ---------------------------------------------------------------- 趋势对齐


def _datetime_values(col: pd.Series) -> np.ndarray:
    if pd.api.types.is_datetime64_any_dtype(col):
        return col.to_numpy()
    if pd.api.types.is_integer_dtype(col):
        return pd.to_datetime(col.astype("int64"), unit="ns").to_numpy()
    return pd.to_datetime(col).to_numpy()


def align_trends(
    period_df: pd.DataFrame,
    daily_df: pd.DataFrame,
    *,
    strict: bool = False,
) -> List[Optional[str]]:
    """把「日线趋势」对齐到每根周期 K 线：用该根之前**已收盘**的日线。

    日线 ``datetime`` 在 TqSdk 里是该交易日的收盘时刻，因此按时间序
    ``searchsorted(daily, period, side="left") - 1`` 即「上一根已收盘日线」。
    """
    if period_df is None or len(period_df) == 0:
        return []
    if daily_df is None or len(daily_df) == 0:
        return [None] * len(period_df)

    daily_trend = trend_mod.trend_series(daily_df, strict=strict).to_numpy()
    daily_time = _datetime_values(daily_df["datetime"])
    period_time = _datetime_values(period_df["datetime"])
    positions = np.searchsorted(daily_time, period_time, side="left") - 1

    out: List[Optional[str]] = []
    for idx in positions:
        if idx < 0:
            out.append(None)
            continue
        value = daily_trend[idx]
        if value is None or (isinstance(value, float) and np.isnan(value)):
            out.append(None)
        else:
            out.append(str(value))
    return out


# ---------------------------------------------------------------- 逐品种 walk-forward


@dataclass
class FrameBacktestResult:
    trades: List[Trade] = field(default_factory=list)
    skip_against_trend: int = 0
    roll_gaps: int = 0
    skipped: List[Tuple[str, str]] = field(default_factory=list)
    bars: Dict[str, int] = field(default_factory=dict)


def run_frame_backtest(
    items: Sequence[Tuple[universe.Variety, pd.DataFrame, pd.DataFrame]],
    *,
    period: str,
    params: Optional[TradeParams] = None,
    box_params: Optional[BoxParams] = None,
    strict: bool = False,
) -> FrameBacktestResult:
    """对已拉齐的 ``(品种, 日线, 周期K线)`` 逐品种跑 walk-forward（纯函数，可测）。"""
    trade_params = params or TradeParams()
    trade_params.validate()
    box_params = box_params or BoxParams()

    result = FrameBacktestResult()
    for variety, daily_df, period_df in items:
        prefix = variety.prefix
        min_bars = box_params.w_min + box_params.impulse_n + 2
        if period_df is None or len(period_df) < min_bars:
            result.skipped.append((prefix, "short_history"))
            continue
        if daily_df is None or len(daily_df) < trend_mod.min_bars():
            result.skipped.append((prefix, "no_daily"))
            continue
        try:
            trends = align_trends(period_df, daily_df, strict=strict)
            symbol_result = run_symbol(
                universe.main_symbol(variety),
                period,
                period_df,
                trends,
                trade_params,
                box_params,
            )
        except Exception as exc:  # 单品种失败不中断全市场
            result.skipped.append((prefix, "error:%s" % exc))
            continue

        result.trades.extend(symbol_result.trades)
        result.skip_against_trend += symbol_result.skip_against_trend
        result.roll_gaps += symbol_result.roll_gaps
        result.bars[prefix] = len(period_df)
    return result


# ---------------------------------------------------------------- 输出


def sweep_ratios(
    items: Sequence[Tuple[universe.Variety, pd.DataFrame, pd.DataFrame]],
    ratios: Sequence[float],
    *,
    period: str,
    base_params: Optional[TradeParams] = None,
    box_params: Optional[BoxParams] = None,
    strict: bool = False,
) -> List[Dict[str, object]]:
    """逐个盈亏比跑一遍 walk-forward，输出对比行（每行 = rr + 一份汇总）。

    只改止盈/止损比例，入场规则不变；同一份数据上比较「能否盈利」。
    """
    base = base_params or TradeParams()
    rows: List[Dict[str, object]] = []
    for rr in ratios:
        params = with_rr(base, float(rr))
        result = run_frame_backtest(
            items, period=period, params=params, box_params=box_params, strict=strict
        )
        summary = summarize(result.trades, period, result.skip_against_trend)
        rows.append({"rr": float(rr), **summary})
    return rows


def sweep_detail_rows(
    items: Sequence[Tuple[universe.Variety, pd.DataFrame, pd.DataFrame]],
    ratios: Sequence[float],
    *,
    period: str,
    base_params: Optional[TradeParams] = None,
    box_params: Optional[BoxParams] = None,
    strict: bool = False,
) -> List[Dict[str, object]]:
    """逐个盈亏比的明细行（比 ``trade_rows`` 多一列 ``rr``）。"""
    base = base_params or TradeParams()
    rows: List[Dict[str, object]] = []
    for rr in ratios:
        params = with_rr(base, float(rr))
        result = run_frame_backtest(
            items, period=period, params=params, box_params=box_params, strict=strict
        )
        for row in trade_rows(result.trades):
            rows.append({"rr": float(rr), **row})
    return rows


def write_csv(rows: Sequence[Dict[str, object]], path: Optional[str], columns=None) -> Optional[str]:
    if not path:
        return None
    fields = list(columns) if columns is not None else (list(rows[0].keys()) if rows else [])
    with open(path, "w", newline="", encoding="utf-8") as fh:
        writer = csv.DictWriter(fh, fieldnames=fields)
        writer.writeheader()
        for row in rows:
            writer.writerow({key: row.get(key) for key in fields})
    return path


def format_summary(summary: Dict[str, object], skipped: Sequence[Tuple[str, str]] = ()) -> str:
    lines = ["=== 回测汇总 (period=%s) ===" % summary.get("period")]
    for key in SUMMARY_COLUMNS:
        if key == "period":
            continue
        value = summary.get(key)
        if isinstance(value, float):
            lines.append("%-18s %.6g" % (key, value))
        else:
            lines.append("%-18s %s" % (key, value))
    if skipped:
        lines.append("跳过品种：%s" % ", ".join("%s(%s)" % item for item in skipped))
    return "\n".join(lines)


def format_sweep(rows: Sequence[Dict[str, object]]) -> str:
    """盈亏比对比表：每行一个 rr，列 = 汇总指标。"""
    if not rows:
        return "无结果"
    columns = ("rr",) + tuple(c for c in SUMMARY_COLUMNS if c != "period")
    lines = ["=== 盈亏比对比 (period=%s) ===" % rows[0].get("period")]
    lines.append("  ".join("%-14s" % c for c in columns))
    for row in rows:
        cells = []
        for key in columns:
            value = row.get(key)
            cells.append("%-14s" % ("%.4f" % value if isinstance(value, float) else str(value)))
        lines.append("  ".join(cells))
    return "\n".join(lines)


def format_trades(rows: Sequence[Dict[str, object]], limit: int = 0) -> str:
    if not rows:
        return "无成交"
    header = "%-16s %-5s %-4s %-16s %-16s %10s %-16s %10s %8s %-5s"
    lines = [header % ("symbol", "period", "涨跌", "box_start", "entry_time", "entry", "exit_time", "exit", "ret", "reason")]
    selected = rows[:limit] if limit and limit > 0 else rows
    for row in selected:
        lines.append(
            header
            % (
                row["symbol"],
                row["period"],
                row["trend"],
                row["box_start"],
                row["entry_time"],
                "%.4f" % float(row["entry"]),
                row["exit_time"],
                "%.4f" % float(row["exit"]),
                "%.4f" % float(row["return"]),
                row["reason"],
            )
        )
    return "\n".join(lines)


# ---------------------------------------------------------------- TqSdk 拉数


def _wait(api, seconds: float) -> None:
    deadline = time.time() + max(float(seconds), 0.001)
    try:
        api.wait_update(deadline=deadline)
    except TypeError:
        api.wait_update(timeout=max(float(seconds), 0.001))


def fetch_frames(
    api,
    varieties: Sequence[universe.Variety],
    period: str,
    n_bars: Optional[int] = None,
    progress=None,
    max_updates: Optional[int] = None,
):
    """用 TqBacktest 推进时间，把 K 线快照累积成本地 DataFrame。

    TqSdk 的 kline buffer 会滚动复用，所以每次更新把「已走完的根」抄出来。
    """
    trend_sec = universe.trend_seconds(period)
    trend_len = universe.trend_bars(period)
    box_sec = universe.period_seconds(period)
    box_len = int(n_bars or universe.backtest_bars(period))

    trend_klines: Dict[str, object] = {}
    box_klines: Dict[str, object] = {}
    trend_chunks: Dict[str, List[pd.DataFrame]] = {}
    box_chunks: Dict[str, List[pd.DataFrame]] = {}
    cursors: Dict[str, int] = {}

    for start in range(0, len(varieties), SUBSCRIBE_BATCH):
        for variety in varieties[start : start + SUBSCRIBE_BATCH]:
            symbol = universe.main_symbol(variety)
            trend_klines[symbol] = api.get_kline_serial(symbol, trend_sec, trend_len)
            box_klines[symbol] = api.get_kline_serial(symbol, box_sec, box_len)
            trend_chunks[symbol] = []
            box_chunks[symbol] = []
        _wait(api, SUBSCRIBE_WAIT_SECONDS)

    columns = ["datetime", "open", "high", "low", "close", "volume", "open_interest"]

    def harvest(symbol: str) -> None:
        for tag, klines, chunks in (
            ("t", trend_klines, trend_chunks),
            ("b", box_klines, box_chunks),
        ):
            kl = klines.get(symbol)
            if kl is None or len(kl) < 2:
                continue
            try:
                if not api.is_changing(kl):
                    continue
            except Exception:
                pass
            done = kl.iloc[:-1]  # 丢掉还没走完的最后一根
            cursor = cursors.get(tag + symbol)
            if cursor is not None:
                done = done[done["datetime"] > cursor]
            if len(done) == 0:
                continue
            chunks[symbol].append(done[columns].copy())
            cursors[tag + symbol] = int(done["datetime"].iloc[-1])

    updates = 0
    try:
        while True:
            api.wait_update()
            updates += 1
            for variety in varieties:
                harvest(universe.main_symbol(variety))
            if progress and updates % 200 == 0:
                progress("updates=%d" % updates)
            if max_updates is not None and updates >= max_updates:
                break
    except Exception as exc:  # 回测到 end_dt 时 TqSdk 会抛异常结束
        if type(exc).__name__ not in ("TqBacktestEnded", "TqBacktestFinished", "TqTimeoutError"):
            raise

    trend_frames, box_frames = {}, {}
    for variety in varieties:
        symbol = universe.main_symbol(variety)
        if trend_chunks[symbol]:
            trend_frames[symbol] = pd.concat(trend_chunks[symbol]).reset_index(drop=True)
        if box_chunks[symbol]:
            box_frames[symbol] = pd.concat(box_chunks[symbol]).reset_index(drop=True)
    return trend_frames, box_frames


def run_backtest(
    args: argparse.Namespace,
    varieties: Sequence[universe.Variety],
    trade_params: TradeParams,
    box_params: BoxParams,
    user: str,
    password: str,
) -> int:
    from tqsdk import TqApi, TqAuth, TqBacktest  # 延迟导入

    start = date.fromisoformat(args.from_)
    end = date.fromisoformat(args.to)
    api = TqApi(
        auth=TqAuth(user, password),
        backtest=TqBacktest(start_dt=start, end_dt=end),
    )
    try:
        trend_frames, box_frames = fetch_frames(
            api,
            varieties,
            args.period,
            args.n_bars,
            progress=lambda msg: print(msg, file=sys.stderr),
        )
    finally:
        try:
            api.close()
        except Exception:
            pass

    items = []
    missing: List[Tuple[str, str]] = []
    for variety in varieties:
        symbol = universe.main_symbol(variety)
        daily = trend_frames.get(symbol)
        period_df = box_frames.get(symbol)
        if daily is None or len(daily) == 0 or period_df is None or len(period_df) == 0:
            missing.append((variety.prefix, "no_data"))
            continue
        items.append((variety, daily, period_df))

    if args.sweep_rr:
        ratios = parse_ratios(args.sweep_rr)
        print("盈亏比列表：%s（止损 %s×ATR，止盈 = rr × 止损）" % (ratios, trade_params.stop_atr), file=sys.stderr)
        sweep = sweep_ratios(
            items,
            ratios,
            period=args.period,
            base_params=trade_params,
            box_params=box_params,
            strict=args.strict,
        )
        print(format_sweep(sweep))
        best = max(sweep, key=lambda r: float(r["total_return"]))
        print(
            "最好盈亏比 rr=%s：总收益 %.4f，胜率 %.1f%%，盈亏因子 %.2f，最大回撤 %.4f"
            % (
                best["rr"],
                float(best["total_return"]),
                float(best["win_rate"]) * 100,
                float(best["profit_factor"]),
                float(best["max_dd"]),
            )
        )
        if float(best["total_return"]) <= 0:
            print("注意：所有盈亏比的总收益都 ≤ 0，这套参数在该样本上不赚钱", file=sys.stderr)
        print(
            "提示：等名义收益、未算保证金杠杆与滑点，成交按收盘价；样本外请自行复核",
            file=sys.stderr,
        )
        if args.summary_out:
            write_csv(sweep, args.summary_out, columns=["rr"] + list(SUMMARY_COLUMNS))
            print("对比表已写入 %s" % args.summary_out, file=sys.stderr)
        if args.out or args.trades:
            detail = sweep_detail_rows(
                items,
                ratios,
                period=args.period,
                base_params=trade_params,
                box_params=box_params,
                strict=args.strict,
            )
            if args.out:
                write_csv(detail, args.out, columns=["rr"] + list(TRADE_COLUMNS))
                print("明细已写入 %s（%d 行）" % (args.out, len(detail)), file=sys.stderr)
            if args.trades:
                print(format_trades(detail))
        return 0

    result = run_frame_backtest(
        items,
        period=args.period,
        params=trade_params,
        box_params=box_params,
        strict=args.strict,
    )
    result.skipped = missing + result.skipped

    summary = summarize(result.trades, args.period, result.skip_against_trend)
    print(format_summary(summary, result.skipped))
    print("换月跳空保护：%d 根" % result.roll_gaps, file=sys.stderr)
    print(
        "止盈/止损：hold-bars=%d，stop=%s×ATR，tp=%s×ATR（盈亏比 %.2f）"
        % (
            trade_params.hold_bars,
            trade_params.stop_atr,
            round(trade_params.tp_atr, 4),
            (trade_params.tp_atr / trade_params.stop_atr) if trade_params.stop_atr > 0 else 0.0,
        ),
        file=sys.stderr,
    )
    if float(summary["total_return"]) <= 0:
        print("注意：总收益 ≤ 0，这套参数在该样本上不赚钱（可试 --sweep-rr 1,1.5,2,3）", file=sys.stderr)

    rows = trade_rows(result.trades)
    if args.out:
        write_csv(rows, args.out, columns=list(TRADE_COLUMNS))
        print("明细已写入 %s（%d 行）" % (args.out, len(rows)), file=sys.stderr)
    if args.summary_out:
        write_csv([summary], args.summary_out, columns=list(SUMMARY_COLUMNS))
    if args.trades:
        print(format_trades(rows))
    return 0


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = parse_args(argv)
    try:
        trade_params, box_params = build_params(args)
        varieties = universe.select(args.only)
    except (ValueError, universe.UnknownPrefixError) as exc:
        print("参数错误：%s" % exc, file=sys.stderr)
        return 2

    user = os.environ.get("TQ_USER", "").strip()
    password = os.environ.get("TQ_PASS", "").strip()
    if not user or not password:
        print(
            "缺少天勤账号：请先设置环境变量 TQ_USER / TQ_PASS（免费模拟账号即可）",
            file=sys.stderr,
        )
        return 2

    try:
        return run_backtest(args, varieties, trade_params, box_params, user, password)
    except ImportError as exc:
        print("缺少 tqsdk：python3 -m pip install tqsdk pandas（%s）" % exc, file=sys.stderr)
        return 2
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    sys.exit(main())
