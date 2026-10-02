#!/usr/bin/env bash
# Qualify Ax stage routes and one-step fallback through OpenShell credentials.
set -euo pipefail
cli=${OPENSHELL_TEST_CLI:?}
state=${HARNESS_STATE:?}
oss=("$cli" --gateway harness-m2)
name=harness-m6-routing
cleanup() { "${oss[@]}" sandbox delete "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
for stage in context context-spare context-503 context-403 context-429 skill context-lookup executor executor-base executor-strong executor-lookup responder responder-lookup; do
  upper=$(printf '%s' "$stage" | tr '[:lower:]' '[:upper:]' | tr '-' '_')
  cat > "$state/m6-$stage-provider.yaml" <<YAML
id: harness-m6-$stage
category: agent
display_name: Harness M6 $stage fixture
credentials:
  - name: HARNESS_${upper}_KEY
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
  "${oss[@]}" provider create --name "harness-m6-$stage" --type "harness-m6-$stage" --credential "HARNESS_${upper}_KEY=synthetic-m6-$stage"
done
if [[ ${HARNESS_M6_ROUTING_WORKER_ONLY:-0} == 1 ]]; then exit 0; fi
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
  --provider harness-m6-context --provider harness-m6-context-spare --provider harness-m6-context-503 \
  --provider harness-m6-context-403 --provider harness-m6-context-429 --provider harness-m6-executor --provider harness-m6-executor-base \
  --provider harness-m6-executor-strong --provider harness-m6-responder \
  --no-auto-providers --no-tty --detach --policy "$state/m6-policy.yaml" -- sleep infinity
manifest='{"context":"context","executor":"executor","responder":"responder","pricing_version":"m6-test-v1","routes":[{"key":"context","model":"harness-route-context","url":"http://host.docker.internal:18118/route/context/v1","api_key_env":"HARNESS_CONTEXT_KEY","price":{"input_micros_per_million":1000000,"output_micros_per_million":1000000}},{"key":"executor","model":"harness-route-executor","url":"http://host.docker.internal:18118/route/executor/v1","api_key_env":"HARNESS_EXECUTOR_KEY","price":{"input_micros_per_million":2000000,"output_micros_per_million":2000000}},{"key":"responder","model":"harness-route-responder","url":"http://host.docker.internal:18118/route/responder/v1","api_key_env":"HARNESS_RESPONDER_KEY","price":{"input_micros_per_million":3000000,"output_micros_per_million":3000000}}]}'
spec='{"version":1,"run_id":"m6-routing","input_id":"m6-routing-input","prompt":"Answer the routing check","max_cost_micros":50000}'
"${oss[@]}" sandbox exec -n "$name" --no-tty --timeout 60 -- sh -c '
  export HARNESS_MODEL_ROUTES="$1" OPENNEKO_HARNESS_LOOKUP_READ=0
  printf "%s" "$2" | /usr/local/bin/harness-openneko
' sh "$manifest" "$spec" > "$state/m6-routing-events.jsonl"
python3 - "$state/m6-routing-events.jsonl" <<'PY'
import json, sys
events=[json.loads(line) for line in open(sys.argv[1]) if line.startswith('{')]
routes=[(e.get('origin'),e.get('name'),e.get('stage')) for e in events if e.get('type')=='model.request.started']
assert routes == [('context','harness-route-context','distiller'),('executor','harness-route-executor','executor'),('responder','harness-route-responder','responder')], routes
done=[e for e in events if e.get('type')=='run.finished']
assert len(done)==1 and done[0]['result']['status']=='completed' and done[0]['result']['answer']=='ROUTED-OK',done
assert done[0]['result']['cost']=={'pricing_version':'m6-test-v1','charged_micros':90,'budget_micros':50000},done
usage=[e for e in events if e.get('type')=='model.request.finished']
assert len(usage)==3 and all(e.get('usage',{}).get('reported')==1 and e['usage'].get('total_tokens')==15 for e in usage),usage
assert [e.get('stage') for e in usage]==['distiller','executor','responder'],usage
assert [e.get('cost_micros') for e in usage]==[15,30,45],usage
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

spec='{"version":1,"run_id":"m6-cost-denied","input_id":"m6-cost-denied-input","prompt":"Answer the routing check","max_cost_micros":4095}'
if "${oss[@]}" sandbox exec -n "$name" --no-tty --timeout 60 -- sh -c '
  export HARNESS_MODEL_ROUTES="$1" OPENNEKO_HARNESS_LOOKUP_READ=0
  printf "%s" "$2" | /usr/local/bin/harness-openneko
' sh "$manifest" "$spec" > "$state/m6-cost-denied-events.jsonl"; then
  echo 'cost gate admitted a model request' >&2
  exit 1
fi
python3 - "$state/m6-cost-denied-events.jsonl" <<'PY'
import json,sys
events=[json.loads(line) for line in open(sys.argv[1]) if line.startswith('{')]
assert not [e for e in events if e.get('type')=='model.request.started'],events
done=[e for e in events if e.get('type')=='run.finished']
assert len(done)==1 and done[0]['result'].get('code')=='cost_budget_exceeded',done
PY
counts=$(curl -fsS http://127.0.0.1:18118/control)
python3 - "$counts" <<'PY'
import json,sys
counts=json.loads(sys.argv[1])
for stage in ('context','executor','responder'):
 assert counts.get('harness-route-'+stage)==1,counts
PY
echo M6_CONNECTED_OPENSHELL_COST_GATE_PASS

for status in 503 403 429; do
  curl -fsS -X POST -d '{}' http://127.0.0.1:18118/control >/dev/null
  manifest=$(python3 - "$status" <<'PY'
import json,sys
status=sys.argv[1]
routes=[]
for key,stage in [('primary','context-'+status),('spare','context-spare'),('executor','executor'),('responder','responder')]:
 routes.append({'key':key,'model':'harness-route-'+stage,
                'url':'http://host.docker.internal:18118/route/'+stage+'/v1',
                'api_key_env':'HARNESS_'+stage.upper().replace('-','_')+'_KEY'})
print(json.dumps({'context':'primary','executor':'executor','responder':'responder',
                  'fallbacks':[{'from':'primary','to':'spare'}],'routes':routes},separators=(',',':')))
PY
)
  spec="{\"version\":1,\"run_id\":\"m6-route-$status\",\"input_id\":\"m6-route-$status-input\",\"prompt\":\"Answer the routing check\"}"
  if [[ "$status" == 429 ]]; then spec="{\"version\":1,\"run_id\":\"m6-route-429\",\"input_id\":\"m6-route-429-input\",\"prompt\":\"Answer the routing check\",\"max_model_calls\":1}"; fi
  if "${oss[@]}" sandbox exec -n "$name" --no-tty --timeout 60 -- sh -c '
    export HARNESS_MODEL_ROUTES="$1" OPENNEKO_HARNESS_LOOKUP_READ=0
    printf "%s" "$2" | /usr/local/bin/harness-openneko
  ' sh "$manifest" "$spec" > "$state/m6-$status-events.jsonl"; then
    [[ "$status" == 503 ]] || { echo "$status incorrectly admitted fallback" >&2; exit 1; }
  else
    [[ "$status" != 503 ]] || { echo '503 failed to use approved fallback' >&2; exit 1; }
  fi
  python3 - "$state/m6-$status-events.jsonl" "$status" <<'PY'
import json,sys
events=[json.loads(line) for line in open(sys.argv[1]) if line.startswith('{')]
status=sys.argv[2]
routes=[e.get('name') for e in events if e.get('type')=='model.request.started']
want=['harness-route-context-'+status]
if status=='503': want+=['harness-route-context-spare','harness-route-executor','harness-route-responder']
assert routes==want,routes
stages=[e.get('stage') for e in events if e.get('type')=='model.request.started']
assert stages==(['distiller','distiller','executor','responder'] if status=='503' else ['distiller']),stages
done=[e for e in events if e.get('type')=='run.finished']
assert len(done)==1,done
assert done[0]['result']['status']==('completed' if status=='503' else 'failed'),done
if status=='429':
 assert done[0]['result'].get('code')=='model_budget_exceeded',done
 assert len([e for e in events if e.get('type')=='model.route.fallback'])==1,events
PY
  counts=$(curl -fsS http://127.0.0.1:18118/control)
  python3 - "$counts" "$status" <<'PY'
import json,sys
counts=json.loads(sys.argv[1]);status=sys.argv[2]
assert counts.get('harness-route-context-'+status)==1,counts
if status=='503':
 for stage in ('context-spare','executor','responder'):
  assert counts.get('harness-route-'+stage)==1,counts
else:
 assert sum(counts.values())==1,counts
PY
done
curl -fsS -X POST -d '{}' http://127.0.0.1:18118/control >/dev/null
spec='{"version":1,"run_id":"m6-route-429-approved","input_id":"m6-route-429-approved-input","prompt":"Answer the routing check","max_model_calls":4}'
"${oss[@]}" sandbox exec -n "$name" --no-tty --timeout 60 -- sh -c '
  export HARNESS_MODEL_ROUTES="$1" OPENNEKO_HARNESS_LOOKUP_READ=0
  printf "%s" "$2" | /usr/local/bin/harness-openneko
' sh "$manifest" "$spec" > "$state/m6-429-approved-events.jsonl"
python3 - "$state/m6-429-approved-events.jsonl" <<'PY'
import json,sys
events=[json.loads(line) for line in open(sys.argv[1]) if line.startswith('{')]
routes=[e.get('name') for e in events if e.get('type')=='model.request.started']
assert routes==['harness-route-context-429','harness-route-context-spare',
                'harness-route-executor','harness-route-responder'],routes
assert [e.get('stage') for e in events if e.get('type')=='model.request.started']==[
    'distiller','distiller','executor','responder'],events
fallback=[e for e in events if e.get('type')=='model.route.fallback']
assert len(fallback)==1 and fallback[0].get('error')=='transient_provider_failure',fallback
done=[e for e in events if e.get('type')=='run.finished']
assert len(done)==1 and done[0]['result']['status']=='completed' and done[0]['result']['answer']=='ROUTED-OK',done
PY
counts=$(curl -fsS http://127.0.0.1:18118/control)
python3 - "$counts" <<'PY'
import json,sys
counts=json.loads(sys.argv[1])
for stage in ('context-429','context-spare','executor','responder'):
 assert counts.get('harness-route-'+stage)==1,counts
assert sum(counts.values())==4,counts
PY
echo M6_CONNECTED_OPENSHELL_FALLBACK_PASS

curl -fsS -X POST -d '{}' http://127.0.0.1:18118/control >/dev/null
manifest=$(python3 - <<'PY'
import json
routes=[]
for key,stage in [('context','context'),('base','executor-base'),('strong','executor-strong'),('responder','responder')]:
 routes.append({'key':key,'model':'harness-route-'+stage,
                'url':'http://host.docker.internal:18118/route/'+stage+'/v1',
                'api_key_env':'HARNESS_'+stage.upper().replace('-','_')+'_KEY'})
print(json.dumps({'context':'context','executor':'base','executor_escalation':'strong',
                  'executor_after_errors':1,'responder':'responder','routes':routes},separators=(',',':')))
PY
)
spec='{"version":1,"run_id":"m6-escalation","input_id":"m6-escalation-input","prompt":"Answer the routing check"}'
"${oss[@]}" sandbox exec -n "$name" --no-tty --timeout 60 -- sh -c '
  export HARNESS_MODEL_ROUTES="$1" OPENNEKO_HARNESS_LOOKUP_READ=0
  printf "%s" "$2" | /usr/local/bin/harness-openneko
' sh "$manifest" "$spec" > "$state/m6-escalation-events.jsonl"
python3 - "$state/m6-escalation-events.jsonl" <<'PY'
import json,sys
events=[json.loads(line) for line in open(sys.argv[1]) if line.startswith('{')]
routes=[e.get('name') for e in events if e.get('type')=='model.request.started']
assert routes==['harness-route-context','harness-route-executor-base','harness-route-executor-strong','harness-route-responder'],routes
failed=[e for e in events if e.get('type')=='executor.step.failed']
assert len(failed)==1 and failed[0].get('error')=='actor_code_error',failed
done=[e for e in events if e.get('type')=='run.finished']
assert len(done)==1 and done[0]['result']['status']=='completed',done
PY
counts=$(curl -fsS http://127.0.0.1:18118/control)
python3 - "$counts" <<'PY'
import json,sys
counts=json.loads(sys.argv[1])
for stage in ('context','executor-base','executor-strong','responder'):
 assert counts.get('harness-route-'+stage)==1,counts
PY
echo M6_CONNECTED_OPENSHELL_EXECUTOR_ESCALATION_PASS
