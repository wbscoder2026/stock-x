#!/usr/bin/env bash
# 一键启动 stock-x：装依赖、编译前端、启动 Go 服务（默认 :8080）
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "缺少命令: $1" >&2
    exit 1
  }
}

need go
need npm

GO_VER="$(go env GOVERSION | sed 's/^go//')"
GO_MAJOR="${GO_VER%%.*}"
GO_MINOR="${GO_VER#*.}"
GO_MINOR="${GO_MINOR%%.*}"
if [ "${GO_MAJOR}" -lt 1 ] || { [ "${GO_MAJOR}" -eq 1 ] && [ "${GO_MINOR}" -lt 26 ]; }; then
  echo "需要 Go >= 1.26，当前: ${GO_VER}" >&2
  exit 1
fi

if [ ! -f .env ]; then
  cp .env.example .env
  echo "已生成 .env（可编辑 FEISHU_WEBHOOK_URL）"
fi

# 服务端口：优先环境变量，其次 .env 里的 HTTP_ADDR，最后 :8080
serve_port() {
  local addr="${HTTP_ADDR:-}"
  if [ -z "$addr" ] && [ -f .env ]; then
    addr="$(sed -n 's/^[[:space:]]*HTTP_ADDR[[:space:]]*=[[:space:]]*//p' .env | tail -n 1 | tr -d '[:space:]')"
  fi
  [ -z "$addr" ] && addr=":8080"
  printf '%s' "${addr##*:}"
}

port_pids() {
  local port="$1"
  if command -v lsof >/dev/null 2>&1; then
    lsof -ti "tcp:$port" -sTCP:LISTEN 2>/dev/null || true
  elif command -v ss >/dev/null 2>&1; then
    ss -lptnH "sport = :$port" 2>/dev/null | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' || true
  fi
}

# 端口上残留着上一次 go run 的 stock-x 时，新进程会 bind 失败直接退出，
# 表现成「脚本说启动了，页面却没换」。这里先把它清掉再启动。
clear_stale_server() {
  local port pid name
  port="$(serve_port)"
  pid="$(port_pids "$port" | sort -u | head -n 1)"
  [ -z "$pid" ] && return 0
  name="$(ps -p "$pid" -o comm= 2>/dev/null || true)"
  case "$name" in
  *stock-x*)
    echo "端口 $port 被上一次的 stock-x 进程占用（PID $pid），先停掉它..."
    kill "$pid" 2>/dev/null || true
    sleep 1
    ;;
  *)
    echo "端口 $port 已被 PID $pid（${name:-未知进程}）占用，请先停掉它再运行。" >&2
    exit 1
    ;;
  esac
}

clear_stale_server

# 仅在 lockfile/package.json 比 node_modules 新，或目录缺失时才 npm install
web_need_install() {
  [ ! -d web/node_modules ] && return 0
  [ web/package.json -nt web/node_modules ] && return 0
  [ web/package-lock.json -nt web/node_modules ] && return 0
  return 1
}

web_install() {
  if web_need_install; then
    echo "安装前端依赖..."
    (cd web && npm install --no-audit --no-fund --prefer-offline)
  else
    echo "前端依赖已就绪，跳过 npm install"
  fi
}

# 源码或配置比 dist 新才重新 vite build
web_need_build() {
  local stamp=web/dist/index.html
  [ ! -f "$stamp" ] && return 0
  [ web/package.json -nt "$stamp" ] && return 0
  [ web/package-lock.json -nt "$stamp" ] && return 0
  [ web/index.html -nt "$stamp" ] && return 0
  [ web/vite.config.ts -nt "$stamp" ] && return 0
  find web/src web/public -type f -newer "$stamp" 2>/dev/null | grep -q . && return 0
  return 1
}

# 开发模式：START_DEV=1 ./start.sh 同时起 Vite 与 API
MODE="${1:-}"
if [ "${START_DEV:-}" = "1" ] || [ "${MODE}" = "dev" ]; then
  echo "开发模式: Vite :5173 + API :8080"
  web_install
  (cd web && npm run dev) &
  VITE_PID=$!
  trap 'kill ${VITE_PID} 2>/dev/null || true' EXIT
  mkdir -p internal/webembed/dist
  if [ ! -f internal/webembed/dist/index.html ]; then
    printf '%s\n' '<!doctype html><meta charset="utf-8"><title>stock-x</title><p>请用 Vite 开发服务器 :5173</p>' > internal/webembed/dist/index.html
  fi
  go run ./cmd/stock-x serve
  exit 0
fi

web_install
if web_need_build; then
  echo "构建前端..."
  (cd web && npm run build)
else
  echo "前端产物已是最新，跳过构建"
fi

echo "同步静态资源到 embed 目录..."
mkdir -p internal/webembed/dist
rm -rf internal/webembed/dist/*
cp -R web/dist/. internal/webembed/dist/

echo "启动服务 http://127.0.0.1${HTTP_ADDR:-:8080} ..."
# serve 会在后台把期货 K 线写入本地 SQLite，并按空闲内存的约 70% 放进内存。
# 1 分钟历史会慢慢补，可在「本地期货」页暂停。关掉多周期增量：FUTURES_SYNC=0
exec go run ./cmd/stock-x serve
