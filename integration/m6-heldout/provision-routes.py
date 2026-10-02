#!/usr/bin/env python3
"""Write non-secret OpenShell profiles and a pinned Ax routing manifest for M6."""

import argparse
import json
from pathlib import Path
from urllib.parse import urlparse


def endpoint(raw: str) -> tuple[str, dict]:
    url = urlparse(raw)
    if (url.scheme != "https" or not url.hostname or url.username or url.password
            or url.query or url.fragment or not url.path.startswith("/") or ".." in url.path):
        raise ValueError("real model routes must use a clean HTTPS base URL")
    path = url.path.rstrip("/")
    if not path:
        raise ValueError("model route URL needs a versioned path")
    return raw.rstrip("/"), {"host": url.hostname, "port": url.port or 443,
                               "protocol": "rest", "enforcement": "enforce",
                               "access": "read-write", "path": path + "/**"}


def price(input_micros: int, output_micros: int) -> dict[str, int]:
    if input_micros <= 0 or output_micros <= 0:
        raise ValueError("all real route prices must be positive")
    return {"input_micros_per_million": input_micros,
            "output_micros_per_million": output_micros}


def profile(name: str, credential: str, allowed: dict) -> dict:
    return {"id": name, "category": "agent", "display_name": name,
            "credentials": [{"name": credential, "env_vars": [credential], "required": True}],
            "endpoints": [allowed], "binaries": ["/usr/local/bin/harness-openneko"]}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True, type=Path)
    parser.add_argument("--model-url", required=True)
    parser.add_argument("--model-name", required=True)
    parser.add_argument("--triage-url", required=True)
    parser.add_argument("--triage-model", required=True)
    parser.add_argument("--model-input-price", required=True, type=int)
    parser.add_argument("--model-output-price", required=True, type=int)
    parser.add_argument("--triage-input-price", required=True, type=int)
    parser.add_argument("--triage-output-price", required=True, type=int)
    parser.add_argument("--graphjin-input-price", required=True, type=int)
    parser.add_argument("--graphjin-output-price", required=True, type=int)
    args = parser.parse_args()
    model_url, model_endpoint = endpoint(args.model_url)
    triage_url, triage_endpoint = endpoint(args.triage_url)
    if not args.model_name or not args.triage_model:
        raise ValueError("model names are required")
    model_price = price(args.model_input_price, args.model_output_price)
    triage_price = price(args.triage_input_price, args.triage_output_price)
    graphjin_price = price(args.graphjin_input_price, args.graphjin_output_price)
    args.output_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    (args.output_dir / "model-provider.json").write_text(json.dumps(profile(
        "harness-m6-model", "HARNESS_M6_MODEL_SOURCE_KEY", model_endpoint), indent=2) + "\n")
    (args.output_dir / "triage-provider.json").write_text(json.dumps(profile(
        "harness-m6-triage", "HARNESS_M6_TRIAGE_SOURCE_KEY", triage_endpoint), indent=2) + "\n")
    routing = {
        "context": "model", "executor": "model", "responder": "model", "triage": "triage",
        "pricing_version": "m6-live-v1", "graphjin_price": graphjin_price,
        "budget_policy": {
            "version": "m6-shadow-v1",
            "short": {"max_model_calls": 4, "max_model_tokens": 12000, "max_cost_micros": 5000},
            "multi_step": {"max_model_calls": 4, "max_model_tokens": 40000, "max_cost_micros": 20000},
            "artifact": {"max_model_calls": 48, "max_model_tokens": 100000,
                         "max_cost_micros": 1000000},
        },
        "routes": [
            {"key": "model", "model": args.model_name, "url": model_url,
             "provider": "harness-m6-model", "credential_env": "HARNESS_M6_MODEL_SOURCE_KEY",
             "api_key_env": "HARNESS_M6_MODEL_KEY", "price": model_price},
            {"key": "triage", "model": args.triage_model, "url": triage_url,
             "provider": "harness-m6-triage", "credential_env": "HARNESS_M6_TRIAGE_SOURCE_KEY",
             "api_key_env": "HARNESS_M6_TRIAGE_KEY", "price": triage_price},
        ],
    }
    (args.output_dir / "routing.json").write_text(json.dumps(routing, separators=(",", ":")) + "\n")


if __name__ == "__main__":
    main()
