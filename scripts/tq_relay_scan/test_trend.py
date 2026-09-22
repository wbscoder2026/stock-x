"""trend.py 的单测 —— 日线 EMA20/60 大趋势过滤。"""

from __future__ import annotations

import pandas as pd
import pytest

import synth
import trend

NEED = 60 + 10  # ema_slow + slope_lookback


def test_uptrend_is_long():
    df = synth.trending_daily(n=120, drift=0.5)
    state = trend.trend_state(df)
    assert state.trend == "long"
    assert state.ema_fast > state.ema_slow
    assert state.close > state.ema_fast
    assert state.slope > 0


def test_downtrend_is_short():
    df = synth.trending_daily(n=120, drift=-0.5)
    state = trend.trend_state(df)
    assert state.trend == "short"
    assert state.ema_fast < state.ema_slow
    assert state.close < state.ema_fast
    assert state.slope < 0


def test_flat_market_is_range():
    df = synth.flat_daily(n=120)
    state = trend.trend_state(df)
    assert state.trend is None
    assert state.reason == "range"


def test_insufficient_bars():
    df = synth.trending_daily(n=NEED - 1, drift=0.5)
    state = trend.trend_state(df)
    assert state.trend is None
    assert state.reason == "insufficient"


def test_uptrend_with_deep_pullback_is_range():
    """上升趋势但最后一根收盘跌回 EMA20 下方 → 不做多。"""
    df = synth.trending_daily(n=120, drift=0.5, tail_drop=40.0, tail_bars=1)
    state = trend.trend_state(df)
    assert state.trend is None


def test_strict_rejects_wick_through_ema():
    df = synth.trending_daily(n=120, drift=0.5)
    last = df.index[-1]
    close = float(df["close"].iloc[-1])
    df.loc[last, "low"] = close - 10.0
    df.loc[last, "high"] = close + 10.0

    assert trend.trend_state(df).trend == "long"
    strict = trend.trend_state(df, strict=True)
    assert strict.trend is None
    assert strict.reason == "strict_pullback"


def test_trend_series_has_no_future_function():
    full = synth.trending_daily(n=120, drift=0.5)
    series = trend.trend_series(full)
    assert len(series) == len(full)
    assert series.iloc[: NEED - 1].isna().all()
    for k in (NEED, NEED + 1, 90, 120):
        part = trend.trend_series(full.iloc[:k])
        assert part.iloc[-1] == series.iloc[k - 1]


def test_trend_series_last_matches_state():
    df = synth.trending_daily(n=120, drift=0.5)
    assert trend.trend_series(df).iloc[-1] == trend.trend_state(df).trend


def test_ema_matches_pandas_ewm():
    df = synth.trending_daily(n=80, drift=0.3)
    got = trend.ema(df["close"], 20)
    expect = df["close"].ewm(span=20, adjust=False, min_periods=20).mean()
    assert got.iloc[-1] == pytest.approx(expect.iloc[-1])
    assert pd.isna(got.iloc[18])


def test_trend_frame_columns_and_no_nan_leak():
    df = synth.trending_daily(n=120, drift=0.5)
    frame = trend.trend_frame(df)
    assert list(frame.columns) == ["ema_fast", "ema_slow", "slope", "trend"]
    assert frame["trend"].iloc[-1] == "long"
    assert frame["trend"].iloc[: NEED - 1].isna().all()
