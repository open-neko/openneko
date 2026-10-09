#!/usr/bin/env python3
"""Join independently reviewed M6 outcomes to the eight durable run receipts."""

import argparse
import hashlib
import json
from pathlib import Path
import sys


CASES = (
    "reference-short-001", "lead-count-misleading-001",
    "lead-overlap-investigation-001", "lead-csv-artifact-001",
)
MODES = ("fixed", "canary")
SHA256 = "7edca56ef2e16aa175e9b880b314112370331c8708dad221cb1250db1d57156e"
OUTCOMES = {"verified_success", "verified_failure", "unverified"}
CLASSES = {"short", "investigation", "artifact"}


def receipt(path: Path, case_id: str, mode: str, source: str) -> dict:
    data = json.loads(path.read_text(encoding="utf-8"))
    if (data.get("version") != 1 or data.get("dataset") != "daily-lead-union-v1"
            or data.get("cases_sha256") != SHA256 or data.get("case_id") != case_id
            or data.get("mode") != mode or data.get("source") != source):
        raise ValueError(f"invalid frozen receipt identity: {path.name}")
    model_name = data.get("model_name")
    if not isinstance(model_name, str) or not model_name:
        raise ValueError(f"missing model identity: {path.name}")
    root = Path(data.get("root", ""))
    run_id = data.get("run_id")
    digest = data.get("checkpoint_sha256")
    if not root.is_absolute() or not isinstance(run_id, str) or not run_id:
        raise ValueError(f"missing checkpoint identity: {path.name}")
    if not isinstance(digest, str) or len(digest) != 64:
        raise ValueError(f"missing checkpoint digest: {path.name}")
    checkpoint = root / (hashlib.sha256(run_id.encode()).hexdigest() + ".json")
    if not checkpoint.is_file() or hashlib.sha256(checkpoint.read_bytes()).hexdigest() != digest:
        raise ValueError(f"checkpoint changed or vanished: {path.name}")
    environment = data.get("graphjin_environment")
    if not isinstance(environment, dict) or any(not isinstance(environment.get(key), str)
            or not environment[key] for key in ("provider", "model", "reasoning",
                                           "eval_fingerprint", "data_snapshot_sha256")):
        raise ValueError(f"missing GraphJin attestation: {path.name}")
    if not isinstance(data.get("wall_ms"), int) or data["wall_ms"] <= 0:
        raise ValueError(f"missing wall time: {path.name}")
    if source == "live" and ("fixture" in model_name.lower()
                             or "fixture" in environment["model"].lower()):
        raise ValueError(f"fixture profile cannot support live outcome: {path.name}")
    return data


def entry(data: dict, verdict: dict, case_id: str) -> dict:
    if verdict.get("reviewed") is not True or verdict.get("outcome") not in OUTCOMES:
        raise ValueError(f"missing independent outcome review: {case_id}/{data['mode']}")
    outcome = verdict["outcome"]
    if outcome == "verified_success" and (data.get("api_status") != "completed"
            or data.get("checkpoint_status") != "completed"
            or case_id == "lead-csv-artifact-001" and data.get("artifact_verified") is not True):
        raise ValueError(f"unverified successful outcome: {case_id}/{data['mode']}")
    return {"root": data["root"], "run_id": data["run_id"],
            "checkpoint_sha256": data["checkpoint_sha256"], "outcome": outcome,
            "wall_ms": data["wall_ms"], "graphjin_environment": data["graphjin_environment"]}


def build(receipts: Path, review: dict, source: str) -> dict:
    if source not in ("live", "synthetic"):
        raise ValueError("invalid held-out run source")
    if review.get("version") != 1 or set(review.get("cases", {})) != set(CASES):
        raise ValueError("review must cover exactly the four frozen cases")
    pairs = []
    environment = None
    for case_id in CASES:
        case_review = review["cases"][case_id]
        task_class = case_review.get("task_class")
        if task_class not in CLASSES or set(case_review.get("runs", {})) != set(MODES):
            raise ValueError(f"incomplete reviewed pair: {case_id}")
        runs = {}
        for mode in MODES:
            data = receipt(receipts / f"{case_id}-{mode}.json", case_id, mode, source)
            runs[mode] = entry(data, case_review["runs"][mode], case_id)
        if runs["fixed"]["graphjin_environment"] != runs["canary"]["graphjin_environment"]:
            raise ValueError(f"GraphJin profile or snapshot changed across pair: {case_id}")
        if environment is None:
            environment = runs["fixed"]["graphjin_environment"]
        elif runs["fixed"]["graphjin_environment"] != environment:
            raise ValueError(f"GraphJin profile or snapshot changed across cases: {case_id}")
        pairs.append({"id": case_id, "split": "held_out", "source": source,
                      "task_class": task_class, **runs})
    return {"version": 1, "pairs": pairs}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipts", required=True, type=Path)
    parser.add_argument("--review", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--source", choices=("live", "synthetic"), required=True)
    args = parser.parse_args()
    review = json.loads(args.review.read_text(encoding="utf-8"))
    manifest = build(args.receipts, review, source=args.source)
    with args.output.open("x", encoding="utf-8") as output:
        json.dump(manifest, output, indent=2)
        output.write("\n")
    args.output.chmod(0o600)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"held-out manifest rejected: {exc}", file=sys.stderr)
        sys.exit(1)
