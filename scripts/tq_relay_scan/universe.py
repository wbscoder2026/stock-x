"""品种池 + 交易所映射（静态表，禁止运行时爬网页）。

与 ``internal/futures/contracts.go`` 的商品/股指主力对齐，默认排除国债 ``T/TF/TS``。
TqSdk 主连标的形如 ``KQ.m@DCE.jm``：小写交易所（DCE/SHFE/INE/GFEX）用大写代码，
郑商所/中金所原样大写。
"""

from __future__ import annotations

from typing import List, NamedTuple, Optional, Sequence, Tuple

import pandas as pd

DCE, SHFE, INE, CZCE, GFEX, CFFEX = "DCE", "SHFE", "INE", "CZCE", "GFEX", "CFFEX"

#: 国债，明确排除（成交清淡且不受本形态驱动）
EXCLUDED_TREASURY: Tuple[str, ...] = ("T", "TF", "TS")

#: 商品默认最低成交额（元）
DEFAULT_MIN_TURNOVER = 5e8

# ---------------------------------------------------------------- 周期表（静态）

#: ``--period`` → 秒
PERIOD_SECONDS: dict = {"5": 300, "15": 900, "30": 1800, "60": 3600, "120": 7200, "1d": 86400}

#: 扫描默认订阅根数
SCAN_BARS: dict = {"5": 400, "15": 300, "30": 250, "60": 200, "120": 160, "1d": 120}

#: 回测建议根数
BACKTEST_BARS: dict = {"5": 8000, "15": 4000, "30": 2500, "60": 2000, "120": 1500, "1d": 800}

#: 日线（大趋势）订阅根数
TREND_BARS: dict = {"1d": 120}

#: 大趋势默认用日线；``--period 1d`` 时箱体就是日线，趋势改用周线
DEFAULT_TREND_SECONDS = 86400
WEEKLY_SECONDS = 7 * 86400


def period_seconds(period: str) -> int:
    """``--period`` → 秒；非法值抛 ``ValueError``。"""
    key = str(period)
    if key not in PERIOD_SECONDS:
        raise ValueError(
            "非法 --period %s（可选：%s）" % (period, "/".join(PERIOD_SECONDS))
        )
    return PERIOD_SECONDS[key]


def scan_bars(period: str) -> int:
    return SCAN_BARS[str(period)]


def backtest_bars(period: str) -> int:
    return BACKTEST_BARS[str(period)]


def trend_seconds(period: str) -> int:
    """大趋势的订阅周期：``1d`` 时用周线，避免同周期自相关。"""
    return WEEKLY_SECONDS if str(period) == "1d" else DEFAULT_TREND_SECONDS


def trend_bars(period: str) -> int:
    """大趋势的订阅根数（周线按 1/5 根数取）。"""
    if str(period) == "1d":
        return TREND_BARS["1d"]
    return 120


class UnknownPrefixError(ValueError):
    """``--only`` 里出现静态表没有的品种。"""


class Variety(NamedTuple):
    prefix: str  # 大写品种码，如 JM
    name: str  # 中文名
    exchange: str  # DCE / SHFE / INE / CZCE / GFEX / CFFEX
    code: str  # TqSdk 合约代码（大小写敏感）
    multiplier: float  # 合约乘数（近似），只用于成交额估算


# (prefix, 中文名, 交易所, TqSdk 代码, 合约乘数)
# 顺序即输出顺序：DCE → SHFE → INE → CZCE → GFEX → CFFEX
_TABLE: Tuple[Tuple[str, str, str, str, float], ...] = (
    # 大商所
    ("JM", "焦煤", DCE, "jm", 60),
    ("J", "焦炭", DCE, "j", 100),
    ("I", "铁矿石", DCE, "i", 100),
    ("M", "豆粕", DCE, "m", 10),
    ("Y", "豆油", DCE, "y", 10),
    ("A", "豆一", DCE, "a", 10),
    ("B", "豆二", DCE, "b", 10),
    ("C", "玉米", DCE, "c", 10),
    ("CS", "淀粉", DCE, "cs", 10),
    ("P", "棕榈油", DCE, "p", 10),
    ("L", "塑料", DCE, "l", 5),
    ("V", "PVC", DCE, "v", 5),
    ("PP", "PP", DCE, "pp", 5),
    ("EG", "乙二醇", DCE, "eg", 10),
    ("EB", "苯乙烯", DCE, "eb", 5),
    ("PG", "液化气", DCE, "pg", 20),
    ("JD", "鸡蛋", DCE, "jd", 10),
    ("LH", "生猪", DCE, "lh", 16),
    ("LG", "原木", DCE, "lg", 90),
    ("RR", "粳米", DCE, "rr", 10),
    # 上期所
    ("RB", "螺纹钢", SHFE, "rb", 10),
    ("HC", "热卷", SHFE, "hc", 10),
    ("AU", "黄金", SHFE, "au", 1000),
    ("AG", "白银", SHFE, "ag", 15),
    ("CU", "铜", SHFE, "cu", 5),
    ("AL", "铝", SHFE, "al", 5),
    ("ZN", "锌", SHFE, "zn", 5),
    ("PB", "铅", SHFE, "pb", 5),
    ("NI", "镍", SHFE, "ni", 1),
    ("SN", "锡", SHFE, "sn", 1),
    ("SS", "不锈钢", SHFE, "ss", 5),
    ("FU", "燃油", SHFE, "fu", 10),
    ("BU", "沥青", SHFE, "bu", 10),
    ("RU", "橡胶", SHFE, "ru", 10),
    ("SP", "纸浆", SHFE, "sp", 10),
    ("AO", "氧化铝", SHFE, "ao", 20),
    # 上期能源
    ("SC", "原油", INE, "sc", 1000),
    ("LU", "低硫燃油", INE, "lu", 10),
    ("NR", "20号胶", INE, "nr", 10),
    # 郑商所
    ("TA", "PTA", CZCE, "TA", 5),
    ("MA", "甲醇", CZCE, "MA", 10),
    ("OI", "菜油", CZCE, "OI", 10),
    ("RM", "菜粕", CZCE, "RM", 10),
    ("SR", "白糖", CZCE, "SR", 10),
    ("CF", "棉花", CZCE, "CF", 5),
    ("ZC", "动力煤", CZCE, "ZC", 100),
    ("FG", "玻璃", CZCE, "FG", 20),
    ("SA", "纯碱", CZCE, "SA", 20),
    ("UR", "尿素", CZCE, "UR", 20),
    ("PF", "短纤", CZCE, "PF", 5),
    ("PK", "花生", CZCE, "PK", 5),
    ("AP", "苹果", CZCE, "AP", 10),
    ("CJ", "红枣", CZCE, "CJ", 5),
    ("SH", "烧碱", CZCE, "SH", 30),
    ("PX", "对二甲苯", CZCE, "PX", 5),
    ("SF", "硅铁", CZCE, "SF", 5),
    ("SM", "锰硅", CZCE, "SM", 5),
    # 广期所
    ("SI", "工业硅", GFEX, "si", 5),
    ("LC", "碳酸锂", GFEX, "lc", 1),
    ("PS", "多晶硅", GFEX, "ps", 3),
    # 中金所（股指）
    ("IF", "沪深300", CFFEX, "IF", 300),
    ("IH", "上证50", CFFEX, "IH", 300),
    ("IC", "中证500", CFFEX, "IC", 200),
    ("IM", "中证1000", CFFEX, "IM", 200),
)

VARIETIES: Tuple[Variety, ...] = tuple(Variety(*row) for row in _TABLE)

_BY_PREFIX = {v.prefix: v for v in VARIETIES}


def all_varieties() -> List[Variety]:
    """全部品种（副本）。"""
    return list(VARIETIES)


def prefixes() -> List[str]:
    """全部品种码（顺序即输出顺序）。"""
    return [v.prefix for v in VARIETIES]


def by_prefix(prefix: str) -> Variety:
    """按品种码取品种，未知则 ``UnknownPrefixError``。"""
    p = (prefix or "").strip().upper()
    try:
        return _BY_PREFIX[p]
    except KeyError:
        raise UnknownPrefixError("未知品种 %s（可选：%s）" % (prefix, ",".join(prefixes()))) from None


def main_symbol(variety: Variety) -> str:
    """TqSdk 主力连续标的，如 ``KQ.m@DCE.jm``。"""
    return "KQ.m@%s.%s" % (variety.exchange, variety.code)


def select(only: Optional[str] = None) -> List[Variety]:
    """``--only JM,RB`` 过滤；空/None → 全部。保持用户输入顺序。"""
    if only is None or not only.strip():
        return all_varieties()
    out: List[Variety] = []
    for raw in only.split(","):
        token = raw.strip()
        if token:
            out.append(by_prefix(token))
    return out


def turnover_proxy(df: pd.DataFrame, variety: Variety) -> float:
    """最后一根成交额估算（元）= 收盘 × 手数 × 合约乘数。"""
    close = float(df["close"].iloc[-1])
    volume = float(df["volume"].iloc[-1])
    return close * volume * float(variety.multiplier)


def liquidity_ok(
    df: Optional[pd.DataFrame],
    variety: Variety,
    *,
    min_turnover: float = DEFAULT_MIN_TURNOVER,
    min_oi: float = 0.0,
    vol_window: int = 5,
) -> Tuple[bool, str]:
    """日线最后一根的流动性过滤。

    返回 ``(是否通过, 原因)``，通过时原因为空串。
    原因取值：``no_data`` / ``zero_volume`` / ``low_turnover`` / ``low_oi``。
    """
    if df is None or len(df) == 0:
        return False, "no_data"
    for col in ("close", "volume"):
        if col not in df.columns:
            return False, "no_data"

    vol = df["volume"].iloc[-vol_window:]
    if len(vol) == 0 or float(vol.mean()) <= 0:
        return False, "zero_volume"
    if turnover_proxy(df, variety) < min_turnover:
        return False, "low_turnover"
    if min_oi > 0 and "open_interest" in df.columns:
        if float(df["open_interest"].iloc[-1]) < min_oi:
            return False, "low_oi"
    return True, ""


def drop_illiquid(
    items: Sequence[Tuple[Variety, Optional[pd.DataFrame]]],
    *,
    min_turnover: float = DEFAULT_MIN_TURNOVER,
    min_oi: float = 0.0,
) -> Tuple[List[Variety], List[Tuple[str, str]]]:
    """批量过滤，返回 ``(保留品种, [(prefix, 原因)...])``。"""
    kept: List[Variety] = []
    dropped: List[Tuple[str, str]] = []
    for variety, df in items:
        ok, reason = liquidity_ok(df, variety, min_turnover=min_turnover, min_oi=min_oi)
        if ok:
            kept.append(variety)
        else:
            dropped.append((variety.prefix, reason))
    return kept, dropped
