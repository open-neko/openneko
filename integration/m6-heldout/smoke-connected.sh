#!/usr/bin/env bash
# Exercise the held-out queue runner with synthetic OpenShell/Ax/GraphJin sources.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${OPENNEKO_TEST_SOURCE:?}"
: "${OPENSHELL_TEST_CLI:?}"
: "${HARNESS_STATE:?}"
[[ ${HARNESS_M6_BUSINESS_SEED:-} == "$PWD/integration/m6-heldout/seed.sql" ]] || { echo 'Frozen held-out seed required' >&2; exit 1; }
[[ ${GRAPHJIN_AGENT_REASONING:-} == high ]] || { echo 'GraphJin smoke profile must use high reasoning' >&2; exit 1; }
cat > "$HARNESS_STATE/m6-smoke-model.json" <<'JSON'
{"id":"harness-m6-smoke-model","category":"agent","display_name":"M6 held-out smoke model","credentials":[{"name":"HARNESS_M6_SMOKE_SOURCE_KEY","env_vars":["HARNESS_M6_SMOKE_SOURCE_KEY"],"required":true}],"endpoints":[{"host":"host.docker.internal","port":18118,"protocol":"rest","enforcement":"enforce","access":"read-write","path":"/v1/**"}],"binaries":["/usr/local/bin/harness-openneko"]}
JSON
cat > "$HARNESS_STATE/m6-smoke-triage.json" <<'JSON'
{"id":"harness-m6-smoke-triage","category":"agent","display_name":"M6 held-out smoke triage","credentials":[{"name":"HARNESS_TRIAGE_SOURCE_KEY","env_vars":["HARNESS_TRIAGE_SOURCE_KEY"],"required":true}],"endpoints":[{"host":"host.docker.internal","port":18118,"protocol":"rest","enforcement":"enforce","access":"read-write","path":"/route/triage/v1/**"}],"binaries":["/usr/local/bin/harness-openneko"]}
JSON
"$OPENSHELL_TEST_CLI" --gateway harness-m2 provider profile import --file "$HARNESS_STATE/m6-smoke-model.json"
"$OPENSHELL_TEST_CLI" --gateway harness-m2 provider create --name harness-m6-smoke-model --type harness-m6-smoke-model --credential HARNESS_M6_SMOKE_SOURCE_KEY=synthetic-m3
"$OPENSHELL_TEST_CLI" --gateway harness-m2 provider profile import --file "$HARNESS_STATE/m6-smoke-triage.json"
"$OPENSHELL_TEST_CLI" --gateway harness-m2 provider create --name harness-m6-smoke-triage --type harness-m6-smoke-triage --credential HARNESS_TRIAGE_SOURCE_KEY=synthetic-m6-triage
export OPENNEKO_HARNESS_ROUTING='{"context":"fixture","executor":"fixture","responder":"fixture","triage":"triage","budget_policy":{"version":"m6-shadow-v1","short":{"max_model_calls":4,"max_model_tokens":12000,"max_cost_micros":5000},"multi_step":{"max_model_calls":4,"max_model_tokens":40000,"max_cost_micros":20000},"artifact":{"max_model_calls":48,"max_model_tokens":100000,"max_cost_micros":1000000}},"pricing_version":"m6-heldout-smoke-v1","graphjin_price":{"input_micros_per_million":1000000,"output_micros_per_million":1000000},"routes":[{"key":"fixture","model":"harness-budget-short-fixture","url":"http://host.docker.internal:18118/v1","provider":"harness-m6-smoke-model","credential_env":"HARNESS_M6_SMOKE_SOURCE_KEY","api_key_env":"HARNESS_M6_SMOKE_KEY","price":{"input_micros_per_million":1000000,"output_micros_per_million":1000000}},{"key":"triage","model":"jev-fixture","url":"http://host.docker.internal:18118/route/triage","provider":"harness-m6-smoke-triage","credential_env":"HARNESS_TRIAGE_SOURCE_KEY","api_key_env":"HARNESS_TRIAGE_KEY","price":{"input_micros_per_million":1000000,"output_micros_per_million":1000000}}]}'
export OPENNEKO_HARNESS_TRIAGE_SHADOW=1 OPENNEKO_HARNESS_BUDGET_CANARY=0
mkdir -p "$HARNESS_STATE/bin"
ln -sfn "$HARNESS_M3_CLI" "$HARNESS_STATE/bin/openshell"
export PATH="$HARNESS_STATE/bin:$PATH" OPENNEKO_AGENT_BACKEND=harness OPENNEKO_AGENT_IMAGE=harness-openneko:m3 OPENNEKO_AGENT_WARM_POOL_SIZE=0 OPENSHELL_GATEWAY=harness-m2
export OPENNEKO_AGENT_MODEL_PROVIDER=harness-m6-smoke-model OPENNEKO_AGENT_HERMES_HOME="$HARNESS_STATE/provider-config" OPENNEKO_AGENT_MODEL_HOST=http://host.docker.internal:18118
export OPENNEKO_AGENT_HOME="$HARNESS_STATE/m6-smoke-agent-home" OPENNEKO_BROKER_PORT=18123 OPENNEKO_HOST_WEB_DEV=1 NODE_ENV=development
cat > "$HARNESS_STATE/provider-config/config.yaml" <<'YAML'
model:
  provider: custom
  default: harness-budget-short-fixture
  base_url: http://host.docker.internal:18118/v1
YAML
(cd "$OPENNEKO_TEST_SOURCE" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
export HARNESS_M6_CASES_FILE="$PWD/integration/m6-heldout/cases.json"
export HARNESS_M6_ATTEST_SCRIPT="$PWD/integration/m6-heldout/attest.py"
export HARNESS_M6_VERIFY_SCRIPT="$PWD/integration/m6-heldout/verify.py"
export HARNESS_M6_GJ_STATUS_URL=http://127.0.0.1:18117/api/v1/agent/status
export HARNESS_M6_GJ_PROVIDER=openai-compatible HARNESS_M6_GJ_MODEL=graphjin-fixture HARNESS_M6_GJ_REASONING=high
export PGHOST=127.0.0.1 PGPORT=18120 PGUSER=fixture PGPASSWORD=fixture PGDATABASE=fixture
export HARNESS_M6_MODEL_NAME=harness-budget-short-fixture HARNESS_M6_CASE_ID=reference-short-001 HARNESS_M6_MODE=fixed
export HARNESS_M6_RUN_REPORT="$HARNESS_STATE/m6-smoke-report.json"
(cd "$OPENNEKO_TEST_SOURCE" && pnpm --filter @neko/worker exec tsx scripts/harness-heldout-live.ts)
python3 - "$HARNESS_M6_RUN_REPORT" <<'PY'
import hashlib, json, pathlib, sys
report = json.load(open(sys.argv[1], encoding="utf-8"))
assert report["api_status"] == report["checkpoint_status"] == "completed", report
assert report["case_id"] == "reference-short-001" and report["mode"] == "fixed"
assert "short check" in report["answer"]
assert report["artifact_path"] is None and report["artifact_verified"] is None
assert report["graphjin_environment"]["data_snapshot_sha256"] == "6be7827bc6103868c95850c729db173dac43c76f717cc7f8ba2e4e01dbd30970"
checkpoint = pathlib.Path(report["root"]) / (hashlib.sha256(report["run_id"].encode()).hexdigest() + ".json")
assert checkpoint.is_file()
assert hashlib.sha256(checkpoint.read_bytes()).hexdigest() == report["checkpoint_sha256"]
events = json.loads(checkpoint.read_text(encoding="utf-8"))["events"]
assert any(event.get("type") == "model.request.finished" and event.get("stage") == "budget_triage" for event in events)
assert any(event.get("type") == "budget.profile.proposed" for event in events)
PY
echo M6_HELDOUT_QUEUE_SMOKE_PASS
