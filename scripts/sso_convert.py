#!/usr/bin/env python3
"""SSO cookie → Grok credential converter (HTTP sidecar).

Implements the same /v1/convert batch API expected by poolctl/ssoimport.Client:

  POST /v1/convert
  Authorization: Bearer <API_KEY>
  {"items":[{"sso":"...","source":"entry-1"}, ...]}
  → {"results":[{"index":0,"ok":true,"credential":{...}}, ...]}

Uses xAI Device Flow with the SSO cookie (accounts.x.ai / auth.x.ai),
matching internal/ssoimport LocalConverter.

Usage:
  export SSO_CONVERTER_API_KEY=change-me
  python3 scripts/sso_convert.py --listen 127.0.0.1:8091

  # then import:
  poolctl import-sso --db ./data/pool.db --in sso.txt \\
    --converter-url http://127.0.0.1:8091 --api-key "$SSO_CONVERTER_API_KEY" --allow-insecure

Or set admin imports.sso_converter.endpoint + api_key to this service.
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from http.cookiejar import CookieJar
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

OIDC_ISSUER = "https://auth.x.ai"
OIDC_CLIENT_ID = "b1a00492-073a-47ea-816f-4c329264a828"
SCOPES = (
    "openid profile email offline_access "
    "grok-cli:access api:access conversations:read conversations:write"
)
UA = "grokbuild-pool-sso-py/1.0"

# simple process-wide throttle for verify+approve
_flow_lock = threading.Lock()
_flow_sem: threading.Semaphore | None = None
_last_flow = 0.0
_flow_gap = 0.5


def _b64url_json(seg: str) -> dict[str, Any]:
    pad = "=" * (-len(seg) % 4)
    try:
        raw = base64.urlsafe_b64decode(seg + pad)
        return json.loads(raw.decode("utf-8", "replace"))
    except Exception:
        return {}


def _jwt_payload(token: str) -> dict[str, Any]:
    parts = token.split(".")
    if len(parts) < 2:
        return {}
    return _b64url_json(parts[1])


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):  # noqa: ANN001
        return None


def _opener_with_sso(sso: str) -> urllib.request.OpenerDirector:
    jar = CookieJar()
    # inject sso cookies for both hosts
    from http.cookiejar import Cookie

    def add(host: str) -> None:
        for name in ("sso", "sso-rw"):
            jar.set_cookie(
                Cookie(
                    version=0,
                    name=name,
                    value=sso,
                    port=None,
                    port_specified=False,
                    domain=host,
                    domain_specified=True,
                    domain_initial_dot=False,
                    path="/",
                    path_specified=True,
                    secure=True,
                    expires=None,
                    discard=True,
                    comment=None,
                    comment_url=None,
                    rest={},
                    rfc2109=False,
                )
            )

    add("accounts.x.ai")
    add("auth.x.ai")
    return urllib.request.build_opener(
        urllib.request.HTTPCookieProcessor(jar),
        urllib.request.HTTPSHandler(),
        _NoRedirect(),
    )


def _request(
    opener: urllib.request.OpenerDirector,
    method: str,
    url: str,
    data: bytes | None = None,
    headers: dict[str, str] | None = None,
    timeout: float = 45.0,
) -> tuple[int, bytes, dict[str, str]]:
    req = urllib.request.Request(url, data=data, method=method)
    req.add_header("User-Agent", UA)
    if headers:
        for k, v in headers.items():
            req.add_header(k, v)
    try:
        with opener.open(req, timeout=timeout) as resp:
            body = resp.read(1 << 20)
            return resp.getcode() or 0, body, dict(resp.headers.items())
    except urllib.error.HTTPError as e:
        body = e.read(1 << 20) if e.fp else b""
        return e.code, body, dict(e.headers.items()) if e.headers else {}


def _acquire_flow(concurrency: int, gap: float) -> None:
    global _flow_sem, _last_flow, _flow_gap
    if _flow_sem is None:
        _flow_sem = threading.Semaphore(max(1, concurrency))
    _flow_gap = gap
    _flow_sem.acquire()
    with _flow_lock:
        now = time.time()
        wait = _flow_gap - (now - _last_flow)
        if wait > 0:
            time.sleep(wait)
        _last_flow = time.time()


def _release_flow() -> None:
    if _flow_sem is not None:
        _flow_sem.release()


def convert_one(
    sso: str,
    *,
    concurrency: int = 2,
    gap: float = 0.5,
    poll_timeout: float = 90.0,
    retries: int = 3,
) -> dict[str, Any]:
    sso = (sso or "").strip()
    if not sso:
        return {"ok": False, "error": "missing sso cookie"}

    last_err = "conversion failed"
    for attempt in range(1, max(1, retries) + 1):
        try:
            cred = _convert_once(sso, concurrency=concurrency, gap=gap, poll_timeout=poll_timeout)
            return {"ok": True, "credential": cred}
        except Exception as e:  # noqa: BLE001
            last_err = str(e)
            msg = last_err.lower()
            retriable = any(
                x in msg
                for x in (
                    "rate_limited",
                    "timeout",
                    "connection",
                    "temporary",
                    "eof",
                    "device code",
                    "token poll",
                    "429",
                )
            )
            if not retriable or attempt >= retries:
                break
            time.sleep(min(8.0, float(attempt)))
    return {"ok": False, "error": last_err}


def _convert_once(
    sso: str,
    *,
    concurrency: int,
    gap: float,
    poll_timeout: float,
) -> dict[str, Any]:
    opener = _opener_with_sso(sso)

    # 1) touch accounts.x.ai (establish session)
    code, body, _ = _request(opener, "GET", "https://accounts.x.ai/")
    if code >= 400 and code not in (301, 302, 303, 307, 308):
        raise RuntimeError(f"accounts.x.ai: status {code}: {body[:200]!r}")

    # 2) device code
    form = urllib.parse.urlencode(
        {"client_id": OIDC_CLIENT_ID, "scope": SCOPES}
    ).encode()
    code, body, _ = _request(
        opener,
        "POST",
        f"{OIDC_ISSUER}/oauth2/device/code",
        data=form,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    if code < 200 or code >= 300:
        raise RuntimeError(f"device code: status {code}: {body[:300]!r}")
    sess = json.loads(body.decode("utf-8", "replace"))
    device_code = sess.get("device_code") or ""
    user_code = sess.get("user_code") or ""
    ver_complete = sess.get("verification_uri_complete") or ""
    interval = int(sess.get("interval") or 2)
    if not device_code or not user_code:
        raise RuntimeError(f"device code missing fields: {sess}")

    # 3-5) verify + approve (throttled)
    _acquire_flow(concurrency, gap)
    try:
        if ver_complete:
            _request(opener, "GET", ver_complete)
        form = urllib.parse.urlencode({"user_code": user_code}).encode()
        code, body, _ = _request(
            opener,
            "POST",
            f"{OIDC_ISSUER}/oauth2/device/verify",
            data=form,
            headers={"Content-Type": "application/x-www-form-urlencoded"},
        )
        if code >= 400 and code not in (301, 302, 303, 307, 308):
            raise RuntimeError(f"device/verify: status {code}: {body[:300]!r}")

        form = urllib.parse.urlencode(
            {
                "user_code": user_code,
                "action": "allow",
                "principal_type": "User",
                "principal_id": "",
            }
        ).encode()
        code, body, _ = _request(
            opener,
            "POST",
            f"{OIDC_ISSUER}/oauth2/device/approve",
            data=form,
            headers={"Content-Type": "application/x-www-form-urlencoded"},
        )
        if code >= 400 and code not in (301, 302, 303, 307, 308):
            raise RuntimeError(f"device/approve: status {code}: {body[:300]!r}")
    finally:
        _release_flow()

    # 6) poll token
    deadline = time.time() + poll_timeout
    token: dict[str, Any] = {}
    while time.time() < deadline:
        form = urllib.parse.urlencode(
            {
                "grant_type": "urn:ietf:params:oauth:grant-type:device_code",
                "device_code": device_code,
                "client_id": OIDC_CLIENT_ID,
            }
        ).encode()
        code, body, _ = _request(
            opener,
            "POST",
            f"{OIDC_ISSUER}/oauth2/token",
            data=form,
            headers={"Content-Type": "application/x-www-form-urlencoded"},
        )
        try:
            token = json.loads(body.decode("utf-8", "replace"))
        except Exception as e:  # noqa: BLE001
            raise RuntimeError(f"token poll: bad json status {code}: {body[:200]!r}") from e
        if token.get("access_token") or token.get("key"):
            break
        err = (token.get("error") or "").lower()
        if err in ("authorization_pending", "slow_down"):
            time.sleep(max(1, interval if err != "slow_down" else interval + 2))
            continue
        if err:
            raise RuntimeError(f"token poll: {err}: {token.get('error_description') or body[:200]!r}")
        time.sleep(max(1, interval))
    else:
        raise RuntimeError("token poll: timeout")

    access = (token.get("access_token") or token.get("key") or "").strip()
    refresh = (token.get("refresh_token") or "").strip()
    if not access:
        raise RuntimeError("empty access_token")
    payload = _jwt_payload(access)
    user_id = str(payload.get("sub") or payload.get("principal_id") or "")
    email = str(payload.get("email") or "")
    exp = None
    if isinstance(payload.get("exp"), (int, float)):
        exp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(float(payload["exp"])))
    elif isinstance(token.get("expires_in"), (int, float)):
        exp = time.strftime(
            "%Y-%m-%dT%H:%M:%SZ",
            time.gmtime(time.time() + float(token["expires_in"])),
        )
    else:
        exp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + 6 * 3600))

    return {
        "key": access,
        "refresh_token": refresh,
        "email": email,
        "user_id": user_id,
        "expires_at": exp,
        "oidc_issuer": OIDC_ISSUER,
        "oidc_client_id": OIDC_CLIENT_ID,
    }


class Handler(BaseHTTPRequestHandler):
    api_key = "change-me"
    workers = 4
    concurrency = 2
    gap = 0.5

    def log_message(self, fmt: str, *args: Any) -> None:  # noqa: A003
        sys.stderr.write("%s - %s\n" % (self.address_string(), fmt % args))

    def _auth_ok(self) -> bool:
        auth = self.headers.get("Authorization") or ""
        return auth == f"Bearer {self.api_key}"

    def do_GET(self) -> None:  # noqa: N802
        if self.path in ("/", "/healthz"):
            body = b"ok\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/plain")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self) -> None:  # noqa: N802
        if self.path.rstrip("/") != "/v1/convert":
            self.send_response(404)
            self.end_headers()
            return
        if not self._auth_ok():
            self.send_response(401)
            self.end_headers()
            self.wfile.write(b'{"error":"unauthorized"}')
            return
        length = int(self.headers.get("Content-Length") or "0")
        if length > 8 << 20:
            self.send_response(413)
            self.end_headers()
            return
        raw = self.rfile.read(length) if length else b"{}"
        try:
            body = json.loads(raw.decode("utf-8"))
        except Exception:
            self.send_response(400)
            self.end_headers()
            self.wfile.write(b'{"error":"invalid json"}')
            return
        items = body.get("items") or []
        if not isinstance(items, list):
            self.send_response(400)
            self.end_headers()
            self.wfile.write(b'{"error":"items must be array"}')
            return

        results: list[dict[str, Any] | None] = [None] * len(items)

        def work(i: int, item: dict[str, Any]) -> tuple[int, dict[str, Any]]:
            sso = ""
            if isinstance(item, dict):
                sso = str(item.get("sso") or "")
            out = convert_one(
                sso,
                concurrency=self.concurrency,
                gap=self.gap,
            )
            if out.get("ok"):
                return i, {"index": i, "ok": True, "credential": out["credential"]}
            return i, {"index": i, "ok": False, "error": out.get("error") or "failed"}

        with ThreadPoolExecutor(max_workers=max(1, self.workers)) as ex:
            futs = [ex.submit(work, i, it if isinstance(it, dict) else {}) for i, it in enumerate(items)]
            for fut in as_completed(futs):
                i, row = fut.result()
                results[i] = row

        payload = json.dumps({"results": results}).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def main() -> int:
    ap = argparse.ArgumentParser(description="Python SSO→Grok converter sidecar for poolctl")
    ap.add_argument("--listen", default="127.0.0.1:8091", help="host:port (default 127.0.0.1:8091)")
    ap.add_argument(
        "--api-key",
        default=os.environ.get("SSO_CONVERTER_API_KEY", "change-me"),
        help="Bearer token expected by clients (env SSO_CONVERTER_API_KEY)",
    )
    ap.add_argument("--workers", type=int, default=4, help="parallel convert workers")
    ap.add_argument("--flow-concurrency", type=int, default=2, help="max concurrent verify+approve")
    ap.add_argument("--flow-gap", type=float, default=0.5, help="min seconds between flow starts")
    args = ap.parse_args()

    host, _, port_s = args.listen.partition(":")
    port = int(port_s or "8091")
    Handler.api_key = args.api_key
    Handler.workers = max(1, args.workers)
    Handler.concurrency = max(1, args.flow_concurrency)
    Handler.gap = max(0.0, args.flow_gap)

    httpd = ThreadingHTTPServer((host or "127.0.0.1", port), Handler)
    print(
        f"sso_convert listening on http://{host or '127.0.0.1'}:{port}/v1/convert "
        f"(api_key set={bool(args.api_key and args.api_key != 'change-me')})",
        flush=True,
    )
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\nbye", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
