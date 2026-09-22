# stock-x

A 股选股系统：Go 1.26+ 后端 + React 前端。灵感来自 [Sequoia-X](https://github.com/sngyai/Sequoia-X)（MIT），数据源为 [baostock](http://www.baostock.com) TCP 协议（后复权日 K）。

仓库：[https://github.com/wbscoder2026/stock-x](https://github.com/wbscoder2026/stock-x)

## 要求

- Go >= 1.26（本机可用 `go1.26.4`）
- Node.js / npm（构建前端）

## 一键启动

```bash
chmod +x start.sh
./start.sh          # 构建前端并在 :8080 提供 API + 页面
./start.sh dev      # 开发：Vite :5173 反代 /api → :8080
```

首次选股前先回填：

```bash
go run ./cmd/stock-x backfill
```

环境变量见 `.env.example`。飞书 Webhook 可留空。

`BAOSTOCK_WORKERS` 默认 **4**（上限 8）。并发登录过多会被匿名账号临时拉黑（错误「黑名单用户」），此时**不要立刻重试**，等 10～30 分钟后再点「继续回填」；已写入 SQLite 的 K 线会保留。

## 命令

```bash
go run ./cmd/stock-x serve      # HTTP
go run ./cmd/stock-x backfill   # 全市场历史 K 线
go run ./cmd/stock-x sync       # 增量
go run ./cmd/stock-x scan       # 跑策略
go run ./cmd/stock-x futures-sync       # 期货 K 线落本地（日线 + 5 分钟，增量）
go run ./cmd/stock-x futures-backfill   # 期货日线往前翻页挖历史
```

## 期货突破监控（全市场）

期货突破页 `/futures` 的「市场监控」可**一键监控全市场主力连续**是否突破 ORB / Donchian / 枢轴位 / 昨高昨低，出现突破立刻弹窗（浏览器允许时同时发原生通知）。间隔默认 30s（5~600s）；运行中修改「级别 / ORB / Donchian / ATR周期 / ATR缓冲 / 量能倍数」会在 500ms 防抖后热更新，不用重启。监控跑在服务端，关掉页面也继续跑，重开页面自动补齐最近 500 条提醒（标「实时 / 启动时已有」）。品种可筛选，留空 = 全部 67 个品种。全市场一轮约 3~4s；若新浪限流（HTTP 456）会自动退避（最多把间隔拉长到 5 倍，页面显示「限流退避 ×N」），恢复后自动回到正常间隔。

| 接口 | 说明 |
|---|---|
| `POST /api/futures/watch/start` | 一键开启（body = 突破页参数 + `interval` + `prefixes`），已在运行则为热更新 |
| `POST /api/futures/watch/config` | 同 start（运行中改配置） |
| `POST /api/futures/watch/stop` | 停止 |
| `GET /api/futures/watch/status` | 运行状态、覆盖品种数、每轮扫描/失败数/耗时、最近错误 |
| `GET /api/futures/watch/events?since=N` | 增量提醒（`fresh=true` = 刚发生 → 前端弹窗） |

**数据源（多源容错，`internal/futures/source.go`）**：按顺序尝试，某个源失败就进冷却（30s→60s→120s→240s→封顶 5 分钟），后面的源顶上；页面显示「数据源 eastmoney（sina 冷却 60s）」。

| 源 | 覆盖 | 说明 |
|---|---|---|
| `eastmoney`（优先） | 商品 5 所 60 个品种 | `push2his.eastmoney.com`，免费无账号，主连代码 = 品种字母 + `m`（`rbm`/`jmm`/`tam`），5/15/30/60/120 分钟 + 日线 800 根，限流宽松 |
| `sina`（兜底） | 全部 67 个品种（含中金所 7 个） | `stock2.finance.sina.com.cn`，分钟线只覆盖近期，连打会 HTTP 456 限流 |
| `sina-alt`（末位保险） | 同新浪 | `stock.finance.sina.com.cn` 备用域名，限流可能与主域名独立计数；只在前两级都失败时被尝试 |

实测（全市场一轮）：单源新浪 = 134 次请求；多源 = 东财 120 次 + 新浪仅 18 次（中金所 14 + 东财失败回退 4），耗时 2.8s → 1.7s。任一源被限流时另外的源立刻顶上（实测：东财连续失败 5 次进入 296s 冷却，新浪承接全部 134 次请求、失败 0、监控不中断）。

进一步降请求数的选项：用 `hq.sinajs.cn/list=` 批量行情（1 次请求拿全市场快照，已实测可用）在本地按周期合成 K 线；或接期货公司 CTP 推送（无 HTTP 轮询）。

```bash
go test ./internal/futures/                                                     # 单测（httptest 打桩，不联网）
FUTURES_LIVE=1 FUTURES_LIVE_ALL=1 go test ./internal/futures/ -run Live -v       # 真实新浪接口冒烟（67 品种一轮约 3~4s）
```

### 期货历史数据本地化（回测不再重复拉网络）

期货 K 线（分钟 + 日线）落到和股票同一个 SQLite（表 `futures_bar`，主键 `symbol+period+ts`）：

```bash
go run ./cmd/stock-x futures-sync                                  # 全市场日线 + 5 分钟增量同步
go run ./cmd/stock-x futures-sync --periods=1d,5,60 --only=JM,RB   # 指定周期与品种
go run ./cmd/stock-x futures-backfill --periods=1d --pages=4       # 日线往前翻页挖历史（东财一页 800 根≈3.3 年）
```

- 网络只拉一个窗口（上游接口限制），本地按 `symbol+period+ts` upsert 去重：**第二次跑新增 0 根**（实测 2 品种 × 2 周期：首次 +9569 根，第二次 +0）
- `POST /api/futures/backtest`（前端「回测突破对错」）改为**本地优先**：本地有数据就完全不走网络，缺了才拉多源并回写本地
- 分钟线上游只保留最近约 1000 根（东财）/ 约 2 个月（新浪），本地库靠**持续增量**越攒越厚；日线可翻页挖到十几年前（新浪日线本身也回溯到 2013 年）
- 实时监控不受影响：它必须看最新行情，仍走多源实时链路（`internal/futures/source.go`）

## 中继箱体扫描（TqSdk，研究用）

顺大趋势（日线 EMA20/60）+ 中继箱体（高 1~2 ATR）的全品种扫描与回测，独立于期货突破页（ORB/PDH/Donchian 仍走 Go）。方案见 `docs/tqsdk-15m-relay-scan.md`。

```bash
python3 -m pip install -r scripts/tq_relay_scan/requirements.txt   # tqsdk + pandas
export TQ_USER=你的天勤账号 TQ_PASS=你的密码                          # 免费模拟账号即可

python3 scripts/tq_relay_scan/scan.py --period 15 --out /tmp/candidates.csv        # 扫一遍出 CSV
python3 scripts/tq_relay_scan/scan.py --period 5 --only JM,RB --watch 60           # 盯盘：每 60s 只打新出现的品种
python3 scripts/tq_relay_scan/backtest.py --period 15 --from 2024-01-01 --to 2026-09-01 --out /tmp/bt.csv
python3 scripts/tq_relay_scan/backtest.py --period 60 --hold-bars 6 --only JM,RB    # 换周期/换持有根数对比
python3 scripts/tq_relay_scan/backtest.py --period 60 --only JM,RB --sweep-rr 1,1.5,2,3  # 扫盈亏比，看能否盈利
python3 scripts/tq_relay_scan/backtest.py --period 60 --only JM,RB --rr 1.5 --stop-atr 1 # 固定盈亏比（tp = 1.5 × stop）
python3 -m pytest scripts/tq_relay_scan/ -q                                         # 单测（合成 K 线，不需要账号）
```

汇总里 `total_return` 是**等名义收益累加**（每笔同额），`> 0` 才说明这套参数赚钱；`--sweep-rr` 会逐个盈亏比跑一遍并给出对比表和最好的一组。第一版未计保证金杠杆与滑点、成交按收盘价，数字偏乐观。

`--period` 支持 `5/15/30/60/120/1d`（默认 15；`1d` 时趋势改用周线）。箱体/趋势/交易回测都是纯函数（`box.py` / `trend.py` / `trade.py`），`scan.py` / `backtest.py` 只负责订阅、循环、写文件。
