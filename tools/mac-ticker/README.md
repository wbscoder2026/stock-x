# stockx-ticker · macOS 系统级期货浮窗

网页里的浮窗只能活在浏览器页面里，切到别的应用就没了（浏览器沙箱限制）。这个工具用 AppKit
开一个**原生置顶窗口**，`level = .floating` + `canJoinAllSpaces`，所以**切到任何应用 / 任何桌面 / 全屏应用里都一直在**。

## 用法

```bash
# 1) 先填要看的品种（最多 5 个，写进 ~/.stockx-ticker.json，只做一次）
./tools/mac-ticker/run.sh --symbols=JM0,RB0,PS0

# 2) 之后就正常启动（会自动编译）
./tools/mac-ticker/run.sh

# 排查用：不开窗口，抓一次打印到终端
./tools/mac-ticker/run.sh --print
```

## 配置 `~/.stockx-ticker.json`

| 字段 | 说明 |
|---|---|
| `server` | 后端地址，默认 `http://127.0.0.1:8080` |
| `symbols` | 要显示的代码，最多 5 个（`JM0` 主连 / `JM2601` 月份合约都行） |
| `count` | 显示几行（1~5） |
| `opacity` | 透明度 0.3~1 |
| `intervalSec` | 刷新间隔（秒） |
| `showBidAsk` | 是否显示买一/卖一（每行多一行盘口） |
| `x` / `y` | 窗口位置（拖动后自动记住） |

## 操作

- **拖动**：直接拖窗口任意位置（位置自动保存）
- **菜单栏「期」图标**：显示/隐藏浮窗、立即刷新、编辑配置、退出
- **⌘Q**：退出（置顶窗口没有关闭按钮，退出走菜单栏）

数据来源就是本机接口 `GET {server}/api/futures/quotes?symbols=...`，所以：
价格/持仓量以分钟线为准，买一卖一来自实时口（要过价格交叉校验），取不到时那一行显示 `—` 并在悬浮提示里给原因。
