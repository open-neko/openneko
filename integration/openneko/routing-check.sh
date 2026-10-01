#!/usr/bin/env bash
# Qualify three Ax stage routes through separate OpenShell provider credentials.
set -euo pipefail
cli=${OPENSHELL_TEST_CLI:?}
state=${HARNESS_STATE:?}
oss=("$cli" --gateway harness-m2)
name=harness-m6-routing
cleanup() { "${oss[@]}" sandbox delete "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
for stage in context executor responder; do
  upper=$(printf '%s' "$stage" | tr '[:lower:]' '[:upper:]')
  cat > "$state/m6-$stage-provider.yaml" <<YAML
id: harness-m6-$stage
category: agent
display_name: Harness M6 $stage fixture
credentials:
  - name: ${stage}_key
    env_vars: [HARNESS_${upper}_KEY]
    required: true
endpoints:
  - host: host.docker.internal
    port: 18118
    protocol: rest
    enforcement: enforce
    access: read-write
    path: /route/$stage/v1/**
binaries: [/usr/local/bin/harness-openneko]
YAML
  "${oss[@]}" provider profile import --file "$state/m6-$stage-provider.yaml"
  "${oss[@]}" provider create --name "harness-m6-$stage" --type "harness-m6-$stage" --credential "${stage}_key=synthetic-m6-$stage"
done
cat > "$state/m6-policy.yaml" <<'YAML'
version: 1
filesystem_policy:
  include_workdir: true
  read_only: [/usr, /etc, /proc]
  read_write: [/sandbox, /tmp, /dev/null]
landlock:
  compatibility: best_effort
process:
  run_as_user: sandbox
  run_as_group: sandbox
network_policies:
  model:
    name: model
    binaries:
      - path: /usr/local/bin/harness-openneko
    endpoints:
      - host: host.docker.internal
        port: 18118
        protocol: rest
        enforcement: enforce
        rules:
          - allow: {method: '*', path: '/route/**'}
YAML
"${oss[@]}" sandbox create --name "$name" --from harness-openneko:m3 \
  --provider harness-m6-context --provider harness-m6-executor --provider harness-m6-responder \
  --no-auto-providers --no-tty --detach --policy "$state/m6-policy.yaml" -- sleep infinity
manifest='{"context":"context","executor":"executor","responder":"responder","routes":[{"key":"context","model":"harness-route-context","url":"http://host.docker.internal:18118/route/context/v1","api_key_env":"HARNESS_CONTEXT_KEY"},{"key":"executor","model":"harness-route-executor","url":"http://host.docker.internal:18118/route/executor/v1","api_key_env":"HARNESS_EXECUTOR_KEY"},{"key":"responder","model":"harness-route-responder","url":"http://host.docker.internal:18118/route/responder/v1","api_key_env":"HARNESS_RESPONDER_KEY"}]}'
spec='{"version":1,"run_id":"m6-routing","input_id":"m6-routing-input","prompt":"Answer the routing check"}'
"${oss[@]}" sandbox exec -n "$name" --no-tty --timeout 60 -- sh -c '
  export HARNESS_CONTEXT_KEY="$context_key" HARNESS_EXECUTOR_KEY="$executor_key" HARNESS_RESPONDER_KEY="$responder_key"
  export HARNESS_MODEL_ROUTES="$1" OPENNEKO_HARNESS_LOOKUP_READ=0
  printf "%s" "$2" | /usr/local/bin/harness-openneko
' sh "$manifest" "$spec" > "$state/m6-routing-events.jsonl"
python3 - "$state/m6-routing-events.jsonl" <<'PY'
import json, sys
events=[json.loads(line) for line in open(sys.argv[1]) if line.startswith('{')]
routes=[(e.get('origin'),e.get('name')) for e in events if e.get('type')=='model.request.started']
assert routes == [('context','harness-route-context'),('executor','harness-route-executor'),('responder','harness-route-responder')], routes
done=[e for e in events if e.get('type')=='run.finished']
assert len(done)==1 and done[0]['result']['status']=='completed' and done[0]['result']['answer']=='ROUTED-OK',done
usage=[e for e in events if e.get('type')=='model.request.finished']
assert len(usage)==3 and all(e.get('usage',{}).get('reported')==1 and e['usage'].get('total_tokens')==15 for e in usage),usage
stages=[e for e in events if e.get('type')=='model.stage_usage']
assert {e['name'] for e in stages}=={'distiller','executor','responder'} and all(e.get('stage_usage',{}).get('coverage')=='complete' for e in stages),stages
PY
counts=$(curl -fsS http://127.0.0.1:18118/control)
python3 - "$counts" <<'PY'
import json,sys
counts=json.loads(sys.argv[1])
for stage in ('context','executor','responder'):
 assert counts.get('harness-route-'+stage)==1,counts
PY
echo M6_CONNECTED_OPENSHELL_ROUTING_PASS
