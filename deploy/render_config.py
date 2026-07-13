#!/usr/bin/env python3
"""Render pool-proxy config.yaml from env overrides.

Used by docker-entrypoint.sh. Extracted so the rewrite rules can be unit-tested
without booting a container.

Critical correctness note:
  re.sub(rf"\\1{rendered}", ...) is unsafe when rendered is a bare number.
  With HOT_SIZE=3000 the pattern becomes \\13000, which re interprets as an
  octal/group backreference and corrupts the YAML (hot_size line becomes "X00").
  Always concatenate via a callable replacement.
"""

from __future__ import annotations

import os
import re
import secrets
import sys
from typing import Mapping, Optional


PLACEHOLDER_ADMIN_KEYS = {
    "",
    "change-me",
    "changeme",
    "dev-admin-change-me",
    "replace-me",
}


def render(value: str) -> str:
    if value.lower() in ("true", "false") or re.fullmatch(r"-?\d+(\.\d+)?", value or ""):
        return value.lower() if value.lower() in ("true", "false") else value
    return '"' + value.replace("\\", "\\\\").replace('"', '\\"') + '"'


def set_scalar(text: str, key: str, value: Optional[str]) -> str:
    if value is None or value == "":
        return text
    rendered = render(str(value))
    pat = re.compile(rf"(?m)^({re.escape(key)}\s*:\s*).*$")
    if pat.search(text):
        # Callable replacement avoids re backreference collisions like \13000.
        return pat.sub(lambda m: m.group(1) + rendered, text, count=1)
    return text + f"\n{key}: {rendered}\n"


def set_nested(text: str, parent: str, key: str, value: Optional[str]) -> str:
    if value is None or value == "":
        return text
    lines = text.splitlines()
    out: list[str] = []
    i = 0
    while i < len(lines):
        line = lines[i]
        out.append(line)
        if re.match(rf"^{re.escape(parent)}\s*:\s*$", line):
            i += 1
            replaced = False
            while i < len(lines) and (
                lines[i].startswith(" ")
                or lines[i].startswith("\t")
                or lines[i].strip() == ""
            ):
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


def apply_env(text: str, env: Mapping[str, str] | None = None) -> tuple[str, Optional[str]]:
    """Apply env overrides. Returns (new_text, generated_admin_key_or_None)."""
    env = env if env is not None else os.environ
    generated_admin: Optional[str] = None

    admin = (env.get("ADMIN_KEY") or "").strip()
    if not admin:
        m = re.search(r'(?m)^admin_key\s*:\s*"?([^"\n]+)"?\s*$', text)
        cur = m.group(1).strip() if m else ""
        if cur.lower() in PLACEHOLDER_ADMIN_KEYS:
            generated_admin = secrets.token_hex(24)
            admin = generated_admin

    env_map = {
        "listen": env.get("LISTEN"),
        "allow_public_listen": env.get("ALLOW_PUBLIC_LISTEN"),
        "data_dir": env.get("POOL_DATA_DIR", "/data"),
        "api_key": env.get("API_KEY"),
        "admin_key": env.get("ADMIN_KEY") or admin,
        "hot_size": env.get("HOT_SIZE"),
        "mock_upstream": env.get("MOCK_UPSTREAM"),
    }
    for k, v in env_map.items():
        text = set_scalar(text, k, v)

    text = set_nested(text, "upstream", "base_url", env.get("UPSTREAM_BASE_URL"))
    text = set_nested(text, "limits", "max_concurrent", env.get("MAX_CONCURRENT"))
    text = set_nested(text, "logging", "level", env.get("LOG_LEVEL"))
    return text, generated_admin


def main(argv: list[str] | None = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    if len(argv) != 1:
        print(f"usage: {sys.argv[0]} /path/to/config.yaml", file=sys.stderr)
        return 2
    path = argv[0]
    with open(path, "r", encoding="utf-8") as f:
        text = f.read()
    text, generated = apply_env(text, os.environ)
    if generated:
        print(f"已生成 ADMIN_KEY（请立即保存）：{generated}", file=sys.stderr)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)
    print(f"config ready: {path}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
