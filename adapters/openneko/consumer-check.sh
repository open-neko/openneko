#!/usr/bin/env bash
set -euo pipefail
product=${OPENNEKO_TEST_SOURCE:?}
cli=${OPENSHELL_TEST_CLI:?}
go build -o "$HARNESS_STATE/openshell-compat" ./adapters/openneko/cmd/openshell-compat
go build -o "$HARNESS_STATE/harness-inspect" ./cmd/harness-inspect
go build -o "$HARNESS_STATE/harness-batch" ./adapters/openneko/cmd/batch
go build -o "$HARNESS_STATE/harness-process" ./adapters/openneko/cmd/process
export HARNESS_INSPECT_BIN="$HARNESS_STATE/harness-inspect"
mkdir -p "$HARNESS_STATE/workflow-bundle"
cp integration/batch/workflow-fixture.py "$HARNESS_STATE/workflow-bundle/run.py"
export HARNESS_M3_BATCH_BIN="$HARNESS_STATE/harness-batch" HARNESS_M3_BATCH_SCRIPT="$HARNESS_STATE/workflow-bundle/run.py"
export HARNESS_BATCH_EXECUTOR_REGISTRY="$HARNESS_STATE/batch-registry.json"
export HARNESS_OPENSHELL_BIN="$cli" HARNESS_M3_CLI="$HARNESS_STATE/openshell-compat"
cat > "$HARNESS_STATE/m3-provider.yaml" <<'YAML'
id: harness-m3
category: agent
display_name: Harness M3 fixture
credentials:
  - name: api_key
    env_vars: [MODEL_API_KEY]
    required: true
endpoints:
  - host: host.docker.internal
    port: 18118
    protocol: rest
    enforcement: enforce
    access: read-write
    path: /v1/**
binaries: [/usr/local/bin/harness-openneko]
YAML
"$cli" --gateway harness-m2 provider profile import --file "$HARNESS_STATE/m3-provider.yaml"
"$cli" --gateway harness-m2 provider create --name harness-m3 --type harness-m3 --credential api_key=synthetic-m3
bash ./integration/batch/batch-check.sh
export HARNESS_M3_LIVE=1 OPENNEKO_PG_ENV_OVERRIDE=1 NEKO_PG_HOST=127.0.0.1 NEKO_PG_PORT=18119 NEKO_PG_USER=neko NEKO_PG_PASSWORD=synthetic-m3 NEKO_PG_DATABASE=neko
(cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-live.test.ts test/harness-memory-live.test.ts test/harness-memory-write-live.test.ts test/harness-run-journal-live.test.ts test/harness-operation-live.test.ts test/harness-proposal-live.test.ts test/harness-effect-live.test.ts test/integration/action-flow.test.ts test/integration/workflow-store.test.ts test/integration/audit-viewer.test.ts)
export RECORDS_PG_HOST=127.0.0.1 RECORDS_PG_PORT=18120 RECORDS_PG_USER=fixture RECORDS_PG_PASSWORD=fixture RECORDS_PG_DATABASE=fixture
(cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-records-live.test.ts)
(cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-records-data-live.test.ts)
(cd "$product" && pnpm --filter @neko/worker exec vitest run test/jobs/harness-batch-live.test.ts)
(cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-approval-sandbox-live.test.ts)
(cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-remote-cancel-live.test.ts)

# Qualify the unchanged Hermes cold and warm paths on the same gateway/image.
sed -e 's/harness-m3/harness-hermes/g' -e 's|/usr/local/bin/harness-openneko|/usr/bin/python3.11|g' "$HARNESS_STATE/m3-provider.yaml" > "$HARNESS_STATE/hermes-provider.yaml"
"$cli" --gateway harness-m2 provider profile import --file "$HARNESS_STATE/hermes-provider.yaml"
"$cli" --gateway harness-m2 provider create --name harness-hermes --type harness-hermes --credential api_key=synthetic-m3
(cd "$product" && pnpm --filter @neko/llm exec vitest run test/hermes-live.test.ts)
(cd "$product" && pnpm --filter @neko/worker exec vitest run test/jobs/work-run-memory-fence.test.ts)
(cd "$product" && pnpm --filter @neko/worker exec vitest run test/reconciler.test.ts)

  mkdir -p "$HARNESS_STATE/bin"
  ln -s "$HARNESS_M3_CLI" "$HARNESS_STATE/bin/openshell"
  export PATH="$HARNESS_STATE/bin:$PATH" OPENNEKO_AGENT_BACKEND=harness OPENNEKO_AGENT_IMAGE=harness-openneko:m3 OPENNEKO_AGENT_WARM_POOL_SIZE=0 OPENSHELL_GATEWAY=harness-m2
  export HARNESS_PROCESS_BIN="$HARNESS_STATE/harness-process" HARNESS_PROCESS_IMAGE=harness-openneko:m3
  export HARNESS_PROCESS_BIN_SHA256=$(python3 -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$HARNESS_PROCESS_BIN")
  export OPENNEKO_AGENT_MODEL_PROVIDER=harness-m3 OPENNEKO_AGENT_HERMES_HOME="$HARNESS_STATE/provider-config" OPENNEKO_AGENT_MODEL_HOST=http://host.docker.internal:18118
  export RECORDS_PG_HOST=127.0.0.1 RECORDS_PG_PORT=18119 RECORDS_PG_USER=neko RECORDS_PG_PASSWORD=synthetic-m3 RECORDS_PG_DATABASE=neko
  export OPENNEKO_HOST_WEB_DEV=1 NODE_ENV=development OPENNEKO_AGENT_HOME="$HARNESS_STATE/user" WORKER_ADMIN_URL=http://127.0.0.1:18122 OPENNEKO_BROKER_PORT=18123
  docker compose -p harness-m3 -f integration/openneko/compose.yml restart model
  if [[ ${HARNESS_M3_API_HTTP:-0} == 1 ]]; then
    export HARNESS_M3_WORKFLOW_ID=$(python3 -c 'import uuid; print(uuid.uuid4())')
    [[ ! -d "$product/apps/web/.next/dev" ]] || mv "$product/apps/web/.next/dev" "$HARNESS_STATE/next-dev-cache"
    set -m
    (cd "$product" && exec pnpm --filter @neko/web exec next dev --port 18121) > /tmp/harness-m3-web.log 2>&1 &
    web_pid=$!
    set +m
    trap 'kill -TERM -- "-$web_pid" 2>/dev/null || true; wait "$web_pid" 2>/dev/null || true' EXIT
    ready=0
    for ((n=0; n<90; n++)); do
      if curl -sS --max-time 3 -o /dev/null http://localhost:18121/ 2>/dev/null; then ready=1; break; fi
      kill -0 "$web_pid" || exit 1
      sleep 1
    done
    [[ "$ready" == 1 ]] || { echo 'Isolated workflow API web server did not start' >&2; exit 1; }
  fi
  (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts)
  if [[ ${HARNESS_M3_API_HTTP:-0} == 1 ]]; then
    process_run=$(cat "$HARNESS_STATE/m5-process-run")
    process_url="http://localhost:18121/api/work/files/runs/$process_run/artifacts/process-1/result.csv"
    curl -fsS --max-time 10 -D "$HARNESS_STATE/process.headers" -o "$HARNESS_STATE/process.csv" "$process_url"
    printf 'lead_id\nLEAD-42\n' | cmp -s - "$HARNESS_STATE/process.csv"
    rg -qi '^content-disposition: attachment; filename="result.csv"' "$HARNESS_STATE/process.headers"
    [[ $(curl -sS -o /dev/null -w '%{http_code}' "${process_url%result.csv}unissued.csv") == 404 ]]
    echo "M5_WEB_PROCESS_PASS $process_run"
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-process-artifact.mjs "$(cat "$HARNESS_STATE/m5-process-thread")")
  fi
  docker compose -p harness-m3 -f integration/openneko/compose.yml restart model
  (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-workflow-child-live.ts)
  docker compose -p harness-m3 -f integration/openneko/compose.yml restart model
  (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-agent-job-child-live.ts)
if [[ ${HARNESS_M3_WEB:-0} == 1 ]]; then
  docker compose -p harness-m3 -f integration/openneko/compose.yml restart model
  if [[ ${HARNESS_M3_API_HTTP:-0} != 1 ]]; then
    [[ ! -d "$product/apps/web/.next/dev" ]] || mv "$product/apps/web/.next/dev" "$HARNESS_STATE/next-dev-cache"
    # Own the whole process group: terminating pnpm alone leaves Next listening.
    set -m
    (cd "$product" && exec pnpm --filter @neko/web exec next dev --port 18121) > /tmp/harness-m3-web.log 2>&1 &
    web_pid=$!
    set +m
    trap 'kill -TERM -- "-$web_pid" 2>/dev/null || true; wait "$web_pid" 2>/dev/null || true' EXIT
  fi
  artifact_run=$(cat "$HARNESS_STATE/m5-artifact-run")
  artifact_url="http://localhost:18121/api/work/files/runs/$artifact_run/artifacts/result.csv"
  for ((n=0; n<60; n++)); do
    if curl -fsS --max-time 5 -D "$HARNESS_STATE/artifact.headers" -o "$HARNESS_STATE/artifact.csv" "$artifact_url" 2>/dev/null; then break; fi
    kill -0 "$web_pid" || exit 1
    sleep 1
  done
  printf 'lead_id\nLEAD-42\n' | cmp -s - "$HARNESS_STATE/artifact.csv"
  rg -qi '^content-disposition: attachment; filename="result.csv"' "$HARNESS_STATE/artifact.headers"
  rg -qi '^content-type: text/csv' "$HARNESS_STATE/artifact.headers"
  [[ $(curl -sS -o /dev/null -w '%{http_code}' "${artifact_url%result.csv}hidden.txt") == 404 ]]
  echo "M5_WEB_ARTIFACT_PASS $artifact_run"
  if [[ ${HARNESS_M3_API_HTTP:-0} != 1 ]]; then
    process_run=$(cat "$HARNESS_STATE/m5-process-run")
    process_url="http://localhost:18121/api/work/files/runs/$process_run/artifacts/process-1/result.csv"
    curl -fsS --max-time 10 -D "$HARNESS_STATE/process.headers" -o "$HARNESS_STATE/process.csv" "$process_url"
    printf 'lead_id\nLEAD-42\n' | cmp -s - "$HARNESS_STATE/process.csv"
    rg -qi '^content-disposition: attachment; filename="result.csv"' "$HARNESS_STATE/process.headers"
    echo "M5_WEB_PROCESS_PASS $process_run"
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-process-artifact.mjs "$(cat "$HARNESS_STATE/m5-process-thread")")
  fi
  (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-card-reload.mjs "$(cat "$HARNESS_STATE/m5-card-thread")")
  batch_run=$(cat "$HARNESS_STATE/m5-batch-workflow-run")
  batch_url="http://localhost:18121/api/workflow-runs/$batch_run/artifact"
  curl -fsS --max-time 10 -D "$HARNESS_STATE/batch.headers" -o "$HARNESS_STATE/batch.csv" "$batch_url"
  printf 'reference\r\nREF-42\r\n' | cmp -s - "$HARNESS_STATE/batch.csv"
  rg -qi '^content-disposition: attachment; filename="references.csv"' "$HARNESS_STATE/batch.headers"
  rg -qi '^content-type: text/csv' "$HARNESS_STATE/batch.headers"
  [[ $(curl -sS -o /dev/null -w '%{http_code}' "http://localhost:18121/api/workflow-runs/00000000-0000-0000-0000-000000000000/artifact") == 404 ]]
  echo "M5_WEB_BATCH_PASS $batch_run"
  echo "M3_WEB_READY state=$HARNESS_STATE"
  for ((n=0; n<900; n++)); do
    [[ ! -f "$HARNESS_STATE/web-done" ]] || break
    kill -0 "$web_pid" || exit 1
    sleep 2
  done
  [[ -f "$HARNESS_STATE/web-done" ]] || { echo "Browser acceptance timed out" >&2; exit 1; }
fi
