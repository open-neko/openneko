"""Verify real OTLP collector delivery without printing raw span attributes."""
import json
import sys
from pathlib import Path


def check(data):
    # The exporter appends while live. Ignore its incomplete final write, but
    # require complete, valid observations from both actual services.
    raw = data[:data.rfind(b"\n") + 1].decode("utf-8")
    for secret in ("synthetic-M2-credential", "synthetic-M2-rotated", "synthetic-M2-alternate", "synthetic-M2-refreshed", "synthetic-refresh-secret", "openshell:resolve:"):
        assert secret not in raw, "credential leaked into exported telemetry"
    services = set()
    spans = 0
    for line in raw.splitlines():
        for resource in json.loads(line).get("resourceSpans", []):
            service = next((a["value"].get("stringValue") for a in resource.get("resource", {}).get("attributes", []) if a["key"] == "service.name"), None)
            for scope in resource.get("scopeSpans", []):
                for span in scope.get("spans", []):
                    assert span.get("traceId") and span.get("spanId"), "missing correlation IDs"
                    spans += 1
                    services.add(service)
    assert {"harness-m2-gateway", "openshell-driver-docker"} <= services, "missing gateway or Docker driver spans"
    return {"check": "otlp_collector_delivery", "ok": True, "services": sorted(services), "spans": spans}


def self_test():
    rows = [{"resourceSpans": [{"resource": {"attributes": [{"key": "service.name", "value": {"stringValue": service}}]}, "scopeSpans": [{"spans": [{"traceId": "trace", "spanId": "span"}]}]}]} for service in ("harness-m2-gateway", "openshell-driver-docker")]
    complete = ("\n".join(json.dumps(row) for row in rows) + "\n").encode()
    assert check(complete + b'{"partial":')['spans'] == 2
    for invalid in (b'', complete.replace(b'span"', b'synthetic-refresh-secret"'), complete + b'broken\n'):
        try:
            check(invalid)
        except (AssertionError, ValueError):
            continue
        raise AssertionError("invalid telemetry accepted")


if __name__ == "__main__":
    if sys.argv[1:] == ["--self-test"]:
        self_test()
    else:
        print(json.dumps(check(Path(sys.argv[1]).read_bytes())))
