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
export HARNESS_M6_MODEL_NAME=harness-budget-short-fixture
cases=(reference-short-001)
modes=(fixed)
if [[ ${HARNESS_M6_HELDOUT_SMOKE_ALL:-0} == 1 ]]; then
  cases=(reference-short-001 lead-count-misleading-001 lead-overlap-investigation-001 lead-csv-artifact-001)
  modes=(fixed canary)
elif [[ ${HARNESS_M6_HELDOUT_SMOKE_CANARY_ONLY:-0} == 1 ]]; then
  modes=(canary)
fi
for case_id in "${cases[@]}"; do
  for mode in "${modes[@]}"; do
    export HARNESS_M6_CASE_ID="$case_id" HARNESS_M6_MODE="$mode"
    export OPENNEKO_HARNESS_BUDGET_CANARY=0
    if [[ "$mode" == canary ]]; then export OPENNEKO_HARNESS_BUDGET_CANARY=1; fi
    triage_choice=multi_step
    case "$case_id" in
      reference-short-001|lead-count-misleading-001) triage_choice=short_answer ;;
      lead-csv-artifact-001) triage_choice=artifact_pipeline ;;
    esac
    curl -fsS -X POST -H 'content-type: application/json' \
      -d "{\"triage_choice\":\"$triage_choice\"}" \
      http://127.0.0.1:18118/control >/dev/null
    export HARNESS_M6_RUN_REPORT="$HARNESS_STATE/$case_id-$mode.json"
    run_log="$HARNESS_STATE/$case_id-$mode.log"
    if ! (cd "$OPENNEKO_TEST_SOURCE" && pnpm --filter @neko/worker exec tsx scripts/harness-heldout-live.ts) > "$run_log" 2>&1; then
      tail -n 80 "$run_log" >&2
      exit 1
    fi
    echo "M6_HELDOUT_SMOKE_RUN $case_id $mode"
  done
done
python3 - "$HARNESS_STATE" "${HARNESS_M6_HELDOUT_SMOKE_ALL:-0}" "${HARNESS_M6_HELDOUT_SMOKE_CANARY_ONLY:-0}" <<'PY'
import hashlib, json, pathlib, sys
root, all_cases, canary_only = pathlib.Path(sys.argv[1]), sys.argv[2] == "1", sys.argv[3] == "1"
cases = ["reference-short-001"] if not all_cases else [
    "reference-short-001", "lead-count-misleading-001",
    "lead-overlap-investigation-001", "lead-csv-artifact-001"]
review = {"version": 1, "cases": {}}
for case_id in cases:
    review_case = {"task_class": "artifact" if case_id == "lead-csv-artifact-001"
                   else "short" if case_id == "reference-short-001" else "investigation",
                   "runs": {}}
    for mode in (["fixed", "canary"] if all_cases else ["canary"] if canary_only else ["fixed"]):
        report = json.loads((root / f"{case_id}-{mode}.json").read_text(encoding="utf-8"))
        assert report["api_status"] == report["checkpoint_status"] and report["api_status"] in ("completed", "failed"), report
        assert report["case_id"] == case_id and report["mode"] == mode
        if report["api_status"] == "completed":
            assert report["answer"] == "Recorded the short check finding."
        else:
            assert mode == "canary" and report["checkpoint_code"] == "dynamic_budget_exceeded", report
        assert report["artifact_path"] is None
        assert report["artifact_verified"] is (False if case_id == "lead-csv-artifact-001" else None)
        assert report["graphjin_environment"]["data_snapshot_sha256"] == "6be7827bc6103868c95850c729db173dac43c76f717cc7f8ba2e4e01dbd30970"
        checkpoint = pathlib.Path(report["root"]) / (hashlib.sha256(report["run_id"].encode()).hexdigest() + ".json")
        assert checkpoint.is_file()
        assert hashlib.sha256(checkpoint.read_bytes()).hexdigest() == report["checkpoint_sha256"]
        events = json.loads(checkpoint.read_text(encoding="utf-8"))["events"]
        assert any(event.get("type") == "model.request.finished" and event.get("stage") == "budget_triage" for event in events)
        assert any(event.get("type") == "budget.profile.proposed" for event in events)
        review_case["runs"][mode] = {"reviewed": True, "outcome": "verified_failure"}
    review["cases"][case_id] = review_case
if all_cases:
    (root / "synthetic-review.json").write_text(json.dumps(review), encoding="utf-8")
PY
if [[ ${HARNESS_M6_HELDOUT_SMOKE_ALL:-0} == 1 ]]; then
  python3 integration/m6-heldout/build-manifest.py --receipts "$HARNESS_STATE" \
    --review "$HARNESS_STATE/synthetic-review.json" --source synthetic \
    --output "$HARNESS_STATE/synthetic-manifest.json"
  go run ./cmd/harness-budget-compare < "$HARNESS_STATE/synthetic-manifest.json" > "$HARNESS_STATE/synthetic-comparison.json"
  python3 - "$HARNESS_STATE/synthetic-comparison.json" <<'PY'
import json, sys
report = json.load(open(sys.argv[1], encoding="utf-8"))
assert len(report["pairs"]) == 4
assert all(pair["source"] == "synthetic" and pair["split"] == "held_out" for pair in report["pairs"])
PY
  echo M6_HELDOUT_PAIRED_SMOKE_PASS
else
  echo M6_HELDOUT_QUEUE_SMOKE_PASS
fi
