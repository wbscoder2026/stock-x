"""中继箱体检测（纯函数 + pandas，周期无关，不依赖 TqApi）。

定义（对上一步结果逐条可测）:

1. 箱体窗口 ``W``：最近连续 ``w`` 根（``w ∈ [w_min, w_max]``，默认 6~20）
2. 箱高 ``H = max(high[W]) - min(low[W])``
3. ``ATR`` = 窗口左端（箱体开始前一根）的 ATR，避免横盘把 ATR 压扁
4. 硬条件 ``atr_lo*ATR <= H <= atr_hi*ATR``（默认 1~2）
5. 冲动：箱体左侧再取 ``impulse_n`` 根，``Δ = |close[末] - close[首]|``，
   多头要求 Δ ≥ 1.5ATR 且冲动低点 < 箱体低点（空头对称）
6. 未完成突破：窗口收盘不越过箱沿 ± ``0.15*ATR``（箱沿取「已确认」的前 w-1 根收盘极值）
7. 中继而非反转：箱体中轴不低于「冲动终点 - 50% Δ」（空头对称）
8. 当前仍在箱内：最新收盘落在 ``[box_low - 0.1ATR, box_high + 0.1ATR]``

箱沿按**收盘价**取（插针不计入箱沿），箱高按最高/最低取（与方案一致）。
"""

from __future__ import annotations

import time
from dataclasses import dataclass
from typing import Optional, Tuple

import numpy as np
import pandas as pd

ATR_PERIOD = 14
ATR_LO = 1.0
ATR_HI = 2.0
IMPULSE_N = 12
IMPULSE_MIN_ATR = 1.5
W_MIN = 6
W_MAX = 20
BREAK_BUF_ATR = 0.15
RETRACEMENT_MAX = 0.5

#: 距箱沿多近算「贴边」（评分用）
TOUCH_BUF_ATR = 0.15
#: 仍算「在箱内」的容差
INSIDE_BUF_ATR = 0.1
#: 评分时箱高/ATR 的理想值
IDEAL_HEIGHT_ATR = 1.4

LONG = "long"
SHORT = "short"


@dataclass
class BoxParams:
    atr_period: int = ATR_PERIOD
    atr_lo: float = ATR_LO
    atr_hi: float = ATR_HI
    impulse_n: int = IMPULSE_N
    impulse_min_atr: float = IMPULSE_MIN_ATR
    w_min: int = W_MIN
    w_max: int = W_MAX
    break_buf_atr: float = BREAK_BUF_ATR
    retracement_max: float = RETRACEMENT_MAX


@dataclass
class Box:
    direction: str  # long / short
    w: int  # 箱体根数
    high: float  # 箱沿上（收盘）
    low: float  # 箱沿下（收盘）
    height: float  # H = max(high) - min(low)
    height_atr: float  # H / ATR
    atr: float  # 箱体左端前一根的 ATR
    impulse_atr: float  # Δ / ATR
    impulse_start_close: float
    impulse_end_close: float
    mid: float  # 箱体中轴
    last_close: float
    dist_edge_atr: float  # 距更近箱沿 / ATR
    touches_break: float  # 贴边收盘占比（假突破打分用）
    score: float
    start_i: int
    end_i: int
    start_time: str
    end_time: str


# ---------------------------------------------------------------- 基础工具


def atr_series(df: pd.DataFrame, n: int = ATR_PERIOD) -> pd.Series:
    """Wilder ATR（前 ``n-1`` 根为 NaN；第 i 根只用 <= i 的数据）。"""
    if df is None or len(df) == 0:
        return pd.Series([], dtype=float)
    high = df["high"].astype(float)
    low = df["low"].astype(float)
    close = df["close"].astype(float)
    prev_close = close.shift(1)
    tr = pd.concat(
        [(high - low).abs(), (high - prev_close).abs(), (low - prev_close).abs()], axis=1
    ).max(axis=1)

    out = np.full(len(df), np.nan, dtype=float)
    if len(df) >= n:
        trv = tr.to_numpy(dtype=float)
        acc = float(np.mean(trv[:n]))
        out[n - 1] = acc
        for i in range(n, len(trv)):
            acc += (trv[i] - acc) / n
            out[i] = acc
    return pd.Series(out, index=df.index)


def _value(row: pd.Series, name: str):
    """取列值（datetime 等非数值也原样返回）。"""
    try:
        return row[name]
    except (KeyError, IndexError, TypeError):
        return None


def fmt_time(value) -> str:
    """TqSdk 纳秒 / pandas Timestamp → ``YYYY-mm-dd HH:MM``。"""
    if value is None:
        return ""
    if isinstance(value, pd.Timestamp):
        return value.strftime("%Y-%m-%d %H:%M")
    if isinstance(value, (int, float, np.integer, np.floating)):
        if not np.isfinite(float(value)):
            return ""
        return pd.to_datetime(int(value), unit="ns").strftime("%Y-%m-%d %H:%M")
    try:
        return pd.Timestamp(value).strftime("%Y-%m-%d %H:%M")
    except Exception:  # pragma: no cover - 兜底
        return str(value)


def complete_bars(df: pd.DataFrame, now=None) -> pd.DataFrame:
    """丢掉还没走完的最后一根（TqSdk 未收盘 K 线的时间戳在未来）。"""
    if df is None or len(df) == 0 or "datetime" not in df.columns:
        return df
    col = df["datetime"]
    if pd.api.types.is_datetime64_any_dtype(col):
        limit = now if now is not None else pd.Timestamp.now()
        if isinstance(limit, (int, float, np.integer, np.floating)):
            limit = pd.to_datetime(int(limit), unit="ns")
        else:
            limit = pd.Timestamp(limit)
        return df[col <= limit]
    if pd.api.types.is_integer_dtype(col):
        limit_ns = int(now) if now is not None else int(time.time() * 10**9)
        return df[col <= limit_ns]
    return df


# ---------------------------------------------------------------- 单窗口评估


def evaluate_window(
    df: pd.DataFrame,
    end_i: int,
    w: int,
    params: Optional[BoxParams] = None,
    atr: Optional[pd.Series] = None,
) -> Tuple[Optional[Box], str]:
    """评估「以 ``end_i`` 结尾的连续 w 根」是否构成中继箱体。

    返回 ``(Box 或 None, 原因)``；原因取值见 ``REASONS``。
    """
    p = params or BoxParams()
    if df is None or len(df) == 0:
        return None, "short_history"
    if end_i < 0:
        end_i += len(df)
    if end_i >= len(df) or end_i < 0:
        return None, "short_history"

    start_i = end_i - w + 1
    impulse_end_i = start_i - 1
    impulse_start_i = start_i - p.impulse_n
    if w < 2 or impulse_start_i < 0 or impulse_end_i < 0:
        return None, "short_history"

    if atr is None:
        atr = atr_series(df, p.atr_period)
    atr_ref = float(atr.iloc[impulse_end_i]) if impulse_end_i < len(atr) else np.nan
    if not np.isfinite(atr_ref) or atr_ref <= 0:
        return None, "atr_warmup"

    win = df.iloc[start_i : end_i + 1]
    highs = win["high"].to_numpy(dtype=float)
    lows = win["low"].to_numpy(dtype=float)
    closes = win["close"].to_numpy(dtype=float)
    height = float(highs.max() - lows.min())

    # 4. 箱高必须落在 [atr_lo, atr_hi] × ATR
    if not (p.atr_lo * atr_ref <= height <= p.atr_hi * atr_ref):
        return None, "height_band"

    # 箱沿 = 「已确认」的前 w-1 根收盘极值；最新一根只判「是否仍在箱内」
    confirmed = closes[:-1] if w >= 2 else closes
    box_high = float(confirmed.max())
    box_low = float(confirmed.min())
    last_close = float(closes[-1])
    buf = p.break_buf_atr * atr_ref

    # 5. 冲动
    impulse = df.iloc[impulse_start_i : impulse_end_i + 1]
    imp_closes = impulse["close"].to_numpy(dtype=float)
    imp_lows = impulse["low"].to_numpy(dtype=float)
    imp_highs = impulse["high"].to_numpy(dtype=float)
    imp_start_close = float(imp_closes[0])
    imp_end_close = float(imp_closes[-1])
    delta = abs(imp_end_close - imp_start_close)
    if delta < p.impulse_min_atr * atr_ref:
        return None, "impulse_weak"
    direction = LONG if imp_end_close > imp_start_close else SHORT
    if direction == LONG:
        if float(imp_lows.min()) >= box_low:
            return None, "impulse_low"
    else:
        if float(imp_highs.max()) <= box_high:
            return None, "impulse_low"

    # 6. 窗口内收盘不有效越过箱沿（允许插针，不允许收盘站稳箱外）
    if bool(((closes > box_high + buf) | (closes < box_low - buf)).any()):
        return None, "broke_out"

    # 7. 中继而非反转：中轴回撤不超过冲动幅度的 retracement_max
    mid = (box_high + box_low) / 2.0
    if direction == LONG:
        if mid < imp_end_close - p.retracement_max * delta:
            return None, "no_retrace"
    else:
        if mid > imp_end_close + p.retracement_max * delta:
            return None, "no_retrace"

    # 8. 当前仍在箱内
    inside_buf = INSIDE_BUF_ATR * atr_ref
    if not (box_low - inside_buf <= last_close <= box_high + inside_buf):
        return None, "outside_now"

    touch_buf = TOUCH_BUF_ATR * atr_ref
    touches = int(((closes >= box_high - touch_buf) | (closes <= box_low + touch_buf)).sum())
    touches_break = touches / float(w)
    score = (
        1.0
        - abs(height / atr_ref - IDEAL_HEIGHT_ATR)
        + 0.3 * min(delta / atr_ref, 3.0) / 3.0
        + 0.2 * (1.0 - touches_break)
    )

    return (
        Box(
            direction=direction,
            w=w,
            high=box_high,
            low=box_low,
            height=height,
            height_atr=height / atr_ref,
            atr=atr_ref,
            impulse_atr=delta / atr_ref,
            impulse_start_close=imp_start_close,
            impulse_end_close=imp_end_close,
            mid=mid,
            last_close=last_close,
            dist_edge_atr=min(box_high - last_close, last_close - box_low) / atr_ref,
            touches_break=touches_break,
            score=score,
            start_i=start_i,
            end_i=end_i,
            start_time=fmt_time(_value(win.iloc[0], "datetime")),
            end_time=fmt_time(_value(win.iloc[-1], "datetime")),
        ),
        "ok",
    )


REASONS = (
    "ok",
    "short_history",
    "atr_warmup",
    "height_band",
    "impulse_weak",
    "impulse_low",
    "broke_out",
    "no_retrace",
    "outside_now",
)


def detect_box(
    df: pd.DataFrame,
    params: Optional[BoxParams] = None,
    atr: Optional[pd.Series] = None,
    end_i: Optional[int] = None,
) -> Optional[Box]:
    """每个品种只取一个最佳箱体：w 从 ``w_min`` 扫到 ``w_max``，评分最高者胜（同分取更长）。"""
    p = params or BoxParams()
    if df is None or len(df) < p.w_min + p.impulse_n + 2:
        return None
    if atr is None:
        atr = atr_series(df, p.atr_period)
    if end_i is None:
        end_i = len(df) - 1

    best: Optional[Box] = None
    for w in range(p.w_min, p.w_max + 1):
        box, _reason = evaluate_window(df, end_i, w, p, atr)
        if box is None:
            continue
        if best is None or box.score > best.score or (box.score == best.score and box.w > best.w):
            best = box
    return best
