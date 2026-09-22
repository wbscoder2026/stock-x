"""scan.py 的单测 —— 周期表 / CLI 校验 / 纯函数管线 / CSV。"""

from __future__ import annotations

import csv

import pytest

import scan
import synth
import universe

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


# ---------------------------------------------------------------- 周期表 / CLI


def test_period_table():
    assert universe.period_seconds("5") == 300
    assert universe.period_seconds("15") == 900
    assert universe.period_seconds("1d") == 86400
    assert universe.scan_bars("15") == 300
    assert universe.trend_seconds("15") == 86400
    assert universe.trend_seconds("1d") == 7 * 86400
    with pytest.raises(ValueError):
        universe.period_seconds("7")


def test_parse_args_defaults():
    args = scan.parse_args([])
    assert args.period == "15"
    assert args.only is None
    assert args.out is None
    assert args.watch == 0
    assert args.strict is False
    assert args.atr_lo == 1.0
    assert args.atr_hi == 2.0
    assert args.impulse_n == 12
    assert args.impulse_min_atr == 1.5
    assert args.w_min == 6
    assert args.w_max == 20
    assert args.break_buf_atr == 0.15


def test_parse_args_invalid_period_exits_nonzero():
    with pytest.raises(SystemExit) as err:
        scan.parse_args(["--period", "7"])
    assert err.value.code not in (0, None)


def test_main_without_credentials_exits_with_hint(monkeypatch, capsys, tmp_path):
    monkeypatch.delenv("TQ_USER", raising=False)
    monkeypatch.delenv("TQ_PASS", raising=False)
    code = scan.main(["--period", "15", "--only", "JM", "--out", str(tmp_path / "c.csv")])
    captured = capsys.readouterr()
    assert code != 0
    assert "TQ_USER" in captured.out + captured.err


# ---------------------------------------------------------------- 纯函数管线


def test_evaluate_symbol_hits():
    daily = synth.trending_daily(n=120, drift=0.5)
    period_df = synth.relay()

    row = scan.evaluate_symbol(universe.by_prefix("JM"), daily, period_df, period="15")

    assert row is not None
    assert list(row.keys()) == list(CSV_COLUMNS)
    assert row["prefix"] == "JM"
    assert row["name"] == "焦煤"
    assert row["symbol"] == "KQ.m@DCE.jm"
    assert row["trend"] == "多"
    assert row["bars"] == 12
    assert 1.0 <= row["height_atr"] <= 2.0
    assert row["box_start"] == "2026-01-05 14:00"
    assert row["box_end"] == "2026-01-05 16:45"
    assert row["last"] == pytest.approx(130.6)


def test_evaluate_symbol_rejects_range_market():
    daily = synth.flat_daily(120)
    assert scan.evaluate_symbol(universe.by_prefix("JM"), daily, synth.relay()) is None


def test_evaluate_symbol_rejects_direction_mismatch():
    daily = synth.trending_daily(n=120, drift=-0.5)
    assert scan.evaluate_symbol(universe.by_prefix("JM"), daily, synth.relay()) is None


def test_evaluate_symbol_rejects_thin_data():
    daily = synth.trending_daily(n=120, drift=0.5)
    assert scan.evaluate_symbol(universe.by_prefix("JM"), daily, synth.relay().iloc[:10]) is None


# ---------------------------------------------------------------- 输出


def make_row(height_atr, dist_edge_atr, prefix="JM"):
    return {
        "time": "2026-01-05 17:00:00",
        "period": "15",
        "prefix": prefix,
        "name": "焦煤",
        "symbol": "KQ.m@DCE.jm",
        "trend": "多",
        "box_high": 130.6,
        "box_low": 126.0,
        "height": 5.6,
        "height_atr": height_atr,
        "atr": 4.0,
        "impulse_atr": 5.5,
        "bars": 12,
        "last": 130.6,
        "dist_edge_atr": dist_edge_atr,
        "box_start": "2026-01-05 14:00",
        "box_end": "2026-01-05 16:45",
    }


def test_sort_rows_prefers_height_near_1_4_then_edge():
    rows = [
        make_row(1.60, 0.5, "A"),
        make_row(1.40, 0.9, "B"),
        make_row(1.45, 0.1, "C"),
    ]
    ordered = [r["prefix"] for r in scan.sort_rows(rows)]
    assert ordered == ["B", "C", "A"]


def test_format_rows_contains_key_info():
    text = scan.format_rows([make_row(1.4, 0.2)])
    assert "JM" in text
    assert "多" in text
    assert "1.40" in text


def test_write_csv_roundtrip(tmp_path):
    path = tmp_path / "candidates.csv"
    scan.write_csv([make_row(1.4, 0.2)], str(path))

    with open(path, newline="", encoding="utf-8") as fh:
        reader = csv.DictReader(fh)
        assert tuple(reader.fieldnames or ()) == CSV_COLUMNS
        rows = list(reader)
    assert len(rows) == 1
    assert rows[0]["prefix"] == "JM"
    assert float(rows[0]["height_atr"]) == pytest.approx(1.4)


def test_write_csv_no_path_is_noop():
    assert scan.write_csv([make_row(1.4, 0.2)], None) is None


def test_new_rows_only_reports_unseen_symbols():
    seen = set()
    rows = [make_row(1.4, 0.2, "JM"), make_row(1.5, 0.3, "RB")]
    assert [r["prefix"] for r in scan.new_rows(seen, rows)] == ["JM", "RB"]
    assert [r["prefix"] for r in scan.new_rows(seen, rows)] == []
    assert [r["prefix"] for r in scan.new_rows(seen, [make_row(1.4, 0.2, "CU")])] == ["CU"]


def test_scan_once_then_csv_end_to_end(tmp_path):
    """整链路（订阅 buffer → 流动性过滤 → 趋势/箱体 → CSV）走一遍。"""
    variety = universe.by_prefix("JM")
    daily = synth.trending_daily(n=120, drift=0.5)
    daily["volume"] = 1e6  # 过流动性门槛
    period_df = synth.relay(volume=1e6)
    symbol = universe.main_symbol(variety)

    args = scan.parse_args(["--period", "15", "--only", "JM", "--out", str(tmp_path / "c.csv")])
    params = scan.build_params(args)

    rows = scan.scan_once([variety], {symbol: daily}, {symbol: period_df}, args, params)
    assert len(rows) == 1

    scan.write_csv(rows, args.out)
    with open(args.out, newline="", encoding="utf-8") as fh:
        csv_rows = list(csv.DictReader(fh))

    assert len(csv_rows) == 1
    row = csv_rows[0]
    assert 1.0 <= float(row["height_atr"]) <= 2.0
    assert row["trend"] in ("多", "空")
    assert row["period"] == "15"
    assert row["bars"] == "12"
