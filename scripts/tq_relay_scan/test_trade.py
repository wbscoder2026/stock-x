"""trade.py 的单测 —— 突破入场 / 持有出场 / 换月保护 / 无未来函数。"""

from __future__ import annotations

import pandas as pd
import pytest

import synth
import trade

PERIOD = "15"
ENTRY_CLOSE = 133.6  # 箱沿 130.6 + 突破缓冲后的一根收盘


def trend_list(df, value="long"):
    return [value] * len(df)


def run(df, trends=None, **params):
    if trends is None:
        trends = trend_list(df)
    return trade.run_symbol("JM", PERIOD, df, trends, trade.TradeParams(**params))


def append_bar(df, **overrides):
    row = {
        "datetime": df["datetime"].iloc[-1] + pd.Timedelta(minutes=15),
        "open": float(df["close"].iloc[-1]),
        "high": float(df["close"].iloc[-1]) + 0.5,
        "low": float(df["close"].iloc[-1]) - 0.5,
        "close": float(df["close"].iloc[-1]),
        "volume": 1000.0,
        "open_interest": 100000.0,
    }
    row.update(overrides)
    return pd.concat([df, pd.DataFrame([row])], ignore_index=True)


# ---------------------------------------------------------------- 入场 / 持有


def test_long_breakout_enters_and_holds():
    df = synth.relay(breakout_step=3.0, post_bars=3, post_step=2.0)
    res = run(df, hold_bars=3)

    assert len(res.trades) == 1
    t = res.trades[0]
    assert t.direction == "long"
    assert t.trend == "long"
    assert t.reason == "hold"
    assert t.entry == pytest.approx(ENTRY_CLOSE)
    assert t.exit == pytest.approx(139.6)
    assert t.ret == pytest.approx((139.6 - ENTRY_CLOSE) / ENTRY_CLOSE)
    assert t.correct is True
    assert t.box_start == "2026-01-05 14:00"
    assert t.entry_i == 32
    assert t.exit_i == 35
    assert res.skip_against_trend == 0
    assert res.roll_gaps == 0
    assert res.bars == len(df)


def test_short_breakdown_enters_and_holds():
    df = synth.relay_short(breakout_step=3.0, post_bars=3, post_step=2.0)
    res = run(df, trends=trend_list(df, "short"), hold_bars=3)

    assert len(res.trades) == 1
    t = res.trades[0]
    assert t.direction == "short"
    assert t.entry == pytest.approx(200.0 - ENTRY_CLOSE)
    assert t.exit == pytest.approx(200.0 - 139.6)
    assert t.ret > 0
    assert t.correct is True


def test_breakout_against_trend_is_skipped():
    df = synth.relay(breakout_step=3.0)
    res = run(df, trends=trend_list(df, "short"))
    assert res.trades == []
    assert res.skip_against_trend == 1


def test_no_daily_trend_no_trade():
    df = synth.relay(breakout_step=3.0)
    res = run(df, trends=[None] * len(df))
    assert res.trades == []


# ---------------------------------------------------------------- 出场


def test_stop_loss_exits_at_stop():
    df = synth.relay(breakout_step=3.0, post_bars=2, post_step=-5.0)
    res = run(df, hold_bars=0, stop_atr=1.0, tp_atr=0.0)

    assert len(res.trades) == 1
    t = res.trades[0]
    assert t.reason == "stop"
    assert t.exit < t.entry
    assert t.ret < 0
    assert t.correct is False


def test_take_profit_exits_at_target():
    df = synth.relay(breakout_step=3.0, post_bars=2, post_step=12.0)
    res = run(df, hold_bars=0, stop_atr=0.0, tp_atr=2.0)

    assert len(res.trades) == 1
    t = res.trades[0]
    assert t.reason == "tp"
    assert t.exit > t.entry
    assert t.correct is True


def test_hold_bars_zero_without_stop_or_tp_is_invalid():
    df = synth.relay(breakout_step=3.0, post_bars=3)
    with pytest.raises(ValueError):
        run(df, hold_bars=0, stop_atr=0.0, tp_atr=0.0)


def test_open_position_closed_at_data_end():
    df = synth.relay(breakout_step=3.0)
    res = run(df, hold_bars=8)
    assert len(res.trades) == 1
    t = res.trades[0]
    assert t.reason == "eod"
    assert t.exit == pytest.approx(ENTRY_CLOSE)


def test_fee_reduces_return():
    df = synth.relay(breakout_step=3.0, post_bars=3, post_step=2.0)
    no_fee = run(df, hold_bars=3).trades[0]
    with_fee = run(df, hold_bars=3, fee_bps=100.0).trades[0]
    assert no_fee.ret - with_fee.ret == pytest.approx(2 * 100.0 / 10000.0)


# ---------------------------------------------------------------- 换月保护


def test_roll_gap_closes_position_at_prev_close():
    df = synth.relay(breakout_step=3.0, post_bars=3, post_step=2.0)
    df = append_bar(df, open=170.0, high=171.0, low=169.0, close=170.0)
    res = run(df, hold_bars=8)

    assert res.roll_gaps == 1
    t = res.trades[0]
    assert t.reason == "roll"
    assert t.exit == pytest.approx(139.6)  # 前一根收盘平，避免假盈亏
    assert t.ret > 0


def test_no_entry_on_roll_gap_bar():
    df = synth.relay()
    df = append_bar(df, open=170.0, high=171.0, low=132.0, close=ENTRY_CLOSE)
    res = run(df, hold_bars=8)
    assert res.trades == []
    assert res.roll_gaps == 1


# ---------------------------------------------------------------- 无未来函数 / 单仓


def test_entries_do_not_change_with_more_future_bars():
    full = synth.relay(breakout_step=3.0, post_bars=3, post_step=2.0)
    full_res = run(full, hold_bars=3)
    assert [t.entry for t in full_res.trades] == [pytest.approx(ENTRY_CLOSE)]

    for k in range(33, len(full) + 1):
        part = run(full.iloc[:k], hold_bars=3)
        assert len(part.trades) == 1
        assert part.trades[0].entry == pytest.approx(ENTRY_CLOSE)
        assert part.trades[0].entry_time == full_res.trades[0].entry_time


def test_single_position_at_a_time():
    df = synth.relay(breakout_step=3.0, post_bars=6, post_step=2.0)
    res = run(df, hold_bars=2)
    for prev, nxt in zip(res.trades, res.trades[1:]):
        assert prev.exit_i < nxt.entry_i


# ---------------------------------------------------------------- 汇总


def make_trade(ret, correct, reason="hold"):
    return trade.Trade(
        symbol="JM",
        period=PERIOD,
        direction="long",
        trend="long",
        box_start="2026-01-05 14:00",
        entry_time="2026-01-05 17:00",
        entry=100.0,
        exit_time="2026-01-05 18:00",
        exit=100.0 * (1 + ret),
        ret=ret,
        correct=correct,
        reason=reason,
        entry_i=0,
        exit_i=1,
    )


def test_summarize_metrics():
    trades = [make_trade(0.10, True), make_trade(-0.05, False), make_trade(0.02, True)]
    s = trade.summarize(trades, PERIOD, skip_against_trend=4)

    assert s["trades"] == 3
    assert s["win_rate"] == pytest.approx(2 / 3)
    assert s["avg_return"] == pytest.approx((0.10 - 0.05 + 0.02) / 3)
    assert s["total_return"] == pytest.approx(0.07)
    assert s["avg_win"] == pytest.approx((0.10 + 0.02) / 2)
    assert s["avg_loss"] == pytest.approx(-0.05)
    assert s["profit_factor"] == pytest.approx(0.12 / 0.05)
    assert s["max_dd"] == pytest.approx(0.05)
    assert s["skip_against_trend"] == 4


def test_summarize_profitability_flags():
    """总收益 > 0 才算这套参数赚钱；盈亏比提高后可以「胜率低但仍盈利」。"""
    win_rate_low_but_profitable = [make_trade(0.30, True), make_trade(-0.10, False), make_trade(-0.10, False)]
    s = trade.summarize(win_rate_low_but_profitable, PERIOD)
    assert s["win_rate"] == pytest.approx(1 / 3)
    assert s["profit_factor"] == pytest.approx(0.30 / 0.20)
    assert s["total_return"] == pytest.approx(0.10)


def test_summarize_without_trades():
    s = trade.summarize([], PERIOD, skip_against_trend=2)
    assert s["trades"] == 0
    assert s["win_rate"] == 0.0
    assert s["avg_return"] == 0.0
    assert s["total_return"] == 0.0
    assert s["profit_factor"] == 0.0
    assert s["max_dd"] == 0.0


def test_correct_matches_return_sign():
    df = synth.relay(breakout_step=3.0, post_bars=6, post_step=2.0)
    res = run(df, hold_bars=2)
    assert res.trades
    for t in res.trades:
        assert t.correct == (t.ret > 0)


def test_detail_rows_match_trade_count():
    df = synth.relay(breakout_step=3.0, post_bars=6, post_step=2.0)
    res = run(df, hold_bars=2)
    rows = trade.trade_rows(res.trades)
    assert len(rows) == len(res.trades)
    assert set(rows[0].keys()) == {
        "symbol",
        "period",
        "trend",
        "box_start",
        "entry_time",
        "entry",
        "exit_time",
        "exit",
        "return",
        "correct",
        "reason",
    }
