#!/usr/bin/env python3
"""Bind a server-observed GraphJin agent profile to the frozen DB snapshot."""

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
from urllib.error import URLError
from urllib.parse import urlparse
from urllib.request import Request, urlopen


VERIFY = Path(__file__).with_name("verify.py")
SHA256 = re.compile(r"[0-9a-f]{64}\Z")


def snapshot_hash() -> str:
    result = subprocess.run([sys.executable, str(VERIFY)], stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, check=False)
    if result.returncode:
        raise ValueError("frozen database snapshot verification failed")
    report = json.loads(result.stdout)
    value = report.get("data_snapshot_sha256")
    if not isinstance(value, str) or not SHA256.fullmatch(value):
        raise ValueError("database snapshot report has no digest")
    return value


def server_status(url: str, token_env: str | None) -> dict:
    parsed = urlparse(url)
    if (parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or parsed.password
            or not parsed.path.endswith("/api/v1/agent/status") or parsed.query or parsed.fragment):
        raise ValueError("invalid GraphJin status URL")
    if parsed.scheme == "http" and parsed.hostname not in ("localhost", "127.0.0.1", "::1"):
        raise ValueError("remote GraphJin status requires HTTPS")
    headers = {"accept": "application/json"}
    if token_env:
        token = os.environ.get(token_env, "")
        if not token:
            raise ValueError("status token environment variable is missing")
        headers["authorization"] = "Bearer " + token
    try:
        with urlopen(Request(url, headers=headers), timeout=10) as response:
            body = response.read(65537)
    except (OSError, URLError) as exc:
        raise ValueError("GraphJin status request failed") from exc
    if len(body) > 65536:
        raise ValueError("GraphJin status response is too large")
    status = json.loads(body)
    if not isinstance(status, dict):
        raise ValueError("GraphJin status response is not an object")
    return status


def attest(status: dict, provider: str, model: str, reasoning: str, snapshot: str) -> dict[str, str]:
    if not provider or not model or not reasoning or not SHA256.fullmatch(snapshot):
        raise ValueError("approved profile or data snapshot is incomplete")
    if any(status.get(flag) is not True for flag in
           ("enabled", "ready", "rest_ready", "server_model_ready", "api_key_configured", "read_only")):
        raise ValueError("GraphJin server agent is not ready and read-only")
    for field, expected in (("provider", provider), ("model", model), ("reasoning", reasoning)):
        if status.get(field) != expected:
            raise ValueError("GraphJin effective server profile differs from the approved profile")
    fingerprint = status.get("eval_fingerprint")
    if not isinstance(fingerprint, str) or not SHA256.fullmatch(fingerprint):
        raise ValueError("GraphJin server has no evaluation fingerprint")
    return {"provider": provider, "model": model, "reasoning": reasoning,
            "eval_fingerprint": fingerprint, "data_snapshot_sha256": snapshot}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True, help="GraphJin /api/v1/agent/status URL")
    parser.add_argument("--provider", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--reasoning", required=True)
    parser.add_argument("--token-env", help="environment variable containing a status bearer token")
    args = parser.parse_args()
    profile = attest(server_status(args.url, args.token_env), args.provider, args.model,
                     args.reasoning, snapshot_hash())
    print(json.dumps(profile, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"GraphJin attestation failed: {exc}", file=sys.stderr)
        sys.exit(1)
