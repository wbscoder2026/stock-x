"""universe.py 的单测 —— 品种池静态表 + 流动性过滤。"""

from __future__ import annotations

import pytest

import synth
import universe
from universe import UnknownPrefixError

# 与 internal/futures/contracts.go 对齐的品种清单（prefix），默认排除国债 T/TF/TS
GO_DCE = ["JM", "J", "I", "M", "Y", "A", "B", "C", "CS", "P", "L", "V", "PP", "EG", "EB", "PG", "JD", "LH", "LG", "RR"]
GO_SHFE = ["RB", "HC", "AU", "AG", "CU", "AL", "ZN", "PB", "NI", "SN", "SS", "FU", "BU", "RU", "SP", "AO"]
GO_INE = ["SC", "LU", "NR"]
GO_CZCE = ["TA", "MA", "OI", "RM", "SR", "CF", "ZC", "FG", "SA", "UR", "PF", "PK", "AP", "CJ", "SH", "PX", "SF", "SM"]
GO_GFEX = ["SI", "LC", "PS"]
GO_CFFEX = ["IF", "IH", "IC", "IM"]
GO_ALL = GO_DCE + GO_SHFE + GO_INE + GO_CZCE + GO_GFEX + GO_CFFEX
TREASURY = ["T", "TF", "TS"]


def test_prefixes_aligned_with_go_contracts():
    assert universe.prefixes() == GO_ALL


def test_unique_prefix_and_symbol():
    assert len(set(universe.prefixes())) == len(GO_ALL)
    symbols = [universe.main_symbol(v) for v in universe.all_varieties()]
    assert len(set(symbols)) == len(GO_ALL)


def test_treasury_excluded():
    for prefix in TREASURY:
        with pytest.raises(UnknownPrefixError):
            universe.by_prefix(prefix)


def test_main_symbol_format():
    assert universe.main_symbol(universe.by_prefix("JM")) == "KQ.m@DCE.jm"
    assert universe.main_symbol(universe.by_prefix("AU")) == "KQ.m@SHFE.au"
    assert universe.main_symbol(universe.by_prefix("SC")) == "KQ.m@INE.sc"
    assert universe.main_symbol(universe.by_prefix("TA")) == "KQ.m@CZCE.TA"
    assert universe.main_symbol(universe.by_prefix("LC")) == "KQ.m@GFEX.lc"
    assert universe.main_symbol(universe.by_prefix("IF")) == "KQ.m@CFFEX.IF"


def test_exchange_and_code_case():
    assert universe.by_prefix("NR").exchange == "INE"
    assert universe.by_prefix("LU").exchange == "INE"
    assert universe.by_prefix("PP").code == "pp"
    assert universe.by_prefix("PF").code == "PF"


def test_name_is_chinese():
    assert universe.by_prefix("JM").name == "焦煤"
    assert universe.by_prefix("RB").name == "螺纹钢"


def test_select_all_when_empty():
    assert [v.prefix for v in universe.select(None)] == GO_ALL
    assert [v.prefix for v in universe.select("")] == GO_ALL


def test_select_by_prefix_list_keeps_order_and_case():
    got = universe.select("jm,rb, CU")
    assert [v.prefix for v in got] == ["JM", "RB", "CU"]


def test_select_unknown_prefix_raises():
    with pytest.raises(UnknownPrefixError) as err:
        universe.select("JM,XX")
    assert "XX" in str(err.value)


def test_liquidity_pass():
    df = synth.bars([1000.0] * 30, range_=10.0, volume=1e6, open_interest=200000.0)
    ok, reason = universe.liquidity_ok(df, universe.by_prefix("RB"))
    assert ok is True
    assert reason == ""


def test_liquidity_rejects_zero_volume():
    df = synth.bars([3000.0] * 30, range_=10.0, volume=0.0)
    ok, reason = universe.liquidity_ok(df, universe.by_prefix("RB"))
    assert ok is False
    assert reason == "zero_volume"


def test_liquidity_rejects_low_turnover():
    # 收盘 100 × 1000 手 × 乘数 10 = 100 万，远低于 5 亿
    df = synth.bars([100.0] * 30, range_=1.0, volume=1000.0)
    ok, reason = universe.liquidity_ok(df, universe.by_prefix("RB"))
    assert ok is False
    assert reason == "low_turnover"


def test_liquidity_rejects_low_open_interest():
    df = synth.bars([3000.0] * 30, range_=10.0, volume=1e6, open_interest=10.0)
    ok, reason = universe.liquidity_ok(df, universe.by_prefix("RB"), min_oi=1000.0)
    assert ok is False
    assert reason == "low_oi"


def test_liquidity_rejects_empty_or_missing_columns():
    ok, reason = universe.liquidity_ok(None, universe.by_prefix("RB"))
    assert ok is False and reason == "no_data"

    empty = synth.bars([], range_=1.0)
    ok, reason = universe.liquidity_ok(empty, universe.by_prefix("RB"))
    assert ok is False and reason == "no_data"

    df = synth.bars([3000.0] * 30, range_=10.0, volume=1e6).drop(columns=["volume"])
    ok, reason = universe.liquidity_ok(df, universe.by_prefix("RB"))
    assert ok is False and reason == "no_data"


def test_liquidity_uses_last_bar_turnover():
    closes = [3000.0] * 29 + [3000.0]
    df = synth.bars(closes, range_=10.0, volume=1e6)
    df.loc[df.index[-1], "volume"] = 0.0
    ok, reason = universe.liquidity_ok(df, universe.by_prefix("RB"))
    assert ok is False
    assert reason in ("low_turnover", "zero_volume")
