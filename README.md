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
```
