"""backtest.py 的单测 —— CLI 校验 / 趋势对齐 / 逐品种 walk-forward / 盈亏比 / 输出。"""

from __future__ import annotations

import csv

import pandas as pd
import pytest

import backtest
import synth
import trade
import universe


def push_bar(df: pd.DataFrame, close: float, high=None, low=None) -> pd.DataFrame:
    row = {
        "datetime": df["datetime"].iloc[-1] + pd.Timedelta(minutes=15),
        "open": float(df["close"].iloc[-1]),
        "high": float(close + 0.5) if high is None else float(high),
        "low": float(close - 0.5) if low is None else float(low),
        "close": float(close),
        "volume": 1000.0,
        "open_interest": 100000.0,
    }
    return pd.concat([df, pd.DataFrame([row])], ignore_index=True)


def make_items():
    """(品种, 日线, 周期K线) 三元组：JM 可交易，RB 数据太短。"""
    good_period = synth.relay(breakout_step=3.0, post_bars=3, post_step=2.0)
    return [
        (
            universe.by_prefix("JM"),
            synth.trending_daily(n=120, drift=0.5),
            good_period,
        ),
        (
            universe.by_prefix("RB"),
            synth.trending_daily(n=120, drift=0.5),
            synth.bars([100.0] * 10, range_=4.0),
        ),
    ]


# ---------------------------------------------------------------- CLI


def test_parse_args_defaults():
    args = backtest.parse_args(["--from", "2025-01-01", "--to", "2025-06-01"])
    assert args.period == "15"
    assert args.from_ == "2025-01-01"
    assert args.to == "2025-06-01"
    assert args.hold_bars == 8
    assert args.stop_atr == 1.0
    assert args.tp_atr == 2.0
    assert args.rr is None
    assert args.sweep_rr is None
    assert args.fee_bps == 0.0
    assert args.trades is False


# ---------------------------------------------------------------- 盈亏比（rr）


def test_parse_ratios_and_errors():
    assert backtest.parse_ratios("1, 1.5 ,2") == [1.0, 1.5, 2.0]
    assert backtest.parse_ratios("3") == [3.0]
    with pytest.raises(ValueError):
        backtest.parse_ratios("1,abc")
    with pytest.raises(ValueError):
        backtest.parse_ratios("")


def test_rr_overrides_tp_atr():
    args = backtest.parse_args(
        [
            "--from", "2025-01-01",
            "--to", "2025-02-01",
            "--rr", "3",
            "--stop-atr", "0.5",
            "--tp-atr", "9",
        ]
    )
    tp, _box = backtest.build_params(args)
    assert tp.stop_atr == pytest.approx(0.5)
    assert tp.tp_atr == pytest.approx(1.5)  # 盈亏比 3 × 止损 0.5


def test_rr_must_be_positive():
    args = backtest.parse_args(["--from", "2025-01-01", "--to", "2025-02-01", "--rr", "0"])
    with pytest.raises(ValueError):
        backtest.build_params(args)


def rr_frame() -> pd.DataFrame:
    """入场后先冲到 +1.5 ATR（不够 2ATR 止盈），再砸破 -1 ATR 止损。"""
    df = synth.relay(breakout_step=3.0)
    df = push_bar(df, 138.6, high=139.1, low=138.1)
    return push_bar(df, 124.0, high=124.5, low=123.5)


def rr_items():
    return [(universe.by_prefix("JM"), synth.trending_daily(n=120, drift=0.5), rr_frame())]


def test_sweep_ratios_low_rr_takes_profit_high_rr_stops_out():
    base = trade.TradeParams(hold_bars=0, stop_atr=1.0)
    low = backtest.sweep_ratios(rr_items(), [1.0], period="15", base_params=base)[0]
    high = backtest.sweep_ratios(rr_items(), [2.0], period="15", base_params=base)[0]

    assert low["rr"] == 1.0
    assert low["win_rate"] == 1.0
    assert low["total_return"] > 0

    assert high["rr"] == 2.0
    assert high["win_rate"] == 0.0
    assert high["total_return"] < 0


def test_sweep_ratios_shape_and_table():
    base = trade.TradeParams(hold_bars=0, stop_atr=1.0)
    rows = backtest.sweep_ratios(
        rr_items(), [0.5, 1.0, 2.0, 3.0], period="15", base_params=base
    )

    assert [r["rr"] for r in rows] == [0.5, 1.0, 2.0, 3.0]
    for row in rows:
        assert set(trade.SUMMARY_COLUMNS) <= set(row.keys())
        assert row["trades"] >= 1

    text = backtest.format_sweep(rows)
    assert "rr" in text
    assert "win_rate" in text
    assert "profit_factor" in text
    assert "total_return" in text


def test_sweep_detail_rows_carry_rr():
    base = trade.TradeParams(hold_bars=0, stop_atr=1.0)
    rows = backtest.sweep_detail_rows(
        rr_items(), [1.0, 2.0], period="15", base_params=base
    )
    assert {r["rr"] for r in rows} == {1.0, 2.0}
    assert all("return" in r and "reason" in r for r in rows)


def test_parse_args_requires_range():
    with pytest.raises(SystemExit) as err:
        backtest.parse_args(["--period", "15"])
    assert err.value.code not in (0, None)


def test_parse_args_invalid_period_exits_nonzero():
    with pytest.raises(SystemExit) as err:
        backtest.parse_args(["--period", "9", "--from", "2025-01-01", "--to", "2025-02-01"])
    assert err.value.code not in (0, None)


def test_main_rejects_disabled_exit_rules(monkeypatch, capsys):
    monkeypatch.setenv("TQ_USER", "u")
    monkeypatch.setenv("TQ_PASS", "p")
    code = backtest.main(
        [
            "--period", "15",
            "--from", "2025-01-01",
            "--to", "2025-02-01",
            "--hold-bars", "0",
            "--stop-atr", "0",
            "--tp-atr", "0",
            "--only", "JM",
        ]
    )
    captured = capsys.readouterr()
    assert code != 0
    assert "hold" in (captured.out + captured.err)


def test_main_without_credentials_exits_with_hint(monkeypatch, capsys):
    monkeypatch.delenv("TQ_USER", raising=False)
    monkeypatch.delenv("TQ_PASS", raising=False)
    code = backtest.main(["--period", "15", "--from", "2025-01-01", "--to", "2025-02-01", "--only", "JM"])
    captured = capsys.readouterr()
    assert code != 0
    assert "TQ_USER" in captured.out + captured.err


# ---------------------------------------------------------------- 趋势对齐


def test_align_trends_maps_period_bars_to_closed_daily():
    daily = synth.trending_daily(n=120, drift=0.5)
    period_df = synth.relay(step_min=15, start="2025-04-01 09:00")

    trends = backtest.align_trends(period_df, daily)

    assert len(trends) == len(period_df)
    assert trends[-1] == "long"


def test_align_trends_short_and_insufficient():
    daily = synth.trending_daily(n=30, drift=-0.5)
    period_df = synth.relay(step_min=15, start="2025-01-20 09:00")
    trends = backtest.align_trends(period_df, daily)
    assert set(trends) == {None}


# ---------------------------------------------------------------- 逐品种 walk-forward


def test_run_frame_backtest_trades_and_skips():
    result = backtest.run_frame_backtest(
        make_items(), period="15", params=trade.TradeParams(hold_bars=3)
    )

    assert len(result.trades) == 1
    assert result.trades[0].symbol == "KQ.m@DCE.jm"
    assert result.trades[0].reason == "hold"
    assert result.skipped == [("RB", "short_history")]
    assert result.bars == {"JM": 36}  # 只统计真正参与回测的品种


def test_summary_matches_detail_rows():
    result = backtest.run_frame_backtest(make_items(), period="15")
    summary = trade.summarize(
        result.trades, "15", skip_against_trend=result.skip_against_trend
    )
    assert summary["trades"] == len(trade.trade_rows(result.trades)) == 1
    for t in result.trades:
        assert t.correct == (t.ret > 0)


def test_run_frame_backtest_is_robust_to_bad_frame():
    bad = synth.relay().drop(columns=["high"])
    items = [(universe.by_prefix("CU"), synth.trending_daily(n=120, drift=0.5), bad)]
    result = backtest.run_frame_backtest(items, period="15")
    assert result.trades == []
    assert result.skipped and result.skipped[0][0] == "CU"


def test_run_frame_backtest_respects_exit_params():
    items = make_items()
    result = backtest.run_frame_backtest(
        items, period="15", params=trade.TradeParams(hold_bars=0, stop_atr=1.0, tp_atr=2.0)
    )
    assert len(result.trades) == 1
    assert result.trades[0].reason in ("stop", "tp", "eod", "roll")


# ---------------------------------------------------------------- 输出


def test_write_detail_csv(tmp_path):
    result = backtest.run_frame_backtest(
        make_items(), period="15", params=trade.TradeParams(hold_bars=3)
    )
    path = tmp_path / "bt.csv"
    backtest.write_csv(trade.trade_rows(result.trades), str(path))

    with open(path, newline="", encoding="utf-8") as fh:
        reader = csv.DictReader(fh)
        assert tuple(reader.fieldnames or ()) == trade.TRADE_COLUMNS
        rows = list(reader)
    assert len(rows) == 1
    assert rows[0]["symbol"] == "KQ.m@DCE.jm"
    assert rows[0]["reason"] == "hold"


def test_format_summary_contains_metrics():
    result = backtest.run_frame_backtest(make_items(), period="15")
    summary = trade.summarize(result.trades, "15", result.skip_against_trend)
    text = backtest.format_summary(summary)
    assert "trades" in text
    assert "win_rate" in text
    assert "max_dd" in text
