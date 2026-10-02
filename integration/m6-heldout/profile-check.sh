#!/usr/bin/env bash
# Validate the real-route profile shape and env-key provisioning on isolated OpenShell 0.1.2.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${OPENSHELL_TEST_CLI:?}"
: "${HARNESS_STATE:?}"
python3 integration/m6-heldout/provision-routes.py --output-dir "$HARNESS_STATE/m6-route-check" \
  --model-url https://example.invalid/v1/chat --model-name synthetic-model \
  --triage-url https://typesafe.example.invalid --triage-model synthetic-triage \
  --model-input-price 1000000 --model-output-price 2000000 \
  --triage-input-price 300000 --triage-output-price 400000 \
  --graphjin-input-price 500000 --graphjin-output-price 600000
for kind in model triage; do
  "$OPENSHELL_TEST_CLI" --gateway harness-m2 provider profile lint \
    --file "$HARNESS_STATE/m6-route-check/$kind-provider.json"
  "$OPENSHELL_TEST_CLI" --gateway harness-m2 provider profile import \
    --file "$HARNESS_STATE/m6-route-check/$kind-provider.json"
done
export HARNESS_M6_MODEL_SOURCE_KEY=synthetic-model-key
export HARNESS_M6_TRIAGE_SOURCE_KEY=synthetic-triage-key
"$OPENSHELL_TEST_CLI" --gateway harness-m2 provider create \
  --name harness-m6-model --type harness-m6-model --credential HARNESS_M6_MODEL_SOURCE_KEY
"$OPENSHELL_TEST_CLI" --gateway harness-m2 provider create \
  --name harness-m6-triage --type harness-m6-triage --credential HARNESS_M6_TRIAGE_SOURCE_KEY
unset HARNESS_M6_MODEL_SOURCE_KEY HARNESS_M6_TRIAGE_SOURCE_KEY
python3 - "$HARNESS_STATE/m6-route-check/routing.json" "$HARNESS_STATE/m6-route-check/triage-provider.json" <<'PY'
import json, sys
routing = json.load(open(sys.argv[1], encoding="utf-8"))
assert routing["context"] == routing["executor"] == routing["responder"] == "model"
assert routing["triage"] == "triage"
assert routing["routes"][0]["provider"] == "harness-m6-model"
assert routing["routes"][1]["provider"] == "harness-m6-triage"
assert routing["routes"][1]["url"] == "https://typesafe.example.invalid"
assert routing["budget_policy"]["version"] == "m6-shadow-v1"
profile = json.load(open(sys.argv[2], encoding="utf-8"))
assert profile["endpoints"][0]["path"] == "/v1/**"
PY
echo M6_REAL_ROUTE_PROFILE_PREFLIGHT_PASS
