# stock-x

A 股选股系统：Go 1.26+ 后端 + React 前端。灵感来自 [Sequoia-X](https://github.com/sngyai/Sequoia-X)（MIT），数据源为 [baostock](http://www.baostock.com) TCP 协议（后复权日 K）。

仓库：[https://github.com/wbscoder2026/stock-x](https://github.com/wbscoder2026/stock-x)

## 要求

- Go >= 1.26（本机可用 `go1.26.4`）
- Node.js / npm（构建前端）

## 一键启动

macOS / Linux（`start.sh`）：

```bash
chmod +x start.sh
./start.sh          # 构建前端并在 :8080 提供 API + 页面
./start.sh dev      # 开发：Vite :5173 反代 /api → :8080
```

Windows（`start.cmd`，可直接双击；它内部用 `-ExecutionPolicy Bypass` 调 `start.ps1`，不受脚本执行策略限制）：

```powershell
.\start.cmd          # 构建前端并在 :8080 提供 API + 页面
.\start.cmd dev      # 开发：Vite :5173 反代 /api → :8080（等价 SET START_DEV=1）
```

两个脚本做的事完全一样：校验 Go >= 1.26 → 首次生成 `.env` → 按需 `npm install` → 按需 `npm run build` → 同步 `web/dist` 到 `internal/webembed/dist` → `go run ./cmd/stock-x serve`。`make build` / `make dev` 会自动挑当前系统的那个。

> 前端依赖固定走国内镜像：`web/.npmrc` 设了 `registry=https://registry.npmmirror.com`，并且**关掉了 `proxy` / `https-proxy`**——因为 `package-lock.json` 里的 `resolved` 全写成 `registry.npmjs.org`，如果同时开着全局代理（如 `127.0.0.1:10809`），上百个 tarball 会挤在一条代理链路上并发下载，表现为 `npm install` 长时间卡住。镜像在国内，直连更快。若你的网络必须经代理出网，删掉 `web/.npmrc` 里的 `proxy=false` / `https-proxy=false` 两行即可。

> **端口被别的本地应用「占用」过？** 如果 `127.0.0.1:8080` 以前跑过别的本地 Web 应用（尤其是带 Service Worker 的 PWA），浏览器会按 **origin** 把那个 Service Worker 一直留着，于是不管后端换成什么，页面永远是旧应用 —— 此时 `curl` 能拿到新页面，浏览器却不行。处理办法（任选）：换端口（`.env` 里改 `HTTP_ADDR=:8090`）、用 `http://localhost:8080` 访问（和 `127.0.0.1` 是不同 origin）、或在浏览器里注销该 Service Worker（Edge/Chrome 的 `edge://serviceworker-internals` / `chrome://serviceworker-internals`）。

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

## 期货（两个页面）

前端把期货拆成两块，导航里分别是 **期货回测** `/futures/backtest` 与 **期货监控** `/futures/watch`（旧入口 `/futures` 会跳到回测页）：

- **期货回测**：单品种扫描关键位 + 价格图 + 带止盈止损的回测 + **参数扫描（网格搜索）**。它不订阅任何行情，只在你点按钮时取一次数据。
- **期货监控**：盯盘发提醒、黑名单。点提醒行会跳到回测页并加载该月份合约对应级别的价格图。

## 期货突破监控（全市场）

监控页 `/futures/watch` 的「市场监控」可**一键监控全市场主力连续**是否突破 ORB / Donchian / 枢轴位 / 昨高昨低，出现突破立刻弹窗（浏览器允许时同时发原生通知）。间隔默认 30s（5~600s）；运行中修改「级别 / ORB / Donchian / ATR周期 / ATR缓冲 / 量能倍数」会在 500ms 防抖后热更新，不用重启。监控跑在服务端，关掉页面也继续跑，重开页面自动补齐最近 500 条提醒（标「实时 / 启动时已有」）。品种可筛选，留空 = 全部 67 个品种。全市场一轮约 3~4s；若新浪限流（HTTP 456）会自动退避（最多把间隔拉长到 5 倍，页面显示「限流退避 ×N」），恢复后自动回到正常间隔。

| 接口 | 说明 |
|---|---|
| `POST /api/futures/watch/start` | 一键开启（body = 突破页参数 + `interval` + `prefixes` + `alert_ttl_min`），已在运行则为热更新 |
| `GET /api/futures/watch/config` | 读**已保存的**监控配置（没保存过 → 默认值），页面进来自动回填 |
| `POST /api/futures/watch/config` | 保存配置：没在跑只落库；在跑则立刻热生效。**不会顺手启动监控** |
| `POST /api/futures/watch/stop` | 停止 |
| `GET /api/futures/watch/status` | 运行状态、覆盖品种数、每轮扫描/失败数/耗时、最近错误 |
| `GET /api/futures/watch/events?since=N` | 增量提醒（`fresh=true` = 刚发生 → 前端弹窗） |
| `POST /api/futures/watch/alert/test` | 当场验证提醒通道（body: `{"feishu":true,"desktop":true}`） |
| `GET/POST/DELETE /api/futures/blacklist` | 监控黑名单：`scope=variety`（整个品种，如 JM）或 `contract`（单合约，如 JM2701） |
| `POST /api/futures/sweep` | **参数扫描**：每个参数给一组候选值，跑笛卡尔积找最优组合。body 里带 `token` 会顺便登记进度 |
| `GET /api/futures/sweep/progress?token=` | 扫描进度（`running` / `done` / `total` / `workers`），前端每 500ms 轮询画进度条；请求返回即注销 |
| `POST /api/futures/sweep/workers` | 扫描途中改并发（`{token, workers}`）→ 当场 `Tune` 协程池；没在跑则只回带规整后的值 |

**监控黑名单**：提醒列表每行有「加入黑名单」按钮 → 弹出模态框二选一（只屏蔽该合约 / 屏蔽整个品种的所有合约）+ 可选备注；命中的品种/合约**不再进入突破提醒列表**（单品种扫描、回测、`futures-sync` 都不受影响）。黑名单持久化在 SQLite（`futures_blacklist` 表），重启仍生效；卡片上的「黑名单(N)」按钮可查看/移除。品种级立即生效（直接从扫描范围剔除），合约级按「当前主力合约」判断——换月后自动放行。

**推荐止损/止盈**：每条提醒都带 `stop_price` / `tp_price` / `rr` / `tick_size`——**止损 = 突破那根的收盘价 ∓ 1×ATR**，**止盈 = 止损距离 × 盈亏比**（默认 `RR = 1.5`，表单里的「盈亏比」可调，运行中改动自动生效）。

- **按品种最小变动价位取整**：结果对齐到该品种的报价单位（`internal/futures/tick.go` 的 `tickSizes` 表：焦煤 0.5、螺纹钢 1、铜 10、黄金 0.02、原油 0.1、国债 0.002…），所以价格是**真挂得出去的**；前后端显示的小数位也按报价单位走（螺纹钢 `3416`、焦煤 `1221.5`、黄金 `623.44`）。表里没有的品种回落到 1，`TestEveryVarietyHasTickSize` 保证品种池里每个品种都显式登记过——若发现某个品种的报价单位与实际不符，直接改这张表。
- ATR 比一个跳还小时，止损/止盈**至少离入场价 1 个跳**，避免取整把止损压到入场价上。
- ATR 数据不足时两个价都给 0，前端显示「-」。飞书卡片与「测试提醒」样例也带这段推荐价，格式同样按报价单位取整。

**回测（带止盈止损）**：`POST /api/futures/backtest` 走和提醒同一套出场规则——入场 = 突破那根的收盘价；**止损 = 入场 ∓ `stop_atr`×ATR**（默认 `stop_atr = 1`）；**止盈 = 入场 ± 止损距离×盈亏比**（表单里的「盈亏比」，默认 1.5，都按品种报价单位对齐）；从下一根起逐根检查，最多持有「最多持有根数」根。两条保守约定：**同一根既破止损又触止盈 → 按止损算**；**跳空开盘已越过止损 → 按开盘价成交**。收益已按突破方向折算（做空跌了是正的），`r_multiple` 以「止损距离」为 1R，汇总给胜率 / 盈利因子 / 期望 R / 出场分布。**未计手续费与滑点**。

> **止损尺度与周期**：ATR 随周期放大，1×ATR 止损的**点数**在 60 分钟上是 15 分钟的约 2~3 倍，但**占价格的百分比**仍在 1% 以内（实测：焦煤 15m 0.34% / 60m 0.90%，螺纹 15m 0.12% / 60m 0.29%，白银 15m 0.27% / 60m 0.60%）。嫌大可以调小 `stop_atr`，但实测**缩小止损多数品种期望 R 反而变差**（更容易被正常波动扫掉：豆粕 60m 从 0.15 掉到 −0.07，黄金 60m 从 0.20 掉到 −0.13）。风控的正解是**先定止损距离、再按「可承受亏损 ÷ (止损点数 × 合约乘数)」反推手数**。

**参数扫描（网格搜索）**：`POST /api/futures/sweep` 把每个参数的候选值做成轴（**级别** / ORB / Donchian / ATR周期 / ATR缓冲 / 量能倍数 / 持有K线 / 止损ATR / 盈亏比，还有 `objective` 排序目标、`min_trades` 最少样本数），跑**所有组合**：

- **同一级别只取一次 K 线**，该级别的所有组合共用（否则 N 个组合 = N 次上游请求）；**级别做轴时每个级别各取一次数**（顺序取，避免把上游打限流；日线共用一次）。组合之间互相独立 → worker 池并行（实测 31ms/组合，72 组合 0.6s；串行要 2.2s）。
- 某个级别取不到数据（上游没这段历史）**只跳过它**并在 `skipped` 里说明，不拖垮整次扫描；结果回带 `period_bars`（各级别 K 线根数）——上游分钟线只留最近约 1000 根，**15 分钟 ≈ 1 个月、60 分钟 ≈ 5 个月，样本数差好几倍，不能直接横向比**。
- 组合数上限按「级别 × 参数组合」总数算（默认 **10000000**，1000 万，`limit` 可调）；实测级别差异往往**比参数微调影响更大**：焦煤 30 分钟期望 R 0.74，而 5 分钟只有 0.22；白银只有 60 分钟为正（0.28），15/30 分钟都是负的。
- `objective` 支持 `win_rate` / `avg_return` / `avg_r`（期望 R，最值得看）/ `profit_factor`，`rows` 按「**可靠组合优先** → 目标降序 → 样本数降序」返回（前端可再按列排序），`best` 就是第一行。
- **样本不足的组合标 `reliable=false`**：3 笔 100% 胜率不算本事，不会盖过可靠组合——这是防过拟合的关键。
- **结果表排序**：表头可点，自己接管排序（不依赖 antd v6 的内置 sorter），降序/升序切换、全量行参与排序；配「只看样本足」开关默认隐藏样本不足的组合（避免被 3 笔 100% 胜率带偏）。
- **并发可调（扫描途中也能改）**：`workers`（**默认 64**，上限 64；填 0 = 自动用满核数）。跑的是 [ants](https://github.com/panjf2000/ants) 协程池，`POST /api/futures/sweep/workers {token, workers}` 会当场 `Tune` —— **正在跑的任务不打断，之后提交的任务按新并发上**（调小则等当前任务跑完自然收敛）。因为 ants 的 `Submit` 在池满时阻塞，内存里同时挂着的任务数 ≈ 并发数而不是组合数（千万组也不会把任务全堆进内存），而且容量一涨阻塞的提交立刻就能用上。扫描是纯计算、组合互相独立 → **并发不影响结果**（1 并发与多并发结果完全一致，有测试盯着）。实测 1000 组 / 1000 根 K 线：1 并发 31s → 4 并发 13s → 8 并发 9s → 11 并发 7s。客户端断开/刷新会取消计算（context 传到协程池），不会白烧 CPU。
- **扫描进度与「切页不丢」**：请求体里带 `token` 时，服务端每跑完一个组合就把 `done` 记下来（`sweepOver` 的 `OnProgress` 回调 → `GET /api/futures/sweep/progress?token=`），前端每 500ms 轮询画进度条：已跑 / 总数 + 按**本次实测速度**外推的剩余时间（比拿上一次的 msPerCombo 估更准）。前端把扫描的表单 / 进度 / 结果 / 排序都放在模块级 store（`web/src/sweepStore.ts`），所以**切到监控页再回来，进度和结果都还在**；只有**刷新页面**才会取消请求（SPA 切页不会）。
- 组合数上限 **10000000**（1000 万，`limit` 可调），超限错误会写清「几个级别 × 几组参数」；参数错误一律 400，取数/数据问题才是 502。上限放到千万级后，真正跑不跑得完取决于内存与耗时：每个组合都要留一份 `Params` + `SweepRow`（千万组约几个 GB），单核实测约 31ms/组合，请配合 `workers` 用。

**回测时间范围（`from` / `to`）**：按**信号时间**筛选（含边界），支持 `2006-01-02`（当天整天）或 `2006-01-02 15:04`，界面用带时间的范围选择器 + 「近1月 / 近3月 / 不限」快捷按钮。三个约定：

- **出场可以延续到结束时间之后**（持有期自然走完）—— 否则会把当日尾盘的信号系统性砍掉、统计有偏。
- 指标（ATR / Donchian / 关键位）仍用范围之前的**全部历史**计算，所以缩小区间不会让指标失准，也没有未来函数。
- 解析不了的输入按「不限」处理；但 `from > to` 或明显非法的写法会被 API 拦成 **400**（`ValidateBacktestParams`），不会拖到取数阶段报 502。范围对**参数扫描的每个组合**都生效（它是筛选，不是扫描轴，不改变组合数）。

> 实测意义（焦煤 60m、1×ATR 止损、盈亏比 1.5）：**不限范围 1217 笔、期望 R 0.08、PF 1.17；只取近 1 个月 → 103 笔、期望 R −0.39、PF 0.40**。同一套参数在最近一个月是负期望 —— 这类「边际不稳定」只有分区间跑才看得出来，别拿全样本的平均值当承诺。

**隔夜筛选（`no_overnight`）**：关掉 = 日内策略——**当日「日盘」收盘前必须平掉**（日盘口径 `hour < 16`，即 09:00–15:00），出场原因标成「日内收盘」；**夜盘 21:00 之后属次日交易时段，日内模式同样不持有**。入场就在日盘收盘后/夜盘的信号**不可交易** → 跳过并单独计数（`skipped_eod`，不计入胜率），避免用成交不了的信号美化统计。扫描里它也能当轴（`no_overnight: [0,1]`），直接比「日内 vs 隔夜」。

> 实测（StopATR=1、RR=1.5、持有 6 根）：**低级别日内更好**（焦煤 15m 期望 R 0.27→0.47、白银 15m −0.28→−0.06、黄金 15m −0.05→+0.06，5/5 改善）；**60 分钟则隔夜更好**（焦煤 0.08 vs 0.05、白银 0.23 vs 0.10、豆粕 0.15 vs 0.06，4/4）——因为 60 分钟的持仓本来就长，日内被强制在收盘平掉（焦煤 60m 有 80% 的出场是「日内收盘」），趋势段收益恰好在隔夜那一段。代价：日内模式会**跳过 33%~60% 的信号**（多出现在下午/夜盘）。另外焦煤 60m 日内胜率 56.4% 高于隔夜 47.7%，但期望 R 反而更低 —— 又一个「胜率会骗人」的例子。

**两个指标怎么读**：

- **期望 R（`avg_r`）**＝每笔「收益 ÷ 该笔止损距离」的平均值。1R 就是入场到止损的距离；止盈单约 +盈亏比 R，止损单约 −1R。**> 0 才有正期望**。例：10 笔里 4 笔止盈（各 +1.5R）、5 笔止损（各 −1R）、1 笔到期 +0.5R → 期望 R = (4×1.5 − 5 + 0.5) ÷ 10 = **0.15R**；每笔风险 1000 元时相当于平均每笔赚 150 元（未计手续费）。它把不同品种/周期/止损宽度换算到「每单位风险赚多少」这把同一把尺子上，所以最值得用来横向排序。
- **盈利因子（`profit_factor`）**＝总盈利 ÷ 总亏损（只看金额不看笔数）。1.0 打平、1.6 表示赚的是亏的 1.6 倍、< 1 就是亏的更多。例：4 胜共 +6%、6 负共 −4% → PF = 1.5。与胜率的关系：PF = (胜率×平均盈利) ÷ ((1−胜率)×平均亏损)，所以**胜率 40% + 盈亏比 3 也能 PF > 1**。局限：**对单笔大赚/大亏很敏感**，样本少时波动大（配合「样本不足」标记看）。显示「-」= **没有亏损单**（PF 理论无穷大），不是没数据。

**数据健康度校验**：行情源的分钟线/日线先过 `internal/futures/sanity.go`——① 价格量级混叠（同一序列出现两簇相差 2 倍以上且各占 ≥2%）判脏；② 分钟线尾价与日线尾价相差 >50% 判脏。判脏即视为**该源本次失败**，链路自动降级到下一个源（`sources` 里能看到失败计数与冷却）；全链路都脏则报错，绝不静默用脏数据出信号。这条是因为实测东财主连（`114.jmm` / `klt=60`）会把历史段分钟线返回成 1153 万量级、最新段才是 1525（97% 的 K 线不可用），而它"解析成功"会一直赢过干净的源，把 ATR、ATR 缓冲、推荐止损、回测统计全部算废。

**监控配置持久化**：页面上的监控参数（级别 / ORB / Donchian / ATR / 量能 / 止损ATR / 盈亏比 / 间隔 / 品种 / 提醒通道 / 保留时长）都存在 SQLite 的 `futures_watch_config` 单行表里 —— 刷新、切标签、重启服务后都会**自动回填**；改任意参数会**防抖 500ms 自动保存**（运行中同时热生效，一个入口两种效果）。`enabled`（开着没开着）也一起存：服务端启动时按它**自动恢复监控**（默认开启），但如果是用户上次主动停的，就**不会**自己开起来。停止操作只翻转开关，**不会**动已保存的参数。

**监控默认值**：默认开启监控 + 默认开桌面通知（`futures.DefaultWatchConfig()`）；飞书要配 `FEISHU_WEBHOOK_URL`，默认关闭。自动恢复失败（比如库里存了非法品种码）**不会让服务起不来**，原因会写进状态提示里。

**提醒时效**：突破提醒讲究时效，保留时长**可在页面上配**（`alert_ttl_min`，1~1440 分钟，默认 30）—— K 线时间早于「现在 − 该时长」的事件**不进列表、不外发**，服务端在每轮扫描结束和每次读取 `events` 时都会清理，前端每轮轮询同步剔除，所以过期的提醒会**自动从列表消失**。状态里下发 `alert_ttl_sec`，前端文案按它渲染；被清理的事件仍留在去重集合里，同一根 K 线不会被重复提醒。

**提醒通道（离开浏览器也能收到）**：只有 `fresh=true`（刚发生）的突破才外发，同一根 K 线只发一次；通道可在页面上随时开关，也可用「测试提醒」按钮当场验证。

| 通道 | 依赖 | 说明 |
|---|---|---|
| 站内弹窗 | 页面开着 | antd notification，不自动消失 |
| 浏览器原生通知 | 页面/标签在后台 + 浏览器已授权 | 首次开启监控时申请权限 |
| **飞书推送** | 服务端 `FEISHU_WEBHOOK_URL` | 服务端卡片推送，关掉浏览器也能收到；未配置时状态栏会提示 |
| **桌面通知** | 服务端所在机器（macOS 通知中心 / Linux `notify-send` / Windows 操作中心） | 带提示音，不需要浏览器。macOS 走 `osascript display notification`，Linux 走 `notify-send`，Windows 走系统自带的 Windows PowerShell 调 WinRT `ToastNotificationManager`（`powershell -EncodedCommand`，标题正文以 base64 塞进 Toast XML，任何字符都不会破坏脚本） |

提醒列表会显示**主力月份合约**（如 `RB/2701`）；合约由新浪行情中心的持仓量异步解析（每个品种当天只查一次，不拖慢扫描）。**点任意一行** → 自动切到该月份合约 + 监控所在级别，加载价格图并画出关键位（`bars_period` 支持 5/15/30/60/120 各级别）。

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
- `./start.sh` 拉起的 `serve` 会在后台做两件事：① 每 30 分钟增量同步 `1d,5,15,30,60,120`（`FUTURES_SYNC=0` 可关）；② 单独慢慢补 **1 分钟**历史，一次只打一页、请求间隔约 600ms。页面「本地期货」能看到每个品种补到哪一天，也可以暂停、继续，或指定品种和时间范围插队
- 内存缓存按**当前空闲内存的 70%**自动伸缩，不写死根数。扫描和回测仍是内存优先，其次 SQLite，两边都有就不再为这段历史打接口
- 参数扫描的每个组合只算统计，不再为每组复制一整段图表 K 线，避免并行时把行情数据在内存里翻很多份
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
