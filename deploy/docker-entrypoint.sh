#!/bin/sh
set -eu

DATA_DIR="${POOL_DATA_DIR:-/data}"
CONFIG="${POOL_CONFIG:-$DATA_DIR/config.yaml}"
EXAMPLE="/etc/pool-proxy/config.example.yaml"
RENDER="/usr/local/bin/render_config.py"

mkdir -p "$DATA_DIR"

if [ ! -f "$CONFIG" ]; then
  if [ -f "$EXAMPLE" ]; then
    cp "$EXAMPLE" "$CONFIG"
  else
    echo "缺少配置模板: $EXAMPLE" >&2
    exit 1
  fi
fi

# 环境变量覆盖常见字段（见 deploy/render_config.py；含 HOT_SIZE 数字回写修复）
if [ -x "$RENDER" ] || [ -f "$RENDER" ]; then
  python3 "$RENDER" "$CONFIG"
else
  # 开发态：脚本与 entrypoint 同目录
  HERE=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
  if [ -f "$HERE/render_config.py" ]; then
    python3 "$HERE/render_config.py" "$CONFIG"
  else
    echo "缺少 render_config.py" >&2
    exit 1
  fi
fi

exec "$@"
