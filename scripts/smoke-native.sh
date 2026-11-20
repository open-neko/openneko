#!/usr/bin/env bash
# Usage: smoke-native.sh <release folder that holds bin/ and share/>
#
# Environment:
#   SMOKE_DATABASE_URL    OpenNeko's Postgres (pgvector), empty database
#   SMOKE_SOURCE_URL      a second Postgres database; the smoke fills it with
#                         one table and connects it as the data source
#   SMOKE_MODEL_PROVIDER  default google-gemini
#   SMOKE_MODEL           default: the provider's default model
#   SMOKE_MODEL_KEY       the provider key; empty skips the question
#   SMOKE_REQUIRE_ANSWER  1 fails the smoke when the question would be skipped
#   SMOKE_MODEL_URL       base URL, for the openai-compatible provider only
#   SMOKE_PORT            web port, default 3000
set -euo pipefail

release=${1:?usage: smoke-native.sh <release folder>}
release=$(cd "$release" && pwd)
exe="$release/bin/openneko"
port=${SMOKE_PORT:-3000}
base="http://127.0.0.1:$port"
provider=${SMOKE_MODEL_PROVIDER:-google-gemini}
: "${SMOKE_DATABASE_URL:?SMOKE_DATABASE_URL is required}"
: "${SMOKE_SOURCE_URL:?SMOKE_SOURCE_URL is required}"

ask=1
model_key=${SMOKE_MODEL_KEY:-}
if [ "$provider" != openai-compatible ] && [ -z "$model_key" ]; then
  if [ "${SMOKE_REQUIRE_ANSWER:-}" = 1 ]; then
    echo "::error::No model key, and this run must ask the question. Check the GEMINI_API_KEY secret and the SMOKE_MODEL_KEY variable." >&2
    exit 1
  fi
  echo "::warning::Question step skipped: no model key. Add the repository secret GEMINI_API_KEY to ask one question."
  ask=0
  model_key=smoke-no-model-key
fi

step() { printf '\n== %s\n' "$*"; }
fail() {
  echo "::error::$*" >&2
  "$exe" status >&2 || true
  "$exe" logs --tail 80 >&2 || true
  "$exe" stop >&2 || true
  exit 1
}

api() {
  local method=$1 path=$2 body=${3:-}
  local args=(-sS -o "$tmp/body" -w '%{http_code}' -X "$method" -H "Origin: $base" -H 'Accept: application/json')
  [ -n "$body" ] && args+=(-H 'Content-Type: application/json' --data-binary "$body")
  local code
  code=$(curl "${args[@]}" "$base$path") || fail "$method $path: no answer"
  case "$code" in
    2??) cat "$tmp/body" ;;
    *) fail "$method $path answered $code: $(head -c 600 "$tmp/body")" ;;
  esac
}

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

step "release-check"
"$exe" version --short
"$exe" release-check --skills --bundled-packs

step "the data source"
psql "$SMOKE_SOURCE_URL" -v ON_ERROR_STOP=1 -q <<'SQL'
drop table if exists warehouse_stock;
create table warehouse_stock (sku text primary key, item text not null, units integer not null);
insert into warehouse_stock values
  ('KT-204', 'Blue enamel kettle', 4817),
  ('MG-118', 'Stone mug', 2290),
  ('PL-310', 'Oak serving board', 655);
SQL

step "start"
OPENNEKO_PORT=$port "$exe" start --native -d --database-url "$SMOKE_DATABASE_URL" || fail "openneko start failed"

step "first admin and model"
api GET /api/v1/auth/session | grep -q '"role":"admin"' || fail "the first visitor is not the admin: $(cat "$tmp/body")"
config='{}'
[ -n "${SMOKE_MODEL_URL:-}" ] && config=$(printf '{"url":"%s"}' "$SMOKE_MODEL_URL")
secrets='{}'
[ -n "$model_key" ] && secrets=$(printf '{"apiKey":"%s"}' "$model_key")
draft=$(printf '{"scope":"primary","provider":"%s","model":"%s","enabled":true,"config":%s,"secrets":%s}' \
  "$provider" "${SMOKE_MODEL:-}" "$config" "$secrets")
[ "$ask" = 1 ] && api POST /api/v1/settings/provider/test "$draft" >/dev/null
api PUT /api/v1/settings/provider "$draft" >/dev/null
gql="http://127.0.0.1:8080/api/v1/graphql"
api POST /api/v1/settings/data-source/test "{\"graphqlUrl\":\"$gql\",\"mcpUrl\":\"\"}" >/dev/null
api PUT /api/v1/settings/data-source "{\"graphqlUrl\":\"$gql\",\"mcpUrl\":\"\",\"label\":\"primary\"}" >/dev/null
api POST /api/v1/settings/finish >/dev/null

step "connect the data source"
re='^postgres(ql)?://([^:@/]+)(:([^@/]*))?@([^:/?]+)(:([0-9]+))?/([^?]+)'
[[ $SMOKE_SOURCE_URL =~ $re ]] || fail "SMOKE_SOURCE_URL is not a postgres URL with a user"
cat > "$tmp/source.yml" <<YAML
sources:
  - name: smoke
    kind: database
    default: true
    read_only: true
    type: postgres
    host: ${BASH_REMATCH[5]}
    port: ${BASH_REMATCH[7]:-5432}
    dbname: ${BASH_REMATCH[8]}
    user: ${BASH_REMATCH[2]}
    password: "${BASH_REMATCH[4]}"
    access:
      read: authenticated
      write: blocked
      delete: blocked
YAML
for i in $(seq 30); do
  if "$exe" graphjin import "$tmp/source.yml" 2>"$tmp/import.err"; then break; fi
  grep -q 'GraphJin is starting' "$tmp/import.err" && [ "$i" -lt 30 ] || fail "graphjin import failed: $(cat "$tmp/import.err")"
  sleep 2
done

if [ "$ask" = 1 ]; then
  step "ask"
  question="How many units of the Blue enamel kettle are in stock? Answer with the number."
  thread=$(api POST /api/v1/work/threads '{"title":"smoke"}' | jq -r .thread.id)
  run=$(api POST "/api/v1/work/threads/$thread/runs" "$(jq -n --arg m "$question" '{message: $m}')" | jq -r .runId)
  echo "thread $thread, run $run"
  curl -sS -N --max-time 600 -H "Origin: $base" -H 'Accept: text/event-stream' \
    "$base/api/v1/work/threads/$thread/runs/$run/events" > "$tmp/events" || true
  done_event=$(sed -n 's/^data: //p' "$tmp/events" | jq -c 'select(.type == "done")' | tail -1)
  [ -n "$done_event" ] || fail "the run stream ended with no done event: $(tail -c 1500 "$tmp/events")"
  api GET "/api/v1/work/threads/$thread" > "$tmp/thread.json"
  status=$(jq -r --arg r "$run" '.runs[] | select(.id == $r) | .status' "$tmp/thread.json")
  answer=$(jq -r --arg r "$run" '[.messages[] | select(.runId == $r and .role == "assistant") | .content] | last // ""' "$tmp/thread.json")
  printf 'run %s\n%s\n' "$status" "$answer"
  [ "$status" = completed ] || fail "the run ended $status: $(jq -c --arg r "$run" '.runs[] | select(.id == $r)' "$tmp/thread.json")"
  printf '%s' "$answer" | tr -d ', ' | grep -q 4817 || fail "the answer does not name 4817"
else
  step "ask: skipped, no model key"
fi

step "health"
for path in /health /health/scheduler /health/security; do
  echo "$path $(api GET "$path" | head -c 300)"
done
"$exe" status

step "stop"
"$exe" stop
if curl -s --max-time 3 "$base/api/v1/version" >/dev/null; then fail "web still answers after stop"; fi
if [ "$ask" = 1 ]; then echo "smoke passed"; else echo "smoke passed; the question was skipped"; fi
