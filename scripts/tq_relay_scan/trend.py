"""大周期趋势过滤（纯函数 + pandas，不依赖 TqApi）。

对日线收盘（``--period 1d`` 时由调用方改喂周线）::

    ema20 = EMA(close, 20)
    ema60 = EMA(close, 60)

    多：ema20 > ema60 且 close > ema20 且近 10 日 ema20 斜率为正
    空：ema20 < ema60 且 close < ema20 且近 10 日 ema20 斜率为负
    其余：丢弃（震荡 / 死叉初期 / 价格回到均线纠结）

只用方向过滤，不在箱体周期上再叠一套均线。
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Optional

import numpy as np
import pandas as pd

LONG = "long"
SHORT = "short"

EMA_FAST = 20
EMA_SLOW = 60
SLOPE_LOOKBACK = 10
STRICT_WINDOW = 20


@dataclass
class TrendState:
    trend: Optional[str]  # "long" / "short" / None
    ema_fast: float
    ema_slow: float
    close: float
    slope: float
    reason: str  # "" / "insufficient" / "range" / "strict_pullback"


def min_bars(ema_slow: int = EMA_SLOW, slope_lookback: int = SLOPE_LOOKBACK) -> int:
    """判定趋势所需最少根数。"""
    return int(ema_slow + slope_lookback)


def ema(series: pd.Series, span: int) -> pd.Series:
    """EMA（前 ``span-1`` 根为 NaN，不用未成熟值）。"""
    return series.ewm(span=span, adjust=False, min_periods=span).mean()


def trend_frame(
    df: pd.DataFrame,
    *,
    ema_fast: int = EMA_FAST,
    ema_slow: int = EMA_SLOW,
    slope_lookback: int = SLOPE_LOOKBACK,
    strict: bool = False,
    strict_window: int = STRICT_WINDOW,
) -> pd.DataFrame:
    """逐根趋势（walk-forward 安全：第 i 行只依赖 <= i 的数据）。

    返回列：``ema_fast`` / ``ema_slow`` / ``slope`` / ``trend``。
    """
    close = df["close"]
    ef = ema(close, ema_fast)
    es = ema(close, ema_slow)
    slope = ef - ef.shift(slope_lookback)

    trend_col = pd.Series(None, index=df.index, dtype=object)

    long_cond = (ef > es) & (close > ef) & (slope > 0)
    short_cond = (ef < es) & (close < ef) & (slope < 0)

    if strict and len(df) >= strict_window:
        # 多：近 N 根低点不破 EMA20；空：近 N 根高点不破 EMA20
        hold_long = (df["low"] > ef).astype(float).rolling(strict_window).min() == 1.0
        hold_short = (df["high"] < ef).astype(float).rolling(strict_window).min() == 1.0
        long_cond = long_cond & hold_long.fillna(False)
        short_cond = short_cond & hold_short.fillna(False)

    trend_col[long_cond.fillna(False)] = LONG
    trend_col[short_cond.fillna(False)] = SHORT

    need = min_bars(ema_slow, slope_lookback)
    if need > 0:
        warm = np.arange(len(df)) >= (need - 1)
        trend_col[~warm] = None

    return pd.DataFrame(
        {
            "ema_fast": ef,
            "ema_slow": es,
            "slope": slope,
            "trend": trend_col,
        },
        index=df.index,
    )


def trend_series(df: pd.DataFrame, **kwargs) -> pd.Series:
    """逐根趋势序列（``long`` / ``short`` / None）。"""
    if df is None or len(df) == 0:
        return pd.Series([], dtype=object)
    return trend_frame(df, **kwargs)["trend"]


def trend_state(df: pd.DataFrame, **kwargs) -> TrendState:
    """最后一根的完整趋势状态（含原因，便于日志）。"""
    ema_slow = int(kwargs.get("ema_slow", EMA_SLOW))
    slope_lookback = int(kwargs.get("slope_lookback", SLOPE_LOOKBACK))
    need = min_bars(ema_slow, slope_lookback)

    if df is None or len(df) == 0:
        return TrendState(None, np.nan, np.nan, np.nan, np.nan, "insufficient")

    close = float(df["close"].iloc[-1])
    if len(df) < need:
        return TrendState(None, np.nan, np.nan, close, np.nan, "insufficient")

    row = trend_frame(df, **kwargs).iloc[-1]
    trend_value = row["trend"]
    if trend_value is None or (isinstance(trend_value, float) and np.isnan(trend_value)):
        trend_value = None

    reason = ""
    if trend_value is None:
        if kwargs.get("strict", False):
            loose_kwargs = dict(kwargs)
            loose_kwargs["strict"] = False
            loose = trend_frame(df, **loose_kwargs).iloc[-1]["trend"]
            if loose is not None and not (isinstance(loose, float) and np.isnan(loose)):
                reason = "strict_pullback"
        if not reason:
            reason = "range"

    return TrendState(
        trend=trend_value,
        ema_fast=float(row["ema_fast"]),
        ema_slow=float(row["ema_slow"]),
        close=close,
        slope=float(row["slope"]),
        reason=reason,
    )
