#!/usr/bin/env python3
"""Regression tests for deploy/render_config.py.

Run: python3 deploy/render_config_test.py
"""

from __future__ import annotations

import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import render_config  # noqa: E402


EXAMPLE = """# sample
listen: "0.0.0.0:18080"
allow_public_listen: true
data_dir: "./data"
db_path: ""
api_key: ""
admin_key: "change-me"
hot_size: 1000
mock_upstream: true
upstream:
  base_url: ""
  client_version: "0.2.93"
limits:
  max_body_bytes: 20971520
  request_timeout_sec: 600
  max_concurrent: 64
logging:
  level: info
"""


class RenderConfigTests(unittest.TestCase):
    def test_hot_size_numeric_not_corrupted(self) -> None:
        """HOT_SIZE=3000 must not become YAML garbage via re \\13000 backref."""
        out, gen = render_config.apply_env(
            EXAMPLE,
            {
                "HOT_SIZE": "3000",
                "ADMIN_KEY": "test-admin-key-not-placeholder",
                "MOCK_UPSTREAM": "true",
                "MAX_CONCURRENT": "120",
                "LOG_LEVEL": "info",
                "LISTEN": "0.0.0.0:18080",
                "ALLOW_PUBLIC_LISTEN": "true",
                "POOL_DATA_DIR": "/data",
            },
        )
        self.assertIsNone(gen)
        self.assertIn("hot_size: 3000", out)
        self.assertNotIn("X00", out)
        self.assertNotRegex(out, r"(?m)^X00\s*$")
        # line must remain a valid scalar assignment
        for line in out.splitlines():
            if line.startswith("hot_size"):
                self.assertEqual(line, "hot_size: 3000")

    def test_admin_placeholder_auto_generated(self) -> None:
        out, gen = render_config.apply_env(EXAMPLE, {"POOL_DATA_DIR": "/data"})
        self.assertIsNotNone(gen)
        self.assertEqual(len(gen or ""), 48)
        self.assertIn(f'admin_key: "{gen}"', out)
        self.assertNotIn('admin_key: "change-me"', out)

    def test_nested_upstream_and_limits(self) -> None:
        out, _ = render_config.apply_env(
            EXAMPLE,
            {
                "ADMIN_KEY": "adm",
                "UPSTREAM_BASE_URL": "https://example.test/v1",
                "MAX_CONCURRENT": "99",
                "LOG_LEVEL": "debug",
            },
        )
        self.assertIn('base_url: "https://example.test/v1"', out)
        self.assertIn("max_concurrent: 99", out)
        self.assertIn('level: "debug"', out)

    def test_string_value_escaped(self) -> None:
        out, _ = render_config.apply_env(
            EXAMPLE,
            {
                "ADMIN_KEY": 'a"b\\c',
                "API_KEY": "sk-test",
            },
        )
        self.assertIn('admin_key: "a\\"b\\\\c"', out)
        self.assertIn('api_key: "sk-test"', out)

    def test_main_writes_file(self) -> None:
        with tempfile.TemporaryDirectory() as td:
            path = Path(td) / "config.yaml"
            path.write_text(EXAMPLE, encoding="utf-8")
            env = {
                "HOT_SIZE": "3000",
                "ADMIN_KEY": "adm-fixed",
                "POOL_DATA_DIR": "/data",
            }
            old = dict(os.environ)
            try:
                os.environ.clear()
                os.environ.update(env)
                rc = render_config.main([str(path)])
            finally:
                os.environ.clear()
                os.environ.update(old)
            self.assertEqual(rc, 0)
            body = path.read_text(encoding="utf-8")
            self.assertIn("hot_size: 3000", body)
            self.assertIn('admin_key: "adm-fixed"', body)


if __name__ == "__main__":
    unittest.main()
