# 全品种中继箱体扫描 + 回测（TqSdk）

## 目标

用天勤 TqSdk 拉国内期货主力连续，做两件事：

1. **扫描**：顺大趋势 + 中继箱体（高度 1~2 ATR）→ 候选清单
2. **回测**：同一套规则在历史 K 线上统计突破后的对错与收益

K 线级别 **可配**，不锁死 15 分钟。`--period` 取值：`5` / `15` / `30` / `60` / `120` / `1d`（秒数见下表）。默认 `15`。

箱体检测、ATR、冲动根数都在 **所选周期** 上算；大趋势仍用日线（`--period 1d` 时趋势改用周线 EMA，避免同周期自相关）。

不做下单。仅研究。

## 为什么用 TqSdk 而不是新浪

| | 新浪（现有 Go/qh.py） | TqSdk |
|---|---|---|
| 覆盖 | 单合约分钟，深度浅 | 全市场主力连续、日线+分钟一次会话 |
| 主连 | `JM0` 近似 | `KQ.m@DCE.jm` 官方主连 |
| 批量 | 易被限流 | 一次 `get_kline_serial` 批量订阅 |

现有突破扫描（ORB/PDH/Donchian）继续走 Go。本脚本是另一套形态：趋势中继，不替代突破页。

## 文件

```
scripts/tq_relay_scan/
  scan.py          # 扫描入口
  backtest.py      # 回测入口
  universe.py      # 品种池 + 交易所映射
  trend.py         # 大周期趋势
  box.py           # 中继箱体（周期无关）
  trade.py         # 突破入场 / 持有出场
  requirements.txt # tqsdk pandas
```

运行：

```bash
python3 -m pip install tqsdk pandas
python3 scripts/tq_relay_scan/scan.py
python3 scripts/tq_relay_scan/scan.py --period 15 --out candidates.csv
python3 scripts/tq_relay_scan/scan.py --period 5 --only JM,RB,CU --watch 60
python3 scripts/tq_relay_scan/backtest.py --period 15 --from 2024-01-01 --to 2026-09-01 --out bt.csv
python3 scripts/tq_relay_scan/backtest.py --period 60 --hold-bars 6 --only JM,RB
```

环境：`TQ_USER` / `TQ_PASS`（天勤账号，免费模拟即可）。无账号则 `TqApi(auth=TqAuth(...))` 失败，脚本应明确报错。

## 品种池

与 `internal/futures/contracts.go` 对齐的商品+股指主力，默认排除国债（T/TF/TS）和明显不活跃品种。

映射表（prefix → TqSdk 标的）：

| 交易所 | 示例 |
|---|---|
| DCE | `KQ.m@DCE.jm` `j` `i` `m` `y` `c` `cs` `p` `l` `v` `pp` `eg` `eb` `pg` `jd` `lh` |
| SHFE | `KQ.m@SHFE.rb` `hc` `au` `ag` `cu` `al` `zn` `ni` `sn` `ss` `fu` `bu` `ru` `sp` `ao` |
| INE | `KQ.m@INE.sc` `lu` `nr` |
| CZCE | `KQ.m@CZCE.TA` `MA` `OI` `RM` `SR` `CF` `FG` `SA` `UR` `PF` `PK` `AP` `CJ` `SH` `PX` `SF` `SM` |
| GFEX | `KQ.m@GFEX.si` `lc` `ps` |
| CFFEX | `KQ.m@CFFEX.IF` `IH` `IC` `IM` |

实现：`universe.py` 静态表（不要运行时爬网页）。`--only` 按 prefix 过滤。

流动性过滤（日线最后一根）：

- 成交额过低（商品 < 5 亿 或 持仓过小）丢弃
- 近 5 日均量 = 0 丢弃

## 周期

| `--period` | 秒 | 扫描默认根数 | 回测建议根数 |
|---|---|---|---|
| 5 | 300 | 400 | 8000 |
| 15 | 900 | 300 | 4000 |
| 30 | 1800 | 250 | 2500 |
| 60 | 3600 | 200 | 2000 |
| 120 | 7200 | 160 | 1500 |
| 1d | 86400 | 120 | 800 |

`period_seconds = {"5":300,"15":900,"30":1800,"60":3600,"120":7200,"1d":86400}`

箱体窗口 `w_min/w_max`、冲动 `impulse_n` **按根数** 定义，换周期不用改形态逻辑；物理时长会变（15m×12 根=3h，60m×12 根=12h）。若以后要「按小时对齐」再加 `--impulse-hours`，第一版不搞。

## 数据订阅

扫描（实时/近端）：

```python
sec = period_seconds[args.period]
klines_trend = {sym: api.get_kline_serial(sym, 24*60*60, 120) for sym in symbols}  # 1d 时改 7*86400
klines_box = {sym: api.get_kline_serial(sym, sec, n_scan) for sym in symbols}
api.wait_update(deadline=time.time() + 30)
```

回测用 **TqBacktest**（同一套 `box.py` / `trend.py` / `trade.py`）：

```python
api = TqApi(
    auth=TqAuth(...),
    backtest=TqBacktest(start_dt=date.fromisoformat(args.from_), end_dt=date.fromisoformat(args.to)),
)
```

不要自己拼新浪历史。主连在回测区间内会换月，TqSdk 主连已处理跳空，报告里加一列 `gap` 标记换月日，统计时可选剔除换月前后 2 根。

注意：

- 全市场一次订阅约 50～60 个主连 × 2 周期；超时则分批 20 个
- 夜盘跨日：箱体按 **连续序列**，不要按自然日切开
- 盘中最后一根未走完则丢掉
- 回测不要 `wait_update` 空转；按 TqSdk 回测循环 `while True: api.wait_update()` 直到结束

## 顺大趋势（日线）

对日线收盘：

```
ema20 = EMA(close, 20)
ema60 = EMA(close, 60)
```

判定（最后一根已收盘日；盘中用上一根完整日线，避免 15:00 前晃）：

| 结果 | 条件 |
|---|---|
| 多 | `ema20 > ema60` 且 `close > ema20` 且近 10 日 `ema20` 斜率为正 |
| 空 | `ema20 < ema60` 且 `close < ema20` 且近 10 日 `ema20` 斜率为负 |
| 丢弃 | 其余（震荡、死叉初期、价格回到均线纠结） |

可选加强（`--strict`）：近 20 日最高/最低不破 EMA20（多不破、空不破）。

大趋势只做方向过滤器，不在箱体周期上再做一套均线。`--period 1d` 时趋势用周线 EMA20/60，箱体用日线。

## 中继箱体（所选 `--period`）

### 定义（可测、禁止拍脑袋）

在所选周期 K 线上，从最新一根往回看：

1. **箱体窗口** `W`：最近连续 `w` 根（`w ∈ [6, 20]`，默认找使评分最高的 w）
2. **箱高** `H = max(high[W]) - min(low[W])`
3. **ATR** = 窗口左端（箱体开始前一根）的 ATR14，不用箱内 ATR（避免横盘把 ATR 压扁）
4. 硬条件：`atr_lo * ATR <= H <= atr_hi * ATR`（默认 1～2）
5. **冲动**：箱体左侧再取 `imp_n=12` 根，位移 `Δ = |close[-1 of impulse] - close[0 of impulse]|`
   - 多头：冲动为上涨，`Δ >= 1.5 * ATR`，且冲动低点 < 箱体低点（箱体是垫高后的整理）
   - 空头：冲动为下跌，对称
6. **未完成突破**：窗口内收盘不有效越过箱沿（缓冲 `0.15 * ATR`）。允许插针，不允许收盘站稳箱外
7. **中继而非反转**：箱体中轴相对冲动终点回撤不超过冲动幅度的 50%（多头：箱中轴不低于冲动起点 + 0.5Δ）
8. **当前仍在箱内**：最新收盘落在 `[box_low - 0.1ATR, box_high + 0.1ATR]`

不满足任一条 → 非候选。

### 选 w

对 `w = 6..20` 凡符合硬条件的，打分取最高：

```
score = 1 - abs(H/ATR - 1.4)          # 高度靠近 1.4 ATR 加分
      + 0.3 * min(Δ/ATR, 3) / 3       # 冲动越清楚越好
      + 0.2 * (1 - touches_break)     # 假突破次数少
```

只输出每个品种 **一个** 最佳箱体（最近的）。

### 图示（多头）

```
日线 EMA20>EMA60
period K:  上涨冲动 |==== 箱体 1~2ATR ====|  ← 现在还在里面
                    低点抬高，未收盘破箱
```

## 输出

CSV 列：

| 列 | 含义 |
|---|---|
| time | 扫描时刻 |
| period | 5/15/30/60/120/1d |
| prefix | JM |
| name | 焦煤 |
| symbol | KQ.m@DCE.jm |
| trend | 多 / 空 |
| box_high / box_low | 箱沿 |
| height | 箱高 |
| height_atr | H/ATR |
| atr | 所用 ATR |
| impulse_atr | Δ/ATR |
| bars | 箱体根数 w |
| last | 最新价 |
| dist_edge_atr | 距更近箱沿 / ATR |
| box_start / box_end | 箱体起止时间 |

控制台：按 `height_atr` 接近 1.4、再按 `dist_edge_atr` 升序（越靠近沿越可能马上选方向）。

`--watch N`：每 N 秒重算，新出现的品种才打印一行。

## 回测

扫描只看「现在有没有箱」。回测回答「这种箱突破之后赚不赚」。

### 入场（`trade.py`）

箱体在 `i` 根确认存在（当时已满足扫描硬条件，且用 **当时** 可见 K 线，禁止未来函数）。

之后第一根 **收盘** 有效突破箱沿（缓冲 `0.15*ATR`），且方向与日线趋势同向：

- 多：`close > box_high + buf` → 开多，入场价 = 该根收盘
- 空：`close < box_low - buf` → 开空，入场价 = 该根收盘
- 反向突破：记 `skip_against_trend`，不开仓
- 箱体尚未突破就走出箱（收盘离箱超过 `2*ATR` 或根数超过 `w_max+10`）：箱失效，等下一个

同一品种同时只持 1 笔。

### 出场（三选一，先碰到先走）

| 方式 | 默认 | 说明 |
|---|---|---|
| 固定持有 | `--hold-bars 8` | 入场后再走 N 根收盘平 |
| 止损 | `--stop-atr 1.0` | 多：入场后低点触及 `entry - 1*ATR`（入场根 ATR） |
| 止盈 | `--tp-atr 2.0` | 多：高点触及 `entry + 2*ATR` |

`--hold-bars 0` 表示不等时间出场，只靠止损止盈。两者都关则报错退出。

默认：`hold-bars=8`，`stop-atr=1`，`tp-atr=2`（盈亏比约 2，和 1~2ATR 箱高同量级）。

正确/错误：平仓收益符号与方向一致为正（多 `exit>entry`，空 `exit<entry`）。收益用 **价格相对变化**，第一版不算保证金杠杆、手续费；`--fee-bps` 预留（默认 0）。

### 回测输出

汇总（每个 period 一份）：

| 列 | 含义 |
|---|---|
| period | 周期 |
| trades | 笔数 |
| win_rate | 正确笔数 / trades |
| avg_return | 平均收益 |
| avg_win / avg_loss | 盈/亏均值 |
| profit_factor | 总盈利 / 总亏损绝对值 |
| max_dd | 按时间序权益最大回撤 |
| skip_against_trend | 逆势突破次数 |

明细 CSV 每笔：`symbol,period,trend,box_start,entry_time,entry,exit_time,exit,return,correct,reason`（reason=`hold`/`stop`/`tp`）。

控制台先打汇总，再可选 `--trades` 打明细。

### 回测实现约束

- 用 TqBacktest 按时间推进；**每个时点** 只把 `klines` 截到当前，再跑 `detect_box` / `trend`，禁止一次性对整段未来打标
- 或等价：先在纯函数里对历史做 walk-forward（`for i in range(...)` 只用 `df.iloc[:i+1]`），TqApi 只负责拉齐数据。更易测，优先这条
- 主连换月：`|open/prev_close-1| > 3*ATR` 的根标 `roll_gap`，该根不开仓，持仓强制按前收平（避免假盈亏）
- `--from/--to` 必填；缺数据的品种跳过并计数
- 全市场回测按品种串行（内存），`--only` 先跑通

## 实现要点

- `box.py` / `trend.py` / `trade.py` **纯函数 + pandas**，不依赖 TqApi
- `scan.py` / `backtest.py` 只负责订阅、循环、写文件
- 合成测试至少：
  1. 多头冲动 + 1.5ATR 箱 → 命中
  2. 箱高 0.5ATR 或 3ATR → 拒绝
  3. 日线震荡 → 不进箱体检测
  4. 箱确认后下一根收盘突破 → 入场；`hold-bars` 后平仓收益符号正确
  5. 突破前把后续 K 线藏起来再 detect → 结果与「当时」一致（无未来函数）
  6. `--period 5` 与 `60` 用同一合成序列（只改 index 间隔）都能跑通
- 失败单品种记 warning，不中断全市场
- 盘中最后一根未走完则丢掉

## 参数默认

```
period=15
atr_period=14
atr_lo=1.0
atr_hi=2.0
impulse_n=12
impulse_min_atr=1.5
w_min=6
w_max=20
break_buf_atr=0.15
retracement_max=0.5
hold_bars=8
stop_atr=1.0
tp_atr=2.0
fee_bps=0
```

## 不做

- 不接现有 Go `/api/futures`（数据源不同，形态不同）
- 不自动交易、不推送飞书（第一版 stdout/CSV）
- 不扫期权、不扫远月
- 第一版不算杠杆与交易所手续费明细（只有 `--fee-bps`）
- 不做多周期共振（一次只跑一个 `--period`；要对比就跑两次）

## 验收

```bash
python3 -m pytest scripts/tq_relay_scan/ -q
python3 scripts/tq_relay_scan/scan.py --period 15 --only JM,RB --out /tmp/c.csv
python3 scripts/tq_relay_scan/backtest.py --period 15 --only JM --from 2025-01-01 --to 2026-09-01 --out /tmp/bt.csv
```

- 单测全绿（含 walk-forward 无未来函数）
- `--period` 非法值退出码非 0
- 扫描 CSV 若有行：`1 <= height_atr <= 2` 且 `trend` 非空
- 回测汇总 `trades` 与明细行数一致；`correct` 与 `return` 符号相符
- 无账号时退出码非 0，错误信息含 `TQ_USER`

## 工作顺序

1. `universe.py` + 单测映射
2. `trend.py` + 合成日线单测
3. `box.py` + 三条形态单测（周期无关）
4. `trade.py` + 入场/出场/未来函数单测
5. `scan.py` 接 TqApi，`--period 15 --only JM`
6. `backtest.py` walk-forward + Tq 拉数，先单品种
7. 放开全市场、流动性过滤、分批订阅
8. `--watch` / CSV / 多 period 对比跑法写进 README 三行示例
