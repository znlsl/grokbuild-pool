#!/bin/sh
set -eu

DATA_DIR="${POOL_DATA_DIR:-/data}"
CONFIG="${POOL_CONFIG:-$DATA_DIR/config.yaml}"
EXAMPLE="/etc/pool-proxy/config.example.yaml"
RENDER="/usr/local/bin/render-config"

mkdir -p "$DATA_DIR"

if [ ! -f "$CONFIG" ]; then
  if [ -f "$EXAMPLE" ]; then
    cp "$EXAMPLE" "$CONFIG"
  else
    echo "缺少配置模板: $EXAMPLE" >&2
    exit 1
  fi
fi

# 纯 Go 渲染环境变量到 config.yaml（见 cmd/render-config）
if [ -x "$RENDER" ]; then
  "$RENDER" "$CONFIG"
else
  echo "缺少 render-config 二进制" >&2
  exit 1
fi

exec "$@"
