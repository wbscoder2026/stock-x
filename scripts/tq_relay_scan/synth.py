"""合成 K 线工具 —— 仅测试用（不被 scan.py / backtest.py 引用）。

构造约定
--------
每根 K 线以收盘价为中心对称展开 ``±range/2``，且相邻收盘步长 ``<= range/2``，
于是该根 ``TR == range``；连续常数 TR 下 Wilder ATR **精确等于** ``range``，
方便写出没有容差魔数的断言。

标准「多头中继」序列（``relay()`` 默认参数）::

    pad 8 根(close=100, range=4)
    → 冲动 12 根(每根 +2, 收盘 102..124, Δ=22)
    → 箱体 12 根(收盘 126 / 130.6 交替, range=1)

    ATR_ref = 4（箱体左端前一根 = 最后一根冲动）
    箱高 H = max(high) - min(low) = 5.6 = 1.4 ATR
    箱沿（收盘）= [126, 130.6]
"""

from __future__ import annotations

from typing import Dict, List, Optional, Sequence, Union

import pandas as pd

FREQ_SECONDS = {"5": 300, "15": 900, "30": 1800, "60": 3600, "120": 7200, "1d": 86400}

DEFAULT_START = "2026-01-05 09:00:00"

Number = Union[int, float]


def bars(
    closes: Sequence[Number],
    range_: Union[Number, Sequence[Number]] = 4.0,
    *,
    volume: Number = 1000.0,
    open_interest: Number = 100000.0,
    start: str = DEFAULT_START,
    step_min: int = 15,
) -> pd.DataFrame:
    """由收盘价序列构造 K 线（open = 上一根收盘，high/low 对称展开）。"""
    n = len(closes)
    if isinstance(range_, (int, float)):
        ranges: List[float] = [float(range_)] * n
    else:
        ranges = [float(x) for x in range_]
    idx = pd.date_range(start=start, periods=n, freq="%dmin" % step_min)
    if n == 0:
        return pd.DataFrame(
            columns=["datetime", "open", "high", "low", "close", "volume", "open_interest"]
        )
    close = pd.Series([float(c) for c in closes], index=idx)
    rng = pd.Series(ranges, index=idx)
    df = pd.DataFrame(
        {
            "open": close.shift(1).fillna(close.iloc[0]),
            "high": close + rng / 2.0,
            "low": close - rng / 2.0,
            "close": close,
            "volume": float(volume),
            "open_interest": float(open_interest),
        }
    )
    df.insert(0, "datetime", idx)
    return df


def to_ns(df: pd.DataFrame) -> pd.DataFrame:
    """把 datetime 列换成 TqSdk 风格的 int64 纳秒。"""
    out = df.copy()
    out["datetime"] = out["datetime"].astype("int64")
    return out


def phase_indices(pad: int = 8, impulse_n: int = 12, w: int = 12) -> Dict[str, int]:
    """标准序列各阶段的下标（含端点）。"""
    impulse_start = pad
    box_start = pad + impulse_n
    return {
        "pad_start": 0,
        "impulse_start": impulse_start,
        "box_start": box_start,
        "box_end": box_start + w - 1,
        "n_bars": box_start + w,
    }


def relay(
    direction: str = "long",
    *,
    atr_range: float = 4.0,
    pad: int = 8,
    impulse_n: int = 12,
    impulse_step: float = 2.0,
    box_height_atr: float = 1.4,
    w: int = 12,
    box_range: float = 1.0,
    box_offset: float = 2.0,
    breakout_step: Optional[float] = None,
    post_bars: int = 0,
    post_step: float = 2.0,
    step_min: int = 15,
    start: str = DEFAULT_START,
    volume: float = 1000.0,
) -> pd.DataFrame:
    """构造「冲动 + 中继箱体（+ 可选突破/后续根）」的合成序列。

    :param direction: ``long`` 上涨冲动；``short`` 镜像为下跌冲动
    :param atr_range: 冲动/pad 根振幅（= 箱体左端 ATR 的精确值）
    :param box_height_atr: 箱高 / ATR，用于构造「命中(1.4)」「过矮(0.5)」「过高(3)」
    :param breakout_step: 若给定，箱体后追加一根突破根（收盘 = 最后箱内收盘 + 该值）
    """
    if direction not in ("long", "short"):
        raise ValueError("direction 只能是 long / short")
    c_range = box_height_atr * atr_range - box_range
    if c_range < 0:
        raise ValueError("箱高小于单根振幅，无法构造该组合")

    closes: List[float] = []
    ranges: List[float] = []

    closes.extend([100.0] * pad)
    ranges.extend([atr_range] * pad)

    last = 100.0
    for _ in range(impulse_n):
        last += impulse_step
        closes.append(last)
        ranges.append(atr_range)

    c0 = last + box_offset
    for k in range(w):
        closes.append(c0 + (c_range if k % 2 else 0.0))
        ranges.append(box_range)

    if breakout_step is not None:
        closes.append(closes[-1] + breakout_step)
        ranges.append(box_range)

    for _ in range(post_bars):
        closes.append(closes[-1] + post_step)
        ranges.append(box_range)

    if direction == "short":
        closes = [200.0 - c for c in closes]

    return bars(closes, ranges, step_min=step_min, start=start, volume=volume)


def relay_short(**kwargs) -> pd.DataFrame:
    """``relay(direction="short", ...)`` 的快捷方式。"""
    return relay("short", **kwargs)


def trending_daily(
    n: int = 120,
    *,
    start_price: float = 100.0,
    drift: float = 0.5,
    start: str = "2025-01-06 15:00:00",
    step_min: int = 1440,
    tail_drop: float = 0.0,
    tail_bars: int = 0,
) -> pd.DataFrame:
    """线性上行日线（drift > 0 → 多头；< 0 → 空头）。"""
    closes = [start_price + drift * i for i in range(n)]
    for k in range(tail_bars):
        closes[n - tail_bars + k] = closes[n - tail_bars - 1] - tail_drop * (k + 1)
    return bars(closes, range_=4.0, start=start, step_min=step_min)


def flat_daily(n: int = 120, *, level: float = 100.0) -> pd.DataFrame:
    """纯横盘日线 → 趋势判定应为 None。"""
    return bars([level] * n, range_=4.0, start="2025-01-06 15:00:00", step_min=1440)
