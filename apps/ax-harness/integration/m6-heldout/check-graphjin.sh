#!/usr/bin/env bash
# Qualifies only the held-out dataset's GraphJin visibility, without a model key.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${OPENNEKO_TEST_SOURCE:?Point to the isolated OpenNeko feature checkout}"
project=harness-m6-heldout
if docker network inspect "${project}_default" >/dev/null 2>&1; then
  echo 'Held-out GraphJin stack already exists; refusing to replace it' >&2
  exit 1
fi
for port in 18117 18120; do
  if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
    echo "Port $port is already in use; refusing to start the fixture" >&2
    exit 1
  fi
done
export HARNESS_M6_BUSINESS_SEED="$PWD/integration/m6-heldout/seed.sql"
export GRAPHJIN_AGENT_PROVIDER=openai-compatible GRAPHJIN_AGENT_MODEL=graphjin-fixture GRAPHJIN_AGENT_REASONING=high
compose=(docker compose -p "$project" -f integration/openneko/compose.yml)
trap '"${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true' EXIT
"${compose[@]}" up -d postgres graphjin
export PGHOST=127.0.0.1 PGPORT=18120 PGUSER=fixture PGPASSWORD=fixture PGDATABASE=fixture
ready=0
for ((n=0; n<60; n++)); do
  if [[ $(psql -X -A -t -q -c 'select count(*) from lead_web' 2>/dev/null) == 1002 ]] &&
    curl -fsS --max-time 2 http://127.0.0.1:18117/health >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done
[[ "$ready" == 1 ]] || { echo 'Held-out GraphJin stack did not become ready' >&2; exit 1; }
python3 integration/m6-heldout/verify.py
python3 integration/m6-heldout/attest.py --url http://127.0.0.1:18117/api/v1/agent/status \
  --provider "$GRAPHJIN_AGENT_PROVIDER" --model "$GRAPHJIN_AGENT_MODEL" --reasoning "$GRAPHJIN_AGENT_REASONING"
if python3 integration/m6-heldout/attest.py --url http://127.0.0.1:18117/api/v1/agent/status \
  --provider "$GRAPHJIN_AGENT_PROVIDER" --model "$GRAPHJIN_AGENT_MODEL" --reasoning low >/dev/null 2>&1; then
  echo 'GraphJin attestation accepted a weaker reasoning level' >&2
  exit 1
fi
response=$(curl -fsS --max-time 15 -H 'content-type: application/json' \
  -d '{"query":"query { lead_web(limit: 1) { id email } lead_event(limit: 1) { id email } lead_crm(limit: 1) { id email } }"}' \
  http://127.0.0.1:18117/api/v1/graphql)
python3 - "$response" <<'PY'
import json, sys
data = json.loads(sys.argv[1])
assert not data.get("errors"), data.get("errors")
assert data["data"]["lead_web"][0]["email"] == "lead0001@example.test"
assert data["data"]["lead_event"][0]["id"] == 1
assert data["data"]["lead_crm"][0]["id"] == 1
PY
echo M6_HELDOUT_GRAPHJIN_DATA_PASS
