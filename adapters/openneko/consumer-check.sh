#!/usr/bin/env bash
set -euo pipefail
product=${OPENNEKO_TEST_SOURCE:?}
cli=${OPENSHELL_TEST_CLI:?}
if [[ ${HARNESS_M6_ARTIFACT_SCOPE_ONLY:-0} == 1 ]]; then
  export OPENNEKO_PG_ENV_OVERRIDE=1 NEKO_PG_HOST=127.0.0.1 NEKO_PG_PORT=18119 NEKO_PG_USER=neko NEKO_PG_PASSWORD=synthetic-m3 NEKO_PG_DATABASE=neko
  (cd "$product" && pnpm --filter @neko/web exec vitest run test/api/workflow-artifact-org-scope.test.ts)
  echo M6_WORKFLOW_ARTIFACT_ORG_SCOPE_PASS
  exit 0
fi
if [[ ${HARNESS_OPENSHELL_VERSION:-0.1.2} == 0.0.116 ]]; then
  go build -o "$HARNESS_STATE/openshell-compat" ./adapters/openneko/cmd/openshell-compat
  export HARNESS_OPENSHELL_BIN="$cli" HARNESS_M3_CLI="$HARNESS_STATE/openshell-compat"
else
  # OpenNeko main now invokes the 0.1.x CLI contract directly. The legacy
  # 0.0.116 adapter deliberately rejects this breaking release.
  export HARNESS_OPENSHELL_BIN="$cli" HARNESS_M3_CLI="$cli"
fi
go build -o "$HARNESS_STATE/harness-inspect" ./cmd/harness-inspect
go build -o "$HARNESS_STATE/harness-batch" ./adapters/openneko/cmd/batch
go build -o "$HARNESS_STATE/harness-process" ./adapters/openneko/cmd/process
export HARNESS_INSPECT_BIN="$HARNESS_STATE/harness-inspect"
mkdir -p "$HARNESS_STATE/workflow-bundle"
cp integration/batch/workflow-fixture.py "$HARNESS_STATE/workflow-bundle/run.py"
export HARNESS_M3_BATCH_BIN="$HARNESS_STATE/harness-batch" HARNESS_M3_BATCH_SCRIPT="$HARNESS_STATE/workflow-bundle/run.py"
export HARNESS_BATCH_EXECUTOR_REGISTRY="$HARNESS_STATE/batch-registry.json"
cat > "$HARNESS_STATE/m3-provider.yaml" <<'YAML'
id: harness-m3
category: agent
display_name: Harness M3 fixture
credentials:
  - name: api_key
    env_vars: [api_key]
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
if [[ ${HARNESS_M6_HERMES_ONLY:-0} == 1 && ${HARNESS_M5_FAST:-0} == 1 ]]; then
  sed -e 's/harness-m3/harness-hermes/g' -e 's|/usr/local/bin/harness-openneko|/usr/bin/python3.11|g' "$HARNESS_STATE/m3-provider.yaml" > "$HARNESS_STATE/hermes-provider.yaml"
  "$cli" --gateway harness-m2 provider profile import --file "$HARNESS_STATE/hermes-provider.yaml"
  "$cli" --gateway harness-m2 provider create --name harness-hermes --type harness-hermes --credential api_key=synthetic-m3
  export HARNESS_M3_LIVE=1 OPENNEKO_PG_ENV_OVERRIDE=1 NEKO_PG_HOST=127.0.0.1 NEKO_PG_PORT=18119 NEKO_PG_USER=neko NEKO_PG_PASSWORD=synthetic-m3 NEKO_PG_DATABASE=neko
  (cd "$product" && pnpm --filter @neko/llm exec vitest run test/hermes-live.test.ts)
  echo M6_CONNECTED_OPENSHELL_HERMES_PASS
  exit 0
fi
workflow_child_priced_route() {
  export OPENNEKO_HARNESS_ROUTING='{"context":"fixture","executor":"fixture","responder":"fixture","pricing_version":"m5-workflow-fixture-v1","graphjin_price":{"input_micros_per_million":1000000,"output_micros_per_million":1000000},"routes":[{"key":"fixture","model":"harness-workflow-child-fixture","url":"http://host.docker.internal:18118/v1","provider":"harness-m5-workflow-priced","credential_env":"HARNESS_WORKFLOW_SOURCE_KEY","api_key_env":"HARNESS_WORKFLOW_KEY","price":{"input_micros_per_million":1000000,"output_micros_per_million":1000000}}]}'
}
register_workflow_priced_provider() {
  cat > "$HARNESS_STATE/m5-workflow-priced-provider.yaml" <<'YAML'
id: harness-m5-workflow-priced
category: agent
display_name: Harness workflow priced fixture
credentials:
  - name: HARNESS_WORKFLOW_SOURCE_KEY
    env_vars: [HARNESS_WORKFLOW_SOURCE_KEY]
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
  "$cli" --gateway harness-m2 provider profile import --file "$HARNESS_STATE/m5-workflow-priced-provider.yaml"
  "$cli" --gateway harness-m2 provider create --name harness-m5-workflow-priced --type harness-m5-workflow-priced --credential HARNESS_WORKFLOW_SOURCE_KEY=synthetic-workflow
}
if [[ ${HARNESS_M6_ROUTING_ONLY:-0} == 1 ]]; then
  bash ./integration/openneko/routing-check.sh
  if [[ ${HARNESS_M6_DIRECT_ONLY:-0} == 1 ]]; then exit 0; fi
  curl -fsS -X POST -d '{}' http://127.0.0.1:18118/control >/dev/null
  export HARNESS_M3_LIVE=1 OPENNEKO_PG_ENV_OVERRIDE=1 NEKO_PG_HOST=127.0.0.1 NEKO_PG_PORT=18119 NEKO_PG_USER=neko NEKO_PG_PASSWORD=synthetic-m3 NEKO_PG_DATABASE=neko
  (cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-routing-live.test.ts)
  echo M6_CONNECTED_OPENNEKO_ROUTE_LAUNCH_PASS
  exit 0
fi
mkdir -p "$HARNESS_STATE/provider-config"
cat > "$HARNESS_STATE/provider-config/config.yaml" <<'YAML'
model:
  provider: custom
  default: harness-fixture
  base_url: http://host.docker.internal:18118/v1
YAML
if [[ ${HARNESS_M5_FAST:-0} != 1 ]]; then
  bash ./integration/batch/batch-check.sh
fi
export HARNESS_M3_LIVE=1 OPENNEKO_PG_ENV_OVERRIDE=1 NEKO_PG_HOST=127.0.0.1 NEKO_PG_PORT=18119 NEKO_PG_USER=neko NEKO_PG_PASSWORD=synthetic-m3 NEKO_PG_DATABASE=neko
if [[ ${HARNESS_M3_LIVE_ONLY:-0} == 1 ]]; then
  (cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-live.test.ts)
  echo M3_CONNECTED_HARNESS_LIVE_PASS
  exit 0
fi
if [[ ( ${HARNESS_M6_BROWSER_STREAM:-0} == 1 || ${HARNESS_M6_QUEUE_BROWSER_STREAM:-0} == 1 ) && ${HARNESS_M5_FAST:-0} == 1 ]]; then
  if lsof -nP -iTCP:18121 -sTCP:LISTEN >/dev/null 2>&1; then
    echo 'Port 18121 is already in use; refusing to test against an existing web server' >&2
    exit 1
  fi
  (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
  [[ ! -d "$product/apps/web/.next/dev" ]] || mv "$product/apps/web/.next/dev" "$HARNESS_STATE/m6-stream-next-dev-cache"
  set -m
  (cd "$product" && exec pnpm --filter @neko/web exec next dev --port 18121) > "$HARNESS_STATE/m6-stream-web.log" 2>&1 &
  web_pid=$!
  set +m
  trap 'kill -TERM -- "-$web_pid" 2>/dev/null || true; wait "$web_pid" 2>/dev/null || true' EXIT
  ready=0
  for ((n=0; n<90; n++)); do
    if curl -sS --max-time 3 -o /dev/null http://localhost:18121/ 2>/dev/null; then ready=1; break; fi
    kill -0 "$web_pid" || exit 1
    sleep 1
  done
  [[ "$ready" == 1 ]] || { echo 'Isolated M6 web server did not start' >&2; exit 1; }
  export OPENNEKO_HARNESS_STREAM_RESPONSES=1
  if [[ ${HARNESS_M6_QUEUE_BROWSER_STREAM:-0} == 1 ]]; then
    cat > "$HARNESS_STATE/provider-config/config.yaml" <<'YAML'
model:
  provider: custom
  default: harness-stream-fixture
  base_url: http://host.docker.internal:18118/v1
YAML
    mkdir -p "$HARNESS_STATE/bin"
    ln -sfn "$HARNESS_M3_CLI" "$HARNESS_STATE/bin/openshell"
    export PATH="$HARNESS_STATE/bin:$PATH" OPENNEKO_AGENT_BACKEND=harness OPENNEKO_AGENT_IMAGE=harness-openneko:m3 OPENNEKO_AGENT_WARM_POOL_SIZE=0 OPENSHELL_GATEWAY=harness-m2
    export OPENNEKO_AGENT_MODEL_PROVIDER=harness-m3 OPENNEKO_AGENT_HERMES_HOME="$HARNESS_STATE/provider-config" OPENNEKO_AGENT_MODEL_HOST=http://host.docker.internal:18118
    export OPENNEKO_HOST_WEB_DEV=1 OPENNEKO_AGENT_HOME="$HARNESS_STATE/stream-agent-home" OPENNEKO_BROKER_PORT=18123
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-streaming-queue-browser-live.ts)
    echo M6_CONNECTED_QUEUE_BROWSER_STREAMING_PASS
  else
    (cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-streaming-browser-live.test.ts)
    echo M6_CONNECTED_BROWSER_STREAMING_PASS
  fi
  exit 0
fi
if [[ ${HARNESS_M6_STREAMING_ONLY:-0} == 1 && ${HARNESS_M5_FAST:-0} == 1 ]]; then
  (cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-streaming-live.test.ts)
  echo M6_CONNECTED_OPENSHELL_STREAMING_PASS
  exit 0
fi
if [[ ${HARNESS_M5_PACK_EFFECT_ONLY:-0} == 1 && ${HARNESS_M5_FAST:-0} == 1 ]]; then
  (cd "$product" && pnpm --filter @neko/worker exec vitest run test/declarative-pack-effect-live.test.ts)
  echo M5_CONNECTED_PACK_EFFECT_PASS
  exit 0
fi
if [[ ${HARNESS_M5_WS_ONLY:-0} == 1 && ${HARNESS_M5_FAST:-0} == 1 ]]; then
  export RECORDS_PG_PORT=18120
  (cd "$product" && pnpm --filter @neko/llm exec vitest run test/source-change-websocket-live.test.ts)
  echo M5_CONNECTED_GRAPHJIN_WEBSOCKET_PASS
  exit 0
fi
if [[ ${HARNESS_M5_SKILL_ONLY:-0} == 1 && ${HARNESS_M5_FAST:-0} == 1 ]]; then
  (cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-skill-create-live.test.ts)
  echo M5_SKILL_CREATE_PASS
  exit 0
fi
if [[ ${HARNESS_M5_FAST:-0} != 1 ]]; then
(cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-live.test.ts test/harness-memory-live.test.ts test/harness-memory-write-live.test.ts test/harness-skill-create-live.test.ts test/harness-run-journal-live.test.ts test/harness-operation-live.test.ts test/harness-proposal-live.test.ts test/harness-effect-live.test.ts test/integration/action-flow.test.ts test/integration/workflow-store.test.ts test/integration/audit-viewer.test.ts)
(cd "$product" && pnpm --filter @neko/worker exec vitest run test/declarative-pack-effect-live.test.ts)
export RECORDS_PG_HOST=127.0.0.1 RECORDS_PG_PORT=18120 RECORDS_PG_USER=fixture RECORDS_PG_PASSWORD=fixture RECORDS_PG_DATABASE=fixture
(cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-records-live.test.ts)
mkdir -p "$HARNESS_STATE/bin"
ln -sfn "$HARNESS_M3_CLI" "$HARNESS_STATE/bin/openshell"
export PATH="$HARNESS_STATE/bin:$PATH" OPENNEKO_AGENT_BACKEND=harness OPENNEKO_AGENT_IMAGE=harness-openneko:m3 OPENNEKO_AGENT_WARM_POOL_SIZE=0 OPENSHELL_GATEWAY=harness-m2
export OPENNEKO_AGENT_MODEL_PROVIDER=harness-m3 OPENNEKO_AGENT_HERMES_HOME="$HARNESS_STATE/provider-config" OPENNEKO_AGENT_MODEL_HOST=http://host.docker.internal:18118
export OPENNEKO_HOST_WEB_DEV=1 OPENNEKO_AGENT_HOME="$HARNESS_STATE/records-agent-home" OPENNEKO_BROKER_PORT=18123
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
fi
if [[ ${HARNESS_M5_APPROVAL:-0} == 1 && ${HARNESS_M5_FAST:-0} == 1 ]]; then
  (cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-approval-sandbox-live.test.ts)
  if [[ ${HARNESS_M5_APPROVAL_ONLY:-0} == 1 ]]; then
    echo M5_APPROVAL_ONLY_PASS
    exit 0
  fi
fi
if [[ ${HARNESS_M5_RECORDS:-0} == 1 && ${HARNESS_M5_FAST:-0} == 1 ]]; then
  mkdir -p "$HARNESS_STATE/bin"
  ln -sfn "$HARNESS_M3_CLI" "$HARNESS_STATE/bin/openshell"
  export PATH="$HARNESS_STATE/bin:$PATH" OPENNEKO_AGENT_BACKEND=harness OPENNEKO_AGENT_IMAGE=harness-openneko:m3 OPENNEKO_AGENT_WARM_POOL_SIZE=0 OPENSHELL_GATEWAY=harness-m2
  export OPENNEKO_AGENT_MODEL_PROVIDER=harness-m3 OPENNEKO_AGENT_HERMES_HOME="$HARNESS_STATE/provider-config" OPENNEKO_AGENT_MODEL_HOST=http://host.docker.internal:18118
  export OPENNEKO_HOST_WEB_DEV=1 OPENNEKO_AGENT_HOME="$HARNESS_STATE/records-agent-home" OPENNEKO_BROKER_PORT=18123
  export RECORDS_PG_HOST=127.0.0.1 RECORDS_PG_PORT=18120 RECORDS_PG_USER=fixture RECORDS_PG_PASSWORD=fixture RECORDS_PG_DATABASE=fixture
  if ! (cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-records-data-live.test.ts); then
    docker compose -p harness-m3 -f integration/openneko/compose.yml logs --tail=60 model >&2 || true
    docker compose -p harness-m2 -f integration/compose.yml logs --tail=60 openshell-gateway >&2 || true
    exit 1
  fi
  if [[ ${HARNESS_M5_RECORDS_ONLY:-0} == 1 ]]; then
    echo M5_RECORDS_ONLY_PASS
    exit 0
  fi
fi
if [[ ${HARNESS_M5_PLUGIN:-0} == 1 && ${HARNESS_M5_FAST:-0} == 1 ]]; then
  docker build -q -f "$product/docker/plugin-base.Dockerfile" -t openneko-plugin:harness-m5 "$product" >/dev/null
  export OPENNEKO_PLUGIN_BASE_IMAGE=openneko-plugin:harness-m5
  mkdir -p "$HARNESS_STATE/bin"
  ln -sfn "$HARNESS_M3_CLI" "$HARNESS_STATE/bin/openshell"
  export PATH="$HARNESS_STATE/bin:$PATH" OPENNEKO_AGENT_BACKEND=harness OPENNEKO_AGENT_IMAGE=harness-openneko:m3 OPENNEKO_AGENT_WARM_POOL_SIZE=0 OPENSHELL_GATEWAY=harness-m2
  export OPENNEKO_AGENT_MODEL_PROVIDER=harness-m3 OPENNEKO_AGENT_HERMES_HOME="$HARNESS_STATE/provider-config" OPENNEKO_AGENT_MODEL_HOST=http://host.docker.internal:18118
  export OPENNEKO_HOST_WEB_DEV=1 OPENNEKO_AGENT_HOME="$HARNESS_STATE/plugin-agent-home" OPENNEKO_BROKER_PORT=18123
  if ! (cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-installed-plugin-live.test.ts); then
    docker compose -p harness-m2 -f integration/compose.yml logs --tail=100 openshell-gateway >&2 || true
    docker ps -a --format '{{.Names}} {{.Status}}' | rg 'openshell-default' >&2 || true
    exit 1
  fi
  if [[ ${HARNESS_M5_PLUGIN_ONLY:-0} == 1 ]]; then
    echo M5_PLUGIN_ONLY_PASS
    exit 0
  fi
fi

  mkdir -p "$HARNESS_STATE/bin"
  ln -sfn "$HARNESS_M3_CLI" "$HARNESS_STATE/bin/openshell"
  export PATH="$HARNESS_STATE/bin:$PATH" OPENNEKO_AGENT_BACKEND=harness OPENNEKO_AGENT_IMAGE=harness-openneko:m3 OPENNEKO_AGENT_WARM_POOL_SIZE=0 OPENSHELL_GATEWAY=harness-m2
  export HARNESS_PROCESS_BIN="$HARNESS_STATE/harness-process" HARNESS_PROCESS_IMAGE=harness-openneko:m3
  export HARNESS_PROCESS_BIN_SHA256=$(python3 -c 'import hashlib,sys; print(hashlib.sha256(open(sys.argv[1],"rb").read()).hexdigest())' "$HARNESS_PROCESS_BIN")
  export OPENNEKO_AGENT_MODEL_PROVIDER=harness-m3 OPENNEKO_AGENT_HERMES_HOME="$HARNESS_STATE/provider-config" OPENNEKO_AGENT_MODEL_HOST=http://host.docker.internal:18118
  export RECORDS_PG_HOST=127.0.0.1 RECORDS_PG_PORT=18119 RECORDS_PG_USER=neko RECORDS_PG_PASSWORD=synthetic-m3 RECORDS_PG_DATABASE=neko
  export OPENNEKO_HOST_WEB_DEV=1 NODE_ENV=development OPENNEKO_AGENT_HOME="$HARNESS_STATE/user" WORKER_ADMIN_URL=http://127.0.0.1:18122 OPENNEKO_BROKER_PORT=18123
  docker compose -p harness-m3 -f integration/openneko/compose.yml restart model
  if [[ ${HARNESS_M5_AGENT_JOB_CHILD_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-agent-job-child-live.ts)
    echo M5_CONNECTED_AGENT_JOB_CHILD_PASS
    exit 0
  fi
  if [[ ${HARNESS_M5_ADMIN_GROUPED:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    for fixture in user-admin group-admin data-source-admin; do
      (cd "$product" && pnpm --filter @neko/worker exec tsx "scripts/harness-${fixture}-live.ts")
    done
    [[ ! -d "$product/apps/web/.next/dev" ]] || mv "$product/apps/web/.next/dev" "$HARNESS_STATE/next-dev-cache"
    set -m
    (cd "$product" && exec pnpm --filter @neko/web exec next dev --port 18121) > /tmp/harness-m5-admin-grouped-next.log 2>&1 &
    web_pid=$!
    set +m
    trap 'kill -TERM -- "-$web_pid" 2>/dev/null || true; wait "$web_pid" 2>/dev/null || true' EXIT
    ready=0
    for ((n=0; n<90; n++)); do
      if curl -sS --max-time 3 -o /dev/null http://localhost:18121/ 2>/dev/null; then ready=1; break; fi
      kill -0 "$web_pid" || exit 1
      sleep 1
    done
    [[ "$ready" == 1 ]] || { echo 'Isolated grouped admin web server did not start' >&2; exit 1; }
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-user-admin-reload.mjs "$(cat "$HARNESS_STATE/m5-user-admin-thread")")
    for action in deactivate reactivate promote; do
      (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-user-state-reload.mjs "$(cat "$HARNESS_STATE/m5-user-${action}-thread")" "$action")
    done
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-group-admin-reload.mjs "$(cat "$HARNESS_STATE/m5-group-admin-thread")")
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-group-member-reload.mjs "$(cat "$HARNESS_STATE/m5-group-member-thread")")
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-data-source-admin-reload.mjs "$(cat "$HARNESS_STATE/m5-data-source-admin-thread")")
    echo M5_CONNECTED_ADMIN_GROUPED_PASS
    exit 0
  fi
  if [[ ${HARNESS_M5_USER_ADMIN_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-user-admin-live.ts)
    [[ ! -d "$product/apps/web/.next/dev" ]] || mv "$product/apps/web/.next/dev" "$HARNESS_STATE/next-dev-cache"
    set -m
    (cd "$product" && exec pnpm --filter @neko/web exec next dev --port 18121) > /tmp/harness-m5-user-admin-next.log 2>&1 &
    web_pid=$!
    set +m
    trap 'kill -TERM -- "-$web_pid" 2>/dev/null || true; wait "$web_pid" 2>/dev/null || true' EXIT
    ready=0
    for ((n=0; n<90; n++)); do
      if curl -sS --max-time 3 -o /dev/null http://localhost:18121/ 2>/dev/null; then ready=1; break; fi
      kill -0 "$web_pid" || exit 1
      sleep 1
    done
    [[ "$ready" == 1 ]] || { echo 'Isolated user-admin web server did not start' >&2; exit 1; }
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-user-admin-reload.mjs "$(cat "$HARNESS_STATE/m5-user-admin-thread")")
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-user-state-reload.mjs "$(cat "$HARNESS_STATE/m5-user-deactivate-thread")" deactivate)
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-user-state-reload.mjs "$(cat "$HARNESS_STATE/m5-user-reactivate-thread")" reactivate)
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-user-state-reload.mjs "$(cat "$HARNESS_STATE/m5-user-promote-thread")" promote)
    echo M5_CONNECTED_USER_ADMIN_PASS
    exit 0
  fi
  if [[ ${HARNESS_M5_DATA_SOURCE_ADMIN_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-data-source-admin-live.ts)
    [[ ! -d "$product/apps/web/.next/dev" ]] || mv "$product/apps/web/.next/dev" "$HARNESS_STATE/next-dev-cache"
    set -m
    (cd "$product" && exec pnpm --filter @neko/web exec next dev --port 18121) > /tmp/harness-m5-data-source-admin-next.log 2>&1 &
    web_pid=$!
    set +m
    trap 'kill -TERM -- "-$web_pid" 2>/dev/null || true; wait "$web_pid" 2>/dev/null || true' EXIT
    ready=0
    for ((n=0; n<90; n++)); do
      if curl -sS --max-time 3 -o /dev/null http://localhost:18121/ 2>/dev/null; then ready=1; break; fi
      kill -0 "$web_pid" || exit 1
      sleep 1
    done
    [[ "$ready" == 1 ]] || { echo 'Isolated data-source-admin web server did not start' >&2; exit 1; }
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-data-source-admin-reload.mjs "$(cat "$HARNESS_STATE/m5-data-source-admin-thread")")
    echo M5_CONNECTED_DATA_SOURCE_ADMIN_PASS
    exit 0
  fi
  if [[ ${HARNESS_M5_GROUP_ADMIN_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-group-admin-live.ts)
    [[ ! -d "$product/apps/web/.next/dev" ]] || mv "$product/apps/web/.next/dev" "$HARNESS_STATE/next-dev-cache"
    set -m
    (cd "$product" && exec pnpm --filter @neko/web exec next dev --port 18121) > /tmp/harness-m5-group-admin-next.log 2>&1 &
    web_pid=$!
    set +m
    trap 'kill -TERM -- "-$web_pid" 2>/dev/null || true; wait "$web_pid" 2>/dev/null || true' EXIT
    ready=0
    for ((n=0; n<90; n++)); do
      if curl -sS --max-time 3 -o /dev/null http://localhost:18121/ 2>/dev/null; then ready=1; break; fi
      kill -0 "$web_pid" || exit 1
      sleep 1
    done
    [[ "$ready" == 1 ]] || { echo 'Isolated group-admin web server did not start' >&2; exit 1; }
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-group-admin-reload.mjs "$(cat "$HARNESS_STATE/m5-group-admin-thread")")
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-group-member-reload.mjs "$(cat "$HARNESS_STATE/m5-group-member-thread")")
    echo M5_CONNECTED_GROUP_ADMIN_PASS
    exit 0
  fi
  if [[ ${HARNESS_M5_SKILL_QUEUE_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-skill-write-live.ts)
    [[ ! -d "$product/apps/web/.next/dev" ]] || mv "$product/apps/web/.next/dev" "$HARNESS_STATE/next-dev-cache"
    set -m
    (cd "$product" && exec pnpm --filter @neko/web exec next dev --port 18121) > /tmp/harness-m5-skill-web.log 2>&1 &
    web_pid=$!
    set +m
    trap 'kill -TERM -- "-$web_pid" 2>/dev/null || true; wait "$web_pid" 2>/dev/null || true' EXIT
    ready=0
    for ((n=0; n<90; n++)); do
      if curl -sS --max-time 3 -o /dev/null http://localhost:18121/ 2>/dev/null; then ready=1; break; fi
      kill -0 "$web_pid" || exit 1
      sleep 1
    done
    [[ "$ready" == 1 ]] || { echo 'Isolated skill-write web server did not start' >&2; exit 1; }
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-skill-write-reload.mjs "$(cat "$HARNESS_STATE/m5-skill-create-thread")" "$(cat "$HARNESS_STATE/m5-skill-update-thread")")
    echo M5_CONNECTED_SKILL_QUEUE_PASS
    exit 0
  fi
  if [[ ${HARNESS_M5_OFFICE_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    [[ ! -d "$product/apps/web/.next/dev" ]] || mv "$product/apps/web/.next/dev" "$HARNESS_STATE/next-dev-cache"
    set -m
    (cd "$product" && exec pnpm --filter @neko/web exec next dev --port 18121) > /tmp/harness-m5-office-web.log 2>&1 &
    web_pid=$!
    set +m
    trap 'kill -TERM -- "-$web_pid" 2>/dev/null || true; wait "$web_pid" 2>/dev/null || true' EXIT
    ready=0
    for ((n=0; n<90; n++)); do
      if curl -sS --max-time 3 -o /dev/null http://localhost:18121/ 2>/dev/null; then ready=1; break; fi
      kill -0 "$web_pid" || exit 1
      sleep 1
    done
    [[ "$ready" == 1 ]] || { echo 'Isolated Office artifact web server did not start' >&2; exit 1; }
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-process-office-live.ts)
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-office-artifacts.mjs "$(cat "$HARNESS_STATE/m5-office-thread")")
    echo M5_CONNECTED_OFFICE_ARTIFACTS_PASS
    exit 0
  fi
  if [[ ${HARNESS_M5_PROCESS_CANCEL_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    if [[ ${HARNESS_M5_PROCESS_TIMEOUT:-0} == 1 ]]; then
      export OPENNEKO_PROCESS_TIMEOUT_SECONDS=2
    fi
    if [[ ${HARNESS_M5_PROCESS_CANCEL_HTTP:-0} == 1 ]]; then
      [[ ! -d "$product/apps/web/.next/dev" ]] || mv "$product/apps/web/.next/dev" "$HARNESS_STATE/next-dev-cache"
      set -m
      (cd "$product" && exec pnpm --filter @neko/web exec next dev --port 18121) > /tmp/harness-m5-stop-web.log 2>&1 &
      web_pid=$!
      set +m
      trap 'kill -TERM -- "-$web_pid" 2>/dev/null || true; wait "$web_pid" 2>/dev/null || true' EXIT
      ready=0
      for ((n=0; n<90; n++)); do
        if curl -sS --max-time 3 -o /dev/null http://localhost:18121/ 2>/dev/null; then ready=1; break; fi
        kill -0 "$web_pid" || exit 1
        sleep 1
      done
      [[ "$ready" == 1 ]] || { echo 'Isolated Work Stop web server did not start' >&2; exit 1; }
    fi
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-process-cancel-live.ts)
    if [[ ${HARNESS_M5_PROCESS_TIMEOUT:-0} == 1 ]]; then
      echo M5_CONNECTED_PROCESS_TIMEOUT_PASS
      exit 0
    fi
    if [[ ${HARNESS_M5_PROCESS_CANCEL_HTTP:-0} == 1 ]]; then
      echo M5_WEB_PROCESS_CANCEL_PASS
    fi
    echo M5_CONNECTED_PROCESS_CANCEL_PASS
    exit 0
  fi
  if [[ ${HARNESS_M5_PROCESS_OUTPUT_LIMITS_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-process-output-limits-live.ts)
    echo M5_CONNECTED_PROCESS_OUTPUT_LIMITS_PASS
    exit 0
  fi
  if [[ ${HARNESS_M6_LARGE_ARTIFACT_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-process-large-live.ts)
    [[ ! -d "$product/apps/web/.next/dev" ]] || mv "$product/apps/web/.next/dev" "$HARNESS_STATE/next-dev-cache"
    set -m
    (cd "$product" && exec pnpm --filter @neko/web exec next dev --port 18121) > /tmp/harness-m6-large-web.log 2>&1 &
    web_pid=$!
    set +m
    trap 'kill -TERM -- "-$web_pid" 2>/dev/null || true; wait "$web_pid" 2>/dev/null || true' EXIT
    ready=0
    for ((n=0; n<90; n++)); do
      if curl -sS --max-time 3 -o /dev/null http://localhost:18121/ 2>/dev/null; then ready=1; break; fi
      kill -0 "$web_pid" || exit 1
      sleep 1
    done
    [[ "$ready" == 1 ]] || { echo 'Isolated large artifact web server did not start' >&2; exit 1; }
    large_run=$(cat "$HARNESS_STATE/m6-large-process-run")
    large_url="http://localhost:18121/api/work/files/runs/$large_run/artifacts/process-1/large.bin"
    curl -fsS --max-time 30 -D "$HARNESS_STATE/process-large.headers" -o "$HARNESS_STATE/process-large.bin" "$large_url"
    python3 -c 'import pathlib,sys; data=pathlib.Path(sys.argv[1]).read_bytes(); assert len(data)==8<<20 and data==b"A"*(8<<20)' "$HARNESS_STATE/process-large.bin"
    rg -qi '^content-disposition: attachment; filename="large.bin"' "$HARNESS_STATE/process-large.headers"
    [[ $(curl -sS -o /dev/null -w '%{http_code}' "${large_url%large.bin}unissued.bin") == 404 ]]
    echo "M6_WEB_LARGE_ARTIFACT_PASS $large_run"
    exit 0
  fi
  if [[ ${HARNESS_M5_TRIGGER_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-trigger-replay-live.ts)
    echo M5_CONNECTED_TRIGGER_REPLAY_PASS
    exit 0
  fi
  if [[ ${HARNESS_M6_PRICING_PREFLIGHT_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-workflow-api-pricing-live.ts)
    echo M6_CONNECTED_API_PRICING_PREFLIGHT_PASS
    exit 0
  fi
  if [[ ${HARNESS_M5_WORKFLOW_CHILD_ONLY:-0} == 1 ]]; then
    register_workflow_priced_provider
    workflow_child_priced_route
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    if ! (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-workflow-child-live.ts); then
      docker compose -p harness-m3 -f integration/openneko/compose.yml logs --tail=80 model >&2 || true
      exit 1
    fi
    echo M5_CONNECTED_WORKFLOW_CHILD_PASS
    exit 0
  fi
  if [[ ${HARNESS_M6_APPROVAL_COMPACTION_ONLY:-0} == 1 || ${HARNESS_M6_TRIAGE_ONLY:-0} == 1 || ${HARNESS_M6_CANARY_ONLY:-0} == 1 ]]; then
    cat > "$HARNESS_STATE/m6-approval-provider.yaml" <<'YAML'
id: harness-m6-approval
category: agent
display_name: Harness M6 approval fixture
credentials:
  - name: HARNESS_APPROVAL_SOURCE_KEY
    env_vars: [HARNESS_APPROVAL_SOURCE_KEY]
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
    "$cli" --gateway harness-m2 provider profile import --file "$HARNESS_STATE/m6-approval-provider.yaml"
    "$cli" --gateway harness-m2 provider create --name harness-m6-approval --type harness-m6-approval --credential HARNESS_APPROVAL_SOURCE_KEY=synthetic-m6-approval
    if [[ ${HARNESS_M6_TRIAGE_ONLY:-0} == 1 || ${HARNESS_M6_CANARY_ONLY:-0} == 1 ]]; then
      cat > "$HARNESS_STATE/m6-triage-provider.yaml" <<'YAML'
id: harness-m6-triage
category: agent
display_name: Harness M6 Typesafe fixture
credentials:
  - name: HARNESS_TRIAGE_SOURCE_KEY
    env_vars: [HARNESS_TRIAGE_SOURCE_KEY]
    required: true
endpoints:
  - host: host.docker.internal
    port: 18118
    protocol: rest
    enforcement: enforce
    access: read-write
    path: /route/triage/v1/**
binaries: [/usr/local/bin/harness-openneko]
YAML
      "$cli" --gateway harness-m2 provider profile import --file "$HARNESS_STATE/m6-triage-provider.yaml"
      "$cli" --gateway harness-m2 provider create --name harness-m6-triage --type harness-m6-triage --credential HARNESS_TRIAGE_SOURCE_KEY=synthetic-m6-triage
      export OPENNEKO_HARNESS_ROUTING='{"context":"fixture","executor":"fixture","responder":"fixture","triage":"triage","budget_policy":{"version":"m6-shadow-v1","short":{"max_model_calls":2,"max_model_tokens":8000,"max_cost_micros":2000},"multi_step":{"max_model_calls":3,"max_model_tokens":40000,"max_cost_micros":20000},"artifact":{"max_model_calls":48,"max_model_tokens":100000,"max_cost_micros":1000000}},"pricing_version":"m6-triage-v1","graphjin_price":{"input_micros_per_million":1000000,"output_micros_per_million":1000000},"routes":[{"key":"fixture","model":"harness-compaction-approval-fixture","url":"http://host.docker.internal:18118/v1","provider":"harness-m6-approval","credential_env":"HARNESS_APPROVAL_SOURCE_KEY","api_key_env":"HARNESS_FIXTURE_KEY","price":{"input_micros_per_million":1000000,"output_micros_per_million":1000000}},{"key":"triage","model":"jev-fixture","url":"http://host.docker.internal:18118/route/triage","provider":"harness-m6-triage","credential_env":"HARNESS_TRIAGE_SOURCE_KEY","api_key_env":"HARNESS_TRIAGE_KEY","price":{"input_micros_per_million":1000000,"output_micros_per_million":1000000}}]}'
      export OPENNEKO_HARNESS_TRIAGE_SHADOW=1
      if [[ ${HARNESS_M6_CANARY_ONLY:-0} == 1 ]]; then
        export OPENNEKO_HARNESS_BUDGET_CANARY=1
        export HARNESS_BUDGET_COMPARISON_REPORT="$HARNESS_STATE/m6-budget-canary.json"
        go build -o "$HARNESS_STATE/harness-budget-compare" ./cmd/harness-budget-compare
      else
        export HARNESS_BUDGET_EVAL_MANIFEST="$HARNESS_STATE/m6-budget-eval-manifest.json"
        go build -o "$HARNESS_STATE/harness-budget-eval" ./cmd/harness-budget-eval
      fi
    else
      export OPENNEKO_HARNESS_ROUTING='{"context":"fixture","executor":"fixture","responder":"fixture","pricing_version":"m6-approval-compaction-v1","graphjin_price":{"input_micros_per_million":1000000,"output_micros_per_million":1000000},"routes":[{"key":"fixture","model":"harness-compaction-approval-fixture","url":"http://host.docker.internal:18118/v1","provider":"harness-m6-approval","credential_env":"HARNESS_APPROVAL_SOURCE_KEY","api_key_env":"HARNESS_FIXTURE_KEY","price":{"input_micros_per_million":1000000,"output_micros_per_million":1000000}}]}'
    fi
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-workflow-compaction-approval-live.ts)
    if [[ ${HARNESS_M6_CANARY_ONLY:-0} == 1 ]]; then
      (
        export OPENNEKO_HARNESS_BUDGET_CANARY=0
        export HARNESS_BUDGET_EVAL_MANIFEST="$HARNESS_STATE/m6-budget-fixed-manifest.json"
        export HARNESS_BUDGET_COMPARISON_REPORT="$HARNESS_STATE/m6-budget-fixed.json"
        cd "$product"
        pnpm --filter @neko/worker exec tsx scripts/harness-workflow-compaction-approval-live.ts
      )
      python3 - "$HARNESS_STATE/m6-budget-canary.json" "$HARNESS_STATE/m6-budget-fixed.json" "$HARNESS_STATE/m6-budget-comparison-manifest.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as f:
    canary = json.load(f)
with open(sys.argv[2], encoding="utf-8") as f:
    fixed = json.load(f)
assert canary["mode"] == "canary" and fixed["mode"] == "fixed"
assert canary["verified"] and fixed["verified"]
assert canary["modelCalls"] == fixed["modelCalls"]
assert canary["ordinaryCalls"] == fixed["ordinaryCalls"]
assert canary["triageCalls"] == fixed["triageCalls"] == 1
assert canary["chargedMicros"] == fixed["chargedMicros"]
manifest = {"version": 1, "pairs": [{"id": "connected-approval-compaction", "split": "calibration",
    "source": "synthetic", "task_class": "investigation",
    "fixed": {"root": fixed["checkpointRoot"], "run_id": fixed["runId"],
        "checkpoint_sha256": fixed["checkpointSha256"], "outcome": "verified_success", "wall_ms": fixed["wallMS"]},
    "canary": {"root": canary["checkpointRoot"], "run_id": canary["runId"],
        "checkpoint_sha256": canary["checkpointSha256"], "outcome": "verified_success", "wall_ms": canary["wallMS"]}}]}
with open(sys.argv[3], "w", encoding="utf-8") as f:
    json.dump(manifest, f)
print("M6_CONNECTED_BUDGET_COMPARISON", json.dumps({"canary": canary, "fixed": fixed}, sort_keys=True))
PY
      "$HARNESS_STATE/harness-budget-compare" < "$HARNESS_STATE/m6-budget-comparison-manifest.json" > "$HARNESS_STATE/m6-budget-comparison-report.json"
      python3 - "$HARNESS_STATE/m6-budget-comparison-report.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as f:
    report = json.load(f)
assert report["calibration"]["pairs"] == 1
assert report["held_out"]["pairs"] == 0
assert report["summary"]["fixed_success"] == report["summary"]["canary_success"] == 1
assert report["summary"]["canary_regressions"] == 0
assert report["summary"]["incomplete_cost_pairs"] == 0
assert report["summary"]["incomplete_usage_pairs"] == 0
assert report["pairs"][0]["fixed"]["charged_micros"] == report["pairs"][0]["canary"]["charged_micros"]
PY
      echo M6_CONNECTED_BUDGET_CANARY_PASS
    elif [[ ${HARNESS_M6_TRIAGE_ONLY:-0} == 1 ]]; then
      "$HARNESS_STATE/harness-budget-eval" < "$HARNESS_BUDGET_EVAL_MANIFEST" > "$HARNESS_STATE/m6-budget-eval-report.json"
      python3 - "$HARNESS_STATE/m6-budget-eval-report.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as f:
    report = json.load(f)
assert report["summary"]["verified_success"] == 1
assert report["summary"]["premature_budget_failures"] == 0
assert report["summary"]["incomplete_usage"] == 0
assert report["cases"][0]["budget"]["final_profile"] == "artifact"
assert report["summary"]["canary_ready"] is False
PY
      echo M6_CONNECTED_BUDGET_EVAL_PASS
      echo M6_CONNECTED_WORKFLOW_TRIAGE_PASS
    else
      echo M6_CONNECTED_WORKFLOW_APPROVAL_COMPACTION_PASS
    fi
    exit 0
  fi
  if [[ ${HARNESS_M6_COMPACTION_ONLY:-0} == 1 ]]; then
    if [[ ${HARNESS_M6_COMPACTION_WEB:-0} == 1 ]]; then
      if lsof -nP -iTCP:18121 -sTCP:LISTEN >/dev/null 2>&1; then
        echo 'Port 18121 is already in use; refusing to test against an existing web server' >&2
        exit 1
      fi
      [[ ! -d "$product/apps/web/.next/dev" ]] || mv "$product/apps/web/.next/dev" "$HARNESS_STATE/m6-next-dev-cache"
      set -m
      (cd "$product" && exec pnpm --filter @neko/web exec next dev --port 18121) > "$HARNESS_STATE/m6-compaction-web.log" 2>&1 &
      web_pid=$!
      set +m
      trap 'kill -TERM -- "-$web_pid" 2>/dev/null || true; wait "$web_pid" 2>/dev/null || true' EXIT
      ready=0
      for ((n=0; n<90; n++)); do
        if curl -sS --max-time 3 -o /dev/null http://localhost:18121/ 2>/dev/null; then ready=1; break; fi
        kill -0 "$web_pid" || exit 1
        sleep 1
      done
      [[ "$ready" == 1 ]] || { echo 'Isolated M6 web server did not start' >&2; exit 1; }
      export HARNESS_M6_COMPACTION_WEB=1
    fi
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-workflow-compaction-live.ts)
    if [[ ${HARNESS_M6_COMPACTION_WEB:-0} == 1 ]]; then
      work_run=$(cat "$HARNESS_STATE/m6-compaction-work-run")
      workflow_run=$(cat "$HARNESS_STATE/m6-compaction-workflow-run")
      python3 - "$HARNESS_STATE/m6-compaction-expected.csv" <<'PY'
import pathlib, sys
pathlib.Path(sys.argv[1]).write_bytes(b'lead_id\n' + b'LEAD-42\n' * 6500)
PY
      [[ $(curl -sS --max-time 30 -o /dev/null -w '%{http_code}' "http://localhost:18121/api/work/files/runs/$work_run/artifacts/result.csv") == 404 ]]
      workflow_status=$(curl -sS --max-time 30 -D "$HARNESS_STATE/m6-workflow-file.headers" -o "$HARNESS_STATE/m6-workflow-file.csv" -w '%{http_code}' "http://localhost:18121/api/workflow-runs/$workflow_run/artifact")
      if [[ "$workflow_status" != 200 ]]; then
        echo "M6 workflow artifact download returned HTTP $workflow_status" >&2
        cat "$HARNESS_STATE/m6-workflow-file.csv" >&2
        exit 1
      fi
      cmp -s "$HARNESS_STATE/m6-compaction-expected.csv" "$HARNESS_STATE/m6-workflow-file.csv"
      rg -qi '^content-disposition: attachment; filename="result.csv"' "$HARNESS_STATE/m6-workflow-file.headers"
      missing_run=$(python3 -c 'import uuid; print(uuid.uuid4())')
      [[ $(curl -sS -o /dev/null -w '%{http_code}' "http://localhost:18121/api/workflow-runs/$missing_run/artifact") == 404 ]]
      echo M6_CONNECTED_COMPACTION_WEB_DOWNLOAD_PASS
    fi
    echo M6_CONNECTED_WORKFLOW_COMPACTION_PASS
    exit 0
  fi
  if [[ ${HARNESS_M6_FINALIZER_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-workflow-finalizer-live.ts)
    echo M6_CONNECTED_WORKFLOW_FINALIZER_PASS
    exit 0
  fi
  if [[ ${HARNESS_M6_STATE_CRASH_ONLY:-0} == 1 ]]; then
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts --seed-only)
    (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-workflow-state-crash-live.ts)
    echo M6_CONNECTED_WORKFLOW_STATE_CRASH_PASS
    exit 0
  fi
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
  if ! (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts); then
    docker compose -p harness-m3 -f integration/openneko/compose.yml logs --tail=80 model >&2 || true
    exit 1
  fi
  if [[ ${HARNESS_M3_API_HTTP:-0} == 1 ]]; then
    process_run=$(cat "$HARNESS_STATE/m5-process-run")
    process_url="http://localhost:18121/api/work/files/runs/$process_run/artifacts/process-1/result.csv"
    curl -fsS --max-time 10 -D "$HARNESS_STATE/process.headers" -o "$HARNESS_STATE/process.csv" "$process_url"
    printf 'lead_id\nLEAD-42\n' | cmp -s - "$HARNESS_STATE/process.csv"
    rg -qi '^content-disposition: attachment; filename="result.csv"' "$HARNESS_STATE/process.headers"
    [[ $(curl -sS -o /dev/null -w '%{http_code}' "${process_url%result.csv}unissued.csv") == 404 ]]
    echo "M5_WEB_PROCESS_PASS $process_run"
    (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-process-artifact.mjs "$(cat "$HARNESS_STATE/m5-process-thread")")
    large_run=$(cat "$HARNESS_STATE/m5-large-process-run")
    large_url="http://localhost:18121/api/work/files/runs/$large_run/artifacts/process-1/large.bin"
    curl -fsS --max-time 30 -D "$HARNESS_STATE/process-large.headers" -o "$HARNESS_STATE/process-large.bin" "$large_url"
    python3 -c 'import pathlib,sys; data=pathlib.Path(sys.argv[1]).read_bytes(); assert len(data)==8<<20 and data==b"A"*(8<<20)' "$HARNESS_STATE/process-large.bin"
    rg -qi '^content-disposition: attachment; filename="large.bin"' "$HARNESS_STATE/process-large.headers"
    echo "M5_WEB_PROCESS_LARGE_PASS $large_run"
  fi
  docker compose -p harness-m3 -f integration/openneko/compose.yml restart model
  register_workflow_priced_provider
  workflow_child_priced_route
  (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-workflow-child-live.ts)
  unset OPENNEKO_HARNESS_ROUTING
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
  (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-workflow-card-reload.mjs "$(cat "$HARNESS_STATE/m5-workflow-thread")")
  (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-workflow-trigger-card-reload.mjs "$(cat "$HARNESS_STATE/m5-workflow-thread")")
  (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-workflow-delete-card-reload.mjs "$(cat "$HARNESS_STATE/m5-workflow-delete-thread")")
  (cd "$product" && pnpm --filter @neko/web exec node scripts/harness-rule-card-reload.mjs "$(cat "$HARNESS_STATE/m5-rule-thread")")
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
