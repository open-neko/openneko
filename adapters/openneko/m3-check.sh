#!/usr/bin/env bash
set -euo pipefail
product=${OPENNEKO_TEST_SOURCE:?}
cli=${OPENSHELL_TEST_CLI:?}
go build -o "$HARNESS_STATE/openshell-compat" ./adapters/openneko/cmd/openshell-compat
go build -o "$HARNESS_STATE/harness-inspect" ./cmd/harness-inspect
export HARNESS_INSPECT_BIN="$HARNESS_STATE/harness-inspect"
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
export HARNESS_M3_LIVE=1 OPENNEKO_PG_ENV_OVERRIDE=1 NEKO_PG_HOST=127.0.0.1 NEKO_PG_PORT=18119 NEKO_PG_USER=neko NEKO_PG_PASSWORD=synthetic-m3 NEKO_PG_DATABASE=neko
(cd "$product" && pnpm --filter @neko/llm exec vitest run test/harness-live.test.ts)

  mkdir -p "$HARNESS_STATE/bin"
  ln -s "$HARNESS_M3_CLI" "$HARNESS_STATE/bin/openshell"
  export PATH="$HARNESS_STATE/bin:$PATH" OPENNEKO_AGENT_BACKEND=harness OPENNEKO_AGENT_IMAGE=harness-openneko:m3 OPENNEKO_AGENT_WARM_POOL_SIZE=0 OPENSHELL_GATEWAY=harness-m2
  export OPENNEKO_AGENT_MODEL_PROVIDER=harness-m3 OPENNEKO_AGENT_HERMES_HOME="$HARNESS_STATE/provider-config" OPENNEKO_AGENT_MODEL_HOST=http://host.docker.internal:18118
  export RECORDS_PG_HOST=127.0.0.1 RECORDS_PG_PORT=18119 RECORDS_PG_USER=neko RECORDS_PG_PASSWORD=synthetic-m3 RECORDS_PG_DATABASE=neko
  export OPENNEKO_HOST_WEB_DEV=1 NODE_ENV=development OPENNEKO_AGENT_HOME="$HARNESS_STATE/user" WORKER_ADMIN_URL=http://127.0.0.1:18122 OPENNEKO_BROKER_PORT=18123
  docker compose -p harness-m3 -f integration/m3/compose.yml restart model
  (cd "$product" && pnpm --filter @neko/worker exec tsx scripts/harness-m3.ts)
if [[ ${HARNESS_M3_WEB:-0} == 1 ]]; then
  docker compose -p harness-m3 -f integration/m3/compose.yml restart model
  (cd "$product" && pnpm --filter @neko/web exec next dev --port 18121) > /tmp/harness-m3-web.log 2>&1 &
  web_pid=$!
  trap 'kill "$web_pid" 2>/dev/null || true' EXIT
  echo "M3_WEB_READY state=$HARNESS_STATE"
  for ((n=0; n<900; n++)); do
    [[ ! -f "$HARNESS_STATE/web-done" ]] || break
    kill -0 "$web_pid" || exit 1
    sleep 2
  done
  [[ -f "$HARNESS_STATE/web-done" ]] || { echo "Browser acceptance timed out" >&2; exit 1; }
fi
