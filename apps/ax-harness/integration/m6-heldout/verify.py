#!/usr/bin/env python3
"""Check a held-out CSV against a fresh database-derived oracle.

Uses ordinary libpq PG* environment variables. The oracle query stays on the
trusted host and is never included in the agent's prompt or sandbox bundle.
"""

import argparse
import csv
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys


FIELDS = ["email", "full_name", "first_seen_utc", "sources", "occurrences"]
ORACLE = Path(__file__).with_name("oracle.sql")
REFERENCE_SQL = 'COPY (SELECT id, label FROM "references" ORDER BY id) TO STDOUT WITH (FORMAT CSV, HEADER TRUE);'
FROZEN_SNAPSHOT_SHA256 = "6be7827bc6103868c95850c729db173dac43c76f717cc7f8ba2e4e01dbd30970"


def rows_from_csv(data: str) -> list[dict[str, str]]:
    reader = csv.DictReader(io.StringIO(data, newline=""))
    if reader.fieldnames != FIELDS:
        raise ValueError("CSV columns differ from the frozen artifact contract")
    rows = list(reader)
    if any(None in row or None in row.values() for row in rows):
        raise ValueError("CSV has malformed rows")
    return rows


def oracle() -> tuple[bytes, list[dict[str, str]]]:
    result = subprocess.run(
        ["psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-f", str(ORACLE)],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
    )
    if result.returncode:
        raise ValueError("database oracle query failed")
    data = result.stdout
    return data, rows_from_csv(data.decode("utf-8"))


def reference_snapshot() -> bytes:
    result = subprocess.run(
        ["psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-c", REFERENCE_SQL],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
    )
    if result.returncode or result.stdout != b"id,label\n42,REF-42\n":
        raise ValueError("reference fixture changed")
    return result.stdout


def check_fixture(rows: list[dict[str, str]]) -> dict[str, int]:
    by_email = {row["email"]: row for row in rows}
    if len(rows) != 1016 or len(by_email) != len(rows) or list(by_email) != sorted(by_email):
        raise ValueError("held-out data count or ordering changed")
    if "previous@example.test" in by_email or "next@example.test" in by_email:
        raise ValueError("adjacent-day record entered the target day")
    expected = {
        "boundary@example.test": ("Boundary Lead", "2026-09-15T00:00:00Z", "web", "1"),
        "lead0001@example.test": ("CRM Lead 0001", "2026-09-15T07:00:01Z", "crm|event|web", "3"),
        "lead0051@example.test": ("Web Lead 0051", "2026-09-15T08:00:51Z", "event|web", "2"),
        "lead0201@example.test": ("Web Lead 0201", "2026-09-15T08:03:21Z", "web", "1"),
        "lead1001@example.test": ("Event Lead 1001", "2026-09-15T12:00:01Z", "event", "1"),
        "lead1011@example.test": ("CRM Lead 1011", "2026-09-15T13:00:01Z", "crm", "1"),
    }
    for email, values in expected.items():
        row = by_email.get(email)
        if row is None or tuple(row[field] for field in FIELDS[1:]) != values:
            raise ValueError("held-out edge-case oracle changed")
    multi_source = sum("|" in row["sources"] for row in rows)
    triple_source = sum(row["sources"] == "crm|event|web" for row in rows)
    if multi_source != 200 or triple_source != 50:
        raise ValueError("held-out overlap counts changed")
    return {"rows": len(rows), "multi_source": multi_source, "triple_source": triple_source}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifact", type=Path, help="CSV produced by the held-out agent run")
    args = parser.parse_args()
    oracle_bytes, expected = oracle()
    stats = check_fixture(expected)
    reference_bytes = reference_snapshot()
    snapshot = hashlib.sha256(b"daily-lead-union-v1\0" + oracle_bytes + b"references-v1\0" + reference_bytes)
    if snapshot.hexdigest() != FROZEN_SNAPSHOT_SHA256:
        raise ValueError("frozen held-out data snapshot changed")
    result: dict[str, str | int | bool] = {
        "dataset": "daily-lead-union-v1",
        "data_snapshot_sha256": snapshot.hexdigest(),
        **stats,
    }
    if args.artifact is not None:
        if args.artifact.stat().st_size > 16 << 20:
            raise ValueError("artifact exceeds held-out CSV limit")
        artifact_bytes = args.artifact.read_bytes()
        actual = rows_from_csv(artifact_bytes.decode("utf-8-sig"))
        if actual != expected:
            raise ValueError("agent artifact differs from database-derived oracle")
        result["artifact_sha256"] = hashlib.sha256(artifact_bytes).hexdigest()
        result["artifact_verified"] = True
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, UnicodeError, ValueError) as exc:
        print(f"held-out verification failed: {exc}", file=sys.stderr)
        sys.exit(1)
