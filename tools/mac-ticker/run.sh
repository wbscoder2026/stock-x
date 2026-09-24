#!/usr/bin/env bash
# 编译（需要改动 main.swift 时才重编）并启动系统级浮窗。
set -euo pipefail
cd "$(dirname "$0")"
if [ ! -x build/stockx-ticker ] || [ main.swift -nt build/stockx-ticker ]; then
  mkdir -p build
  echo "编译中…"
  swiftc -O -o build/stockx-ticker main.swift
fi
exec ./build/stockx-ticker "$@"
