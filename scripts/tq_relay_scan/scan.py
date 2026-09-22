"""扫描入口：全品种中继箱体扫描（TqSdk 订阅 + 纯函数判定 + CSV/stdout）。

    python3 scripts/tq_relay_scan/scan.py
    python3 scripts/tq_relay_scan/scan.py --period 15 --out candidates.csv
    python3 scripts/tq_relay_scan/scan.py --period 5 --only JM,RB,CU --watch 60

不做下单，仅研究。箱体/趋势判定全部走 ``box.py`` / ``trend.py``（与 TqApi 无关）。
"""

from __future__ import annotations

import argparse
import csv
import os
import sys
import time
from datetime import datetime
from typing import Dict, List, Optional, Sequence

import box as box_mod
import trend as trend_mod
import universe
from box import Box, BoxParams

PERIOD_CHOICES = ("5", "15", "30", "60", "120", "1d")

#: 扫描 CSV 列（顺序即方案定义）
CSV_COLUMNS = (
    "time",
    "period",
    "prefix",
    "name",
    "symbol",
    "trend",
    "box_high",
    "box_low",
    "height",
    "height_atr",
    "atr",
    "impulse_atr",
    "bars",
    "last",
    "dist_edge_atr",
    "box_start",
    "box_end",
)

TREND_LABEL = {box_mod.LONG: "多", box_mod.SHORT: "空"}

#: 一次订阅多少品种，超时（TqSdk 首次连接慢）就分批
SUBSCRIBE_BATCH = 20
SUBSCRIBE_WAIT_SECONDS = 30

IDEAL_HEIGHT_ATR = box_mod.IDEAL_HEIGHT_ATR


def parse_args(argv: Optional[Sequence[str]] = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="全品种中继箱体扫描（TqSdk）")
    parser.add_argument("--period", default="15", choices=PERIOD_CHOICES, help="K 线级别，默认 15")
    parser.add_argument("--only", default=None, help="按品种码过滤，如 JM,RB,CU")
    parser.add_argument("--out", default=None, help="候选 CSV 路径")
    parser.add_argument("--watch", type=int, default=0, help="每 N 秒重算，只打印新出现的品种")
    parser.add_argument("--n-bars", type=int, default=None, help="箱体周期订阅根数（默认按 --period 查表）")
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
    parser.add_argument("--min-turnover", type=float, default=universe.DEFAULT_MIN_TURNOVER)
    parser.add_argument("--min-oi", type=float, default=0.0)
    parser.add_argument("--verbose", action="store_true", help="打印被流动性过滤掉的品种")
    return parser.parse_args(argv)


def build_params(args: argparse.Namespace) -> BoxParams:
    params = BoxParams(
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
    if params.w_min < 2 or params.w_max < params.w_min:
        raise ValueError("w-min / w-max 非法：%s / %s" % (params.w_min, params.w_max))
    if params.impulse_n < 1:
        raise ValueError("impulse-n 必须 >= 1")
    if not (0 < params.atr_lo <= params.atr_hi):
        raise ValueError("atr-lo / atr-hi 非法：%s / %s" % (params.atr_lo, params.atr_hi))
    return params


# ---------------------------------------------------------------- 纯函数管线


def build_row(
    variety: universe.Variety,
    box: Box,
    last: float,
    period: str,
    now: Optional[str] = None,
) -> Dict[str, object]:
    return {
        "time": now or datetime.now().strftime("%Y-%m-%d %H:%M:%S"),
        "period": period,
        "prefix": variety.prefix,
        "name": variety.name,
        "symbol": universe.main_symbol(variety),
        "trend": TREND_LABEL[box.direction],
        "box_high": round(box.high, 6),
        "box_low": round(box.low, 6),
        "height": round(box.height, 6),
        "height_atr": round(box.height_atr, 4),
        "atr": round(box.atr, 6),
        "impulse_atr": round(box.impulse_atr, 4),
        "bars": box.w,
        "last": round(last, 6),
        "dist_edge_atr": round(box.dist_edge_atr, 4),
        "box_start": box.start_time,
        "box_end": box.end_time,
    }


def evaluate_symbol(
    variety: universe.Variety,
    daily_df,
    period_df,
    *,
    period: str = "15",
    params: Optional[BoxParams] = None,
    strict: bool = False,
    now: Optional[str] = None,
) -> Optional[Dict[str, object]]:
    """趋势过滤 → 箱体检测 → 一行候选（不满足返回 None）。"""
    if daily_df is None or period_df is None or len(period_df) == 0:
        return None
    state = trend_mod.trend_state(daily_df, strict=strict)
    if state.trend is None:
        return None
    box = box_mod.detect_box(period_df, params)
    if box is None or box.direction != state.trend:
        return None
    return build_row(variety, box, float(period_df["close"].iloc[-1]), period, now)


def sort_rows(rows: List[Dict[str, object]]) -> List[Dict[str, object]]:
    """按 ``height_atr`` 接近 1.4、再按 ``dist_edge_atr`` 升序。"""
    return sorted(
        rows,
        key=lambda r: (abs(float(r["height_atr"]) - IDEAL_HEIGHT_ATR), float(r["dist_edge_atr"])),
    )


def new_rows(seen: set, rows: List[Dict[str, object]]) -> List[Dict[str, object]]:
    """``--watch`` 用：只返回没见过（并记入 ``seen``）的品种。"""
    fresh: List[Dict[str, object]] = []
    for row in rows:
        key = row.get("prefix")
        if key in seen:
            continue
        seen.add(key)
        fresh.append(row)
    return fresh


def format_rows(rows: List[Dict[str, object]]) -> str:
    if not rows:
        return "无候选"
    lines = [
        "%-19s %-5s %-6s %-8s %-4s %10s %10s %8s %6s %8s %6s %5s %10s %6s %s"
        % (
            "time",
            "period",
            "prefix",
            "name",
            "trend",
            "box_high",
            "box_low",
            "height",
            "h/atr",
            "atr",
            "Δ/atr",
            "bars",
            "last",
            "d_edge",
            "box_start ~ box_end",
        )
    ]
    for row in rows:
        lines.append(
            "%-19s %-5s %-6s %-8s %-4s %10.2f %10.2f %8.2f %6.2f %8.2f %6.2f %5d %10.2f %6.2f %s ~ %s"
            % (
                row["time"],
                row["period"],
                row["prefix"],
                row["name"],
                row["trend"],
                row["box_high"],
                row["box_low"],
                row["height"],
                row["height_atr"],
                row["atr"],
                row["impulse_atr"],
                row["bars"],
                row["last"],
                row["dist_edge_atr"],
                row["box_start"],
                row["box_end"],
            )
        )
    return "\n".join(lines)


def write_csv(rows: Sequence[Dict[str, object]], path: Optional[str]) -> Optional[str]:
    if not path:
        return None
    with open(path, "w", newline="", encoding="utf-8") as fh:
        writer = csv.DictWriter(fh, fieldnames=list(CSV_COLUMNS))
        writer.writeheader()
        for row in rows:
            writer.writerow({key: row.get(key) for key in CSV_COLUMNS})
    return path


# ---------------------------------------------------------------- TqSdk 订阅


def _wait(api, seconds: float) -> None:
    deadline = time.time() + max(float(seconds), 0.001)
    try:
        api.wait_update(deadline=deadline)
    except TypeError:  # 部分版本用 timeout
        api.wait_update(timeout=max(float(seconds), 0.001))


def subscribe_batched(
    api,
    varieties: Sequence[universe.Variety],
    period: str,
    n_bars: Optional[int] = None,
    batch: int = SUBSCRIBE_BATCH,
    wait_seconds: float = SUBSCRIBE_WAIT_SECONDS,
):
    """分批订阅（全市场一次约 50~60 主连 × 2 周期，容易超时）。"""
    trend_sec = universe.trend_seconds(period)
    trend_len = universe.trend_bars(period)
    box_sec = universe.period_seconds(period)
    box_len = int(n_bars or universe.scan_bars(period))

    trend_klines, box_klines = {}, {}
    for start in range(0, len(varieties), batch):
        for variety in varieties[start : start + batch]:
            symbol = universe.main_symbol(variety)
            trend_klines[symbol] = api.get_kline_serial(symbol, trend_sec, trend_len)
            box_klines[symbol] = api.get_kline_serial(symbol, box_sec, box_len)
        _wait(api, wait_seconds)
    return trend_klines, box_klines


def scan_once(
    varieties: Sequence[universe.Variety],
    trend_klines: Dict[str, object],
    box_klines: Dict[str, object],
    args: argparse.Namespace,
    params: BoxParams,
    now: Optional[str] = None,
) -> List[Dict[str, object]]:
    """一轮扫描（K 线已在 buffer 里）。单品种失败只记 warning。"""
    rows: List[Dict[str, object]] = []
    for variety in varieties:
        symbol = universe.main_symbol(variety)
        try:
            daily = box_mod.complete_bars(trend_klines[symbol])
            period_df = box_mod.complete_bars(box_klines[symbol])
        except KeyError:
            print("warning: %s 无订阅数据" % variety.prefix, file=sys.stderr)
            continue

        ok, reason = universe.liquidity_ok(
            daily,
            variety,
            min_turnover=args.min_turnover,
            min_oi=args.min_oi,
        )
        if not ok:
            if args.verbose:
                print("skip %s: %s" % (variety.prefix, reason), file=sys.stderr)
            continue

        try:
            row = evaluate_symbol(
                variety,
                daily,
                period_df,
                period=args.period,
                params=params,
                strict=args.strict,
                now=now,
            )
        except Exception as exc:  # 单品种失败不中断全市场
            print("warning: %s 扫描失败：%s" % (variety.prefix, exc), file=sys.stderr)
            continue
        if row is not None:
            rows.append(row)
    return sort_rows(rows)


def run_scan(args: argparse.Namespace, varieties, params: BoxParams, user: str, password: str) -> int:
    from tqsdk import TqApi, TqAuth  # 延迟导入：无 tqsdk / 无账号时给出清晰报错

    api = TqApi(auth=TqAuth(user, password))
    try:
        trend_klines, box_klines = subscribe_batched(api, varieties, args.period, args.n_bars)
        seen: set = set()
        all_rows: List[Dict[str, object]] = []
        while True:
            rows = scan_once(varieties, trend_klines, box_klines, args, params)
            if args.watch > 0:
                fresh = new_rows(seen, rows)
                if fresh:
                    print(format_rows(fresh))
                all_rows = rows
            else:
                all_rows = rows
                print(format_rows(rows))
                break

            if args.out:
                write_csv(all_rows, args.out)
            _wait(api, args.watch)
        if args.out:
            write_csv(all_rows, args.out)
            print("已写入 %s（%d 行）" % (args.out, len(all_rows)), file=sys.stderr)
        return 0
    finally:
        try:
            api.close()
        except Exception:
            pass


def main(argv: Optional[Sequence[str]] = None) -> int:
    args = parse_args(argv)
    try:
        params = build_params(args)
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
        return run_scan(args, varieties, params, user, password)
    except ImportError as exc:
        print("缺少 tqsdk：python3 -m pip install tqsdk pandas（%s）" % exc, file=sys.stderr)
        return 2
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    sys.exit(main())
