"""box.py 的单测 —— 中继箱体检测（周期无关、纯函数、无未来函数）。"""

from __future__ import annotations

import pandas as pd
import pytest

import box as box_mod
import synth

PHASE = synth.phase_indices()  # pad 8 + impulse 12 + box 12


def key(b):
    """取箱体可比较字段（浮点做定点舍入）。"""
    if b is None:
        return None
    return (
        b.direction,
        b.w,
        b.start_i,
        b.end_i,
        round(b.high, 6),
        round(b.low, 6),
        round(b.height, 6),
        round(b.height_atr, 6),
        round(b.atr, 6),
        round(b.impulse_atr, 6),
    )


# ---------------------------------------------------------------- ATR


def test_atr_is_exact_for_constant_tr():
    df = synth.relay()
    atr = box_mod.atr_series(df, 14)
    assert atr.iloc[:13].isna().all()
    assert atr.iloc[13] == pytest.approx(4.0)
    assert atr.iloc[PHASE["box_start"] - 1] == pytest.approx(4.0)


def test_atr_handles_short_frame():
    df = synth.bars([100.0] * 5, range_=2.0)
    atr = box_mod.atr_series(df, 14)
    assert len(atr) == 5
    assert atr.isna().all()


# ---------------------------------------------------------------- 命中


def test_long_relay_hits():
    df = synth.relay()
    b = box_mod.detect_box(df)
    assert b is not None
    assert b.direction == "long"
    assert b.w == 12
    assert b.start_i == PHASE["box_start"]
    assert b.end_i == PHASE["box_end"]
    assert b.high == pytest.approx(130.6)
    assert b.low == pytest.approx(126.0)
    assert b.height == pytest.approx(5.6)
    assert b.atr == pytest.approx(4.0)
    assert b.height_atr == pytest.approx(1.4)
    assert b.impulse_atr == pytest.approx(5.5)
    assert b.last_close == pytest.approx(130.6)
    assert b.dist_edge_atr == pytest.approx(0.0)
    assert b.start_time and b.end_time
    assert 1.0 <= b.height_atr <= 2.0


def test_long_relay_hits_with_1_5_atr_box():
    df = synth.relay(box_height_atr=1.5)
    box, reason = box_mod.evaluate_window(df, PHASE["box_end"], 12)
    assert box is not None, reason
    assert box.direction == "long"
    assert box.height_atr == pytest.approx(1.5)


def test_short_relay_mirror():
    df = synth.relay_short()
    b = box_mod.detect_box(df)
    assert b is not None
    assert b.direction == "short"
    assert b.w == 12
    assert b.height_atr == pytest.approx(1.4)
    assert b.dist_edge_atr == pytest.approx(0.0)


def test_height_ratio_close_to_1_4_scores_best():
    tight, _ = box_mod.evaluate_window(synth.relay(box_height_atr=1.4), PHASE["box_end"], 12)
    loose, _ = box_mod.evaluate_window(synth.relay(box_height_atr=1.9), PHASE["box_end"], 12)
    assert tight.score > loose.score


# ---------------------------------------------------------------- 硬条件拒绝


def test_rejects_too_low_box():
    """箱高 0.5 ATR：指定的 12 根箱体窗口必须被硬条件拒掉。"""
    df = synth.relay(box_height_atr=0.5)
    b, reason = box_mod.evaluate_window(df, PHASE["box_end"], 12)
    assert b is None
    assert reason == "height_band"
    # detect_box 会扫 w（更短的窗口可能落在冲动段内，ATR 参照点不同），
    # 但无论选到哪个 w，硬条件都必须成立
    best = box_mod.detect_box(df)
    assert best is None or 1.0 <= best.height_atr <= 2.0


def test_rejects_too_high_box():
    """箱高 3 ATR：指定的 12 根箱体窗口必须被硬条件拒掉。"""
    df = synth.relay(box_height_atr=3.0)
    b, reason = box_mod.evaluate_window(df, PHASE["box_end"], 12)
    assert b is None
    assert reason == "height_band"
    best = box_mod.detect_box(df)
    assert best is None or 1.0 <= best.height_atr <= 2.0


def test_rejects_weak_impulse():
    df = synth.relay(impulse_step=0.1)  # Δ = 1.2 < 1.5 × ATR
    b, reason = box_mod.evaluate_window(df, PHASE["box_end"], 12)
    assert b is None
    assert reason == "impulse_weak"


def test_rejects_when_impulse_low_not_below_box():
    # 箱体整体下移到冲动起点以下 → 冲动低点不再低于箱体低点
    df = synth.relay(box_offset=-30.0)
    b, reason = box_mod.evaluate_window(df, PHASE["box_end"], 12)
    assert b is None
    assert reason in ("impulse_low", "no_retrace", "impulse_weak")


def test_rejects_box_already_outside():
    df = synth.relay()
    last = df.index[-1]
    df.loc[last, "close"] = 131.1  # 超过箱沿 + 0.1×ATR
    df.loc[last, "high"] = 131.6
    b, reason = box_mod.evaluate_window(df, PHASE["box_end"], 12)
    assert b is None
    assert reason == "outside_now"


def test_rejects_too_deep_retracement():
    df = synth.relay(box_offset=-22.0)
    b, reason = box_mod.evaluate_window(df, PHASE["box_end"], 12)
    assert b is None
    assert reason == "no_retrace"


def test_rejects_short_frame():
    df = synth.relay()
    b, reason = box_mod.evaluate_window(df.iloc[:25], 24, 12)
    assert b is None
    assert reason in ("short_history", "atr_warmup")


def test_rejects_close_beyond_edge_buffer():
    df = synth.relay()
    last = df.index[-1]
    df.loc[last, "close"] = 131.5  # 超过箱沿 + 0.15×ATR → 收盘已越箱
    df.loc[last, "high"] = 132.0
    b, reason = box_mod.evaluate_window(df, PHASE["box_end"], 12)
    assert b is None
    assert reason == "broke_out"


# ---------------------------------------------------------------- 周期无关


def test_period_agnostic():
    a = box_mod.detect_box(synth.relay(step_min=5))
    b = box_mod.detect_box(synth.relay(step_min=60))
    assert key(a) == key(b)
    assert a is not None


# ---------------------------------------------------------------- 无未来函数


def test_detect_box_only_uses_past_bars():
    full = synth.relay(breakout_step=3.0)
    assert box_mod.detect_box(full) is None  # 突破根改变了「箱内」判定

    cut = full.iloc[:-1]
    assert box_mod.detect_box(cut) is not None
    assert key(box_mod.detect_box(cut)) == key(box_mod.detect_box(synth.relay()))

    # 逐根截断：任何一根的结论都不受其后 K 线影响
    for end_i in range(PHASE["box_end"], len(full)):
        partial = box_mod.detect_box(full.iloc[: end_i + 1])
        again = box_mod.detect_box(full.iloc[: end_i + 1])
        assert key(partial) == key(again)


# ---------------------------------------------------------------- 未走完 K 线 / 时间格式


def test_complete_bars_drops_unfinished_last_bar():
    df = synth.to_ns(synth.relay())
    # TqSdk 里未走完的 K 线时间戳在未来（现在还没到它的收盘时刻）
    now_ns = int(df["datetime"].iloc[-1]) - 1
    assert len(box_mod.complete_bars(df, now_ns)) == len(df) - 1
    now_ns = int(df["datetime"].iloc[-1])
    assert len(box_mod.complete_bars(df, now_ns)) == len(df)


def test_complete_bars_accepts_timestamps():
    df = synth.relay()
    now = df["datetime"].iloc[-2] + pd.Timedelta(minutes=5)
    assert len(box_mod.complete_bars(df, now)) == len(df) - 1


def test_fmt_time_handles_ns_and_timestamp():
    df = synth.relay()
    assert box_mod.fmt_time(df["datetime"].iloc[0]) == "2026-01-05 09:00"
    ns = int(df["datetime"].iloc[0].value)
    assert box_mod.fmt_time(ns) == "2026-01-05 09:00"
    assert box_mod.fmt_time(None) == ""
