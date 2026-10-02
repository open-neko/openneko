"""Synthetic workflow query-to-file fixture; model calls stay with Harness/Ax."""

import argparse
import csv
import hashlib
import json
import os
import socket
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.request import urlopen

parser = argparse.ArgumentParser()
for name in ("target-day", "work-dir", "output", "summary", "max-runtime"):
    parser.add_argument("--" + name, required=True)
args = vars(parser.parse_args())

assert "MODEL_API_KEY" not in os.environ
assert "OPENNEKO_BROKER_TOKEN" not in os.environ
try:
    urlopen("http://model-fixture:8080/v1/chat/completions", timeout=5)
    raise AssertionError("model egress must be denied")
except HTTPError as denied:
    assert denied.code == 403
except URLError as denied:
    assert isinstance(denied.reason, (socket.gaierror, PermissionError)), denied.reason

query = "query { references { id label } }"
query_id = hashlib.sha256(query.encode()).hexdigest()
cache = Path(os.environ["OPENNEKO_QUERY_CACHE_DIR"])
response = cache / "responses" / (query_id + ".json")
if not response.exists():
    request = {
        "schema_version": 1,
        "id": query_id,
        "tool": "mcp_neko_graphjin_execute_graphql",
        "arguments": {"query": query},
        "response_path": str(response),
    }
    (cache / "requests" / (query_id + ".json")).write_text(json.dumps(request))
    raise SystemExit(4)

rows = json.loads(response.read_text())["data"]["references"]
assert rows == [{"id": 42, "label": "REF-42"}]
output_rows = 1_000_000 if args["target_day"] == "2026-09-16" else 1
with open(args["output"], "w", newline="") as output:
    writer = csv.writer(output)
    writer.writerow(["reference"])
    for _ in range(output_rows):
        writer.writerow([rows[0]["label"]])
Path(args["summary"]).write_text(json.dumps({
    "status": "completed",
    "target_day": args["target_day"],
    "merge": {"final_rows": output_rows},
}))
