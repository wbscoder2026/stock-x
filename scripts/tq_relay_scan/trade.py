"""突破入场 / 持有出场 + walk-forward（纯函数 + pandas，不依赖 TqApi）。

入场
----
箱体在 ``i`` 根确认存在（当时可见的 K 线），之后第一根**收盘**有效突破箱沿
（缓冲 ``0.15*ATR``）且方向与日线趋势同向 → 入场价 = 该根收盘。

* 反向突破：记 ``skip_against_trend``，不开仓
* 收盘离箱超过 ``invalidate_atr``×ATR 或根数超过 ``w_max + max_extra_bars``：箱失效
* 同一品种同时只持 1 笔

出场（先碰到先走，止损优先）
---------------------------
* 固定持有 ``--hold-bars``
* 止损 ``--stop-atr``（入场根 ATR）
* 止盈 ``--tp-atr``
* 换月跳空（``|open - prev_close| > 3*ATR``）当根不开仓，持仓按前收平（``roll``）

收益为价格相对变化，扣 ``2 × fee_bps``（第一版不算保证金杠杆）。
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Dict, List, Optional, Sequence

import numpy as np
import pandas as pd

from box import Box, BoxParams, atr_series, detect_box, fmt_time

ROLL_GAP_ATR = 3.0  # |open - prev_close| > 3×ATR 视为主连换月

LONG = "long"
SHORT = "short"


@dataclass
class TradeParams:
    hold_bars: int = 8
    stop_atr: float = 1.0
    tp_atr: float = 2.0
    fee_bps: float = 0.0
    max_extra_bars: int = 10
    invalidate_atr: float = 2.0

    def validate(self) -> None:
        """``hold_bars`` 关掉时必须保留止损或止盈，否则无从出场。"""
        if self.hold_bars <= 0 and self.stop_atr <= 0 and self.tp_atr <= 0:
            raise ValueError("hold-bars=0 时必须至少启用 stop-atr 或 tp-atr")


@dataclass
class Trade:
    symbol: str
    period: str
    direction: str  # long / short
    trend: str  # 入场时的日线趋势
    box_start: str
    entry_time: str
    entry: float
    exit_time: str
    exit: float
    ret: float
    correct: bool
    reason: str  # hold / stop / tp / roll / eod
    entry_i: int = 0
    exit_i: int = 0


@dataclass
class SymbolResult:
    symbol: str
    period: str
    trades: List[Trade] = field(default_factory=list)
    skip_against_trend: int = 0
    roll_gaps: int = 0
    bars: int = 0


@dataclass
class _Position:
    direction: str
    trend: str
    box_start: str
    entry: float
    entry_i: int
    entry_atr: float


def _is_roll_gap(df: pd.DataFrame, i: int, atr: pd.Series, multiple: float = ROLL_GAP_ATR) -> bool:
    """主连换月跳空：``|open - prev_close| > multiple × ATR``。"""
    if i <= 0:
        return False
    if "open" not in df.columns:
        return False
    a = float(atr.iloc[i]) if i < len(atr) else np.nan
    if not np.isfinite(a) or a <= 0:
        return False
    prev_close = float(df["close"].iloc[i - 1])
    open_price = float(df["open"].iloc[i])
    return abs(open_price - prev_close) > multiple * a


def run_symbol(
    symbol: str,
    period: str,
    df: pd.DataFrame,
    trends: Optional[Sequence[Optional[str]]],
    params: Optional[TradeParams] = None,
    box_params: Optional[BoxParams] = None,
    atr: Optional[pd.Series] = None,
) -> SymbolResult:
    """单品种 walk-forward 回测（第 i 根只用 <= i 的数据判定）。"""
    p = params or TradeParams()
    p.validate()
    bp = box_params or BoxParams()

    if df is None or len(df) == 0:
        return SymbolResult(symbol=symbol, period=period, bars=0)

    if atr is None:
        atr = atr_series(df, bp.atr_period)
    trend_seq: List[Optional[str]] = (
        list(trends) if trends is not None else [None] * len(df)
    )

    n = len(df)
    trades: List[Trade] = []
    skip_against = 0
    roll_gaps = 0
    position: Optional[_Position] = None
    pending: Optional[Dict[str, object]] = None

    for i in range(n):
        close = float(df["close"].iloc[i])
        high = float(df["high"].iloc[i])
        low = float(df["low"].iloc[i])
        roll = _is_roll_gap(df, i, atr)
        if roll:
            roll_gaps += 1

        # ---------------------------------------------------------- 持仓管理
        if position is not None:
            exit_price: Optional[float] = None
            reason = ""
            if roll:
                exit_price = float(df["close"].iloc[i - 1])
                reason = "roll"
            else:
                a = position.entry_atr
                sign = 1.0 if position.direction == LONG else -1.0
                if p.stop_atr > 0:
                    stop = position.entry - sign * p.stop_atr * a
                    if (sign > 0 and low <= stop) or (sign < 0 and high >= stop):
                        exit_price = stop
                        reason = "stop"
                if exit_price is None and p.tp_atr > 0:
                    tp = position.entry + sign * p.tp_atr * a
                    if (sign > 0 and high >= tp) or (sign < 0 and low <= tp):
                        exit_price = tp
                        reason = "tp"
                if exit_price is None and p.hold_bars > 0 and (i - position.entry_i) >= p.hold_bars:
                    exit_price = close
                    reason = "hold"
            if exit_price is None and i == n - 1:
                exit_price = close
                reason = "eod"

            if exit_price is not None:
                trades.append(
                    _build_trade(
                        symbol, period, position, df, i, exit_price, reason, p.fee_bps
                    )
                )
                position = None
                continue  # 平仓当根不再开新仓

        # ---------------------------------------------------------- 箱体确认 / 突破
        if pending is None:
            if trend_seq[i]:
                box = detect_box(df, bp, atr, end_i=i)
                if box is not None:
                    pending = {"box": box, "i": i}
            continue

        box = pending["box"]  # type: ignore[assignment]
        assert isinstance(box, Box)
        buf = bp.break_buf_atr * box.atr
        far = p.invalidate_atr * box.atr
        direction: Optional[str] = None
        if close > box.high + buf:
            direction = LONG
        elif close < box.low - buf:
            direction = SHORT

        if close > box.high + far or close < box.low - far:
            pending = None  # 走太远，箱作废
        elif direction is not None:
            if roll:
                pending = None  # 换月根不开仓
            elif trend_seq[i] != direction:
                skip_against += 1
                pending = None
            else:
                entry_atr = float(atr.iloc[i]) if i < len(atr) else np.nan
                if not np.isfinite(entry_atr) or entry_atr <= 0:
                    entry_atr = box.atr
                position = _Position(
                    direction=direction,
                    trend=str(trend_seq[i]),
                    box_start=box.start_time,
                    entry=close,
                    entry_i=i,
                    entry_atr=entry_atr,
                )
                pending = None
        elif i - int(pending["i"]) >= bp.w_max + p.max_extra_bars:
            pending = None  # 迟迟不突破

    # 收尾：最后一根才开仓却没走到出场判定 → 按收盘平，保证「入场必落明细」
    if position is not None:
        trades.append(
            _build_trade(
                symbol,
                period,
                position,
                df,
                n - 1,
                float(df["close"].iloc[n - 1]),
                "eod",
                p.fee_bps,
            )
        )

    return SymbolResult(
        symbol=symbol,
        period=period,
        trades=trades,
        skip_against_trend=skip_against,
        roll_gaps=roll_gaps,
        bars=n,
    )


def _build_trade(
    symbol: str,
    period: str,
    position: _Position,
    df: pd.DataFrame,
    exit_i: int,
    exit_price: float,
    reason: str,
    fee_bps: float,
) -> Trade:
    sign = 1.0 if position.direction == LONG else -1.0
    gross = sign * (exit_price - position.entry) / position.entry
    ret = gross - 2.0 * fee_bps / 10000.0
    return Trade(
        symbol=symbol,
        period=period,
        direction=position.direction,
        trend=position.trend,
        box_start=position.box_start,
        entry_time=fmt_time(df["datetime"].iloc[position.entry_i]),
        entry=position.entry,
        exit_time=fmt_time(df["datetime"].iloc[exit_i]),
        exit=exit_price,
        ret=ret,
        correct=ret > 0,
        reason=reason,
        entry_i=position.entry_i,
        exit_i=exit_i,
    )


TRADE_COLUMNS = (
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
)


def trade_rows(trades: Sequence[Trade]) -> List[Dict[str, object]]:
    """明细行（列名与方案一致，收益列叫 ``return``）。"""
    return [
        {
            "symbol": t.symbol,
            "period": t.period,
            "trend": t.trend,
            "box_start": t.box_start,
            "entry_time": t.entry_time,
            "entry": t.entry,
            "exit_time": t.exit_time,
            "exit": t.exit,
            "return": t.ret,
            "correct": t.correct,
            "reason": t.reason,
        }
        for t in trades
    ]


SUMMARY_COLUMNS = (
    "period",
    "trades",
    "win_rate",
    "avg_return",
    "total_return",
    "avg_win",
    "avg_loss",
    "profit_factor",
    "max_dd",
    "skip_against_trend",
)


def summarize(
    trades: Sequence[Trade], period: str, skip_against_trend: int = 0
) -> Dict[str, object]:
    """汇总：正确率 / 总收益 / 盈亏均值 / 盈亏比因子 / 等名义权益最大回撤。

    ``total_return`` = 等名义（每笔同额）收益累加，> 0 才说明这套参数赚钱。
    """
    rets = [t.ret for t in trades]
    wins = [r for r in rets if r > 0]
    losses = [r for r in rets if r <= 0]
    count = len(rets)
    gross_win = float(sum(wins))
    gross_loss = float(-sum(losses))
    if gross_loss > 0:
        profit_factor = gross_win / gross_loss
    else:
        profit_factor = float("inf") if gross_win > 0 else 0.0

    # 等名义单笔收益按平仓时间累加，取最大回撤
    ordered = sorted(trades, key=lambda t: (t.exit_time, t.exit_i))
    equity = 0.0
    peak = 0.0
    max_dd = 0.0
    for t in ordered:
        equity += t.ret
        peak = max(peak, equity)
        max_dd = max(max_dd, peak - equity)

    return {
        "period": period,
        "trades": count,
        "win_rate": (len(wins) / count) if count else 0.0,
        "avg_return": (float(sum(rets)) / count) if count else 0.0,
        "total_return": float(sum(rets)),
        "avg_win": (gross_win / len(wins)) if wins else 0.0,
        "avg_loss": (-gross_loss / len(losses)) if losses else 0.0,
        "profit_factor": profit_factor,
        "max_dd": max_dd,
        "skip_against_trend": skip_against_trend,
    }
