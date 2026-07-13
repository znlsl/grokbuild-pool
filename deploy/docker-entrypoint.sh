#!/bin/sh
set -eu

DATA_DIR="${POOL_DATA_DIR:-/data}"
CONFIG="${POOL_CONFIG:-$DATA_DIR/config.yaml}"
EXAMPLE="/etc/pool-proxy/config.example.yaml"

mkdir -p "$DATA_DIR"

if [ ! -f "$CONFIG" ]; then
  if [ -f "$EXAMPLE" ]; then
    cp "$EXAMPLE" "$CONFIG"
  else
    echo "缺少配置模板: $EXAMPLE" >&2
    exit 1
  fi
fi

# 用环境变量覆盖常见字段
python3 - "$CONFIG" <<'PY'
import os, sys, re
path = sys.argv[1]
text = open(path, "r", encoding="utf-8").read()

def render(value: str) -> str:
    if value.lower() in ("true", "false") or re.fullmatch(r"-?\d+(\.\d+)?", value or ""):
        return value.lower() if value.lower() in ("true", "false") else value
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"') + '"'

def set_scalar(text, key, value):
    if value is None or value == "":
        return text
    rendered = render(str(value))
    pat = re.compile(rf"(?m)^({re.escape(key)}\s*:\s*).*$")
    if pat.search(text):
        return pat.sub(rf"\1{rendered}", text, count=1)
    return text + f"\n{key}: {rendered}\n"

def set_nested(text, parent, key, value):
    if value is None or value == "":
        return text
    lines = text.splitlines()
    out = []
    i = 0
    while i < len(lines):
        line = lines[i]
        out.append(line)
        if re.match(rf"^{re.escape(parent)}\s*:\s*$", line):
            i += 1
            replaced = False
            while i < len(lines) and (lines[i].startswith(" ") or lines[i].startswith("\t") or lines[i].strip() == ""):
                if re.match(rf"^\s+{re.escape(key)}\s*:", lines[i]):
                    indent = re.match(r"^(\s*)", lines[i]).group(1)
                    out.append(f"{indent}{key}: {render(str(value))}")
                    replaced = True
                    i += 1
                    continue
                out.append(lines[i])
                i += 1
            if not replaced:
                out.append(f"  {key}: {render(str(value))}")
            continue
        i += 1
    return "\n".join(out) + ("\n" if text.endswith("\n") else "")

# 若 ADMIN_KEY 未提供且配置仍是占位，生成一次并打印
admin = os.environ.get("ADMIN_KEY", "").strip()
if not admin:
    m = re.search(r'(?m)^admin_key\s*:\s*"?([^"\n]+)"?\s*$', text)
    cur = (m.group(1).strip() if m else "")
    if cur.lower() in {"", "change-me", "changeme", "dev-admin-change-me", "replace-me"}:
        import secrets
        admin = secrets.token_hex(24)
        os.environ["ADMIN_KEY"] = admin
        print(f"已生成 ADMIN_KEY（请立即保存）：{admin}", file=sys.stderr)

env_map = {
    "listen": os.environ.get("LISTEN"),
    "allow_public_listen": os.environ.get("ALLOW_PUBLIC_LISTEN"),
    "data_dir": os.environ.get("POOL_DATA_DIR", "/data"),
    "api_key": os.environ.get("API_KEY"),
    "admin_key": os.environ.get("ADMIN_KEY") or admin,
    "hot_size": os.environ.get("HOT_SIZE"),
    "mock_upstream": os.environ.get("MOCK_UPSTREAM"),
}
for k, v in env_map.items():
    text = set_scalar(text, k, v)

text = set_nested(text, "upstream", "base_url", os.environ.get("UPSTREAM_BASE_URL"))
text = set_nested(text, "limits", "max_concurrent", os.environ.get("MAX_CONCURRENT"))
text = set_nested(text, "logging", "level", os.environ.get("LOG_LEVEL"))

open(path, "w", encoding="utf-8").write(text)
print(f"config ready: {path}", file=sys.stderr)
PY

exec "$@"
