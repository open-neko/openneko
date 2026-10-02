#!/usr/bin/env bash
# Owns one isolated Postgres container and removes it even if an assertion fails.
set -euo pipefail
cd "$(dirname "$0")/../.."
container=harness-m6-heldout-oracle
if docker ps -a --format '{{.Names}}' | rg -qx "$container"; then
  echo 'Held-out oracle container already exists; refusing to replace it' >&2
  exit 1
fi
if lsof -nP -iTCP:18123 -sTCP:LISTEN >/dev/null 2>&1; then
  echo 'Port 18123 is already in use; refusing to start the fixture' >&2
  exit 1
fi
state=$(mktemp -d)
cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf "$state"
}
trap cleanup EXIT
docker run -d --rm --memory 256m --name "$container" \
  -e POSTGRES_USER=fixture -e POSTGRES_PASSWORD=fixture -e POSTGRES_DB=fixture \
  -p 127.0.0.1:18123:5432 \
  -v "$PWD/integration/m6-heldout/seed.sql:/docker-entrypoint-initdb.d/seed.sql:ro" \
  postgres:16-alpine >/dev/null
export PGHOST=127.0.0.1 PGPORT=18123 PGUSER=fixture PGPASSWORD=fixture PGDATABASE=fixture
ready=0
for ((n=0; n<40; n++)); do
  if [[ $(psql -X -A -t -q -c 'select count(*) from lead_web' 2>/dev/null) == 1002 ]]; then
    ready=1
    break
  fi
  sleep 1
done
[[ "$ready" == 1 ]] || { echo 'Held-out seed did not become ready' >&2; exit 1; }
python3 integration/m6-heldout/verify.py
psql -X -q -v ON_ERROR_STOP=1 -f integration/m6-heldout/oracle.sql > "$state/expected.csv"
python3 integration/m6-heldout/verify.py --artifact "$state/expected.csv"
sed 's/Boundary Lead/Tampered Lead/' "$state/expected.csv" > "$state/tampered.csv"
if cmp -s "$state/expected.csv" "$state/tampered.csv" ||
  python3 integration/m6-heldout/verify.py --artifact "$state/tampered.csv" >/dev/null 2>&1; then
  echo 'Held-out verifier accepted a changed artifact' >&2
  exit 1
fi
echo M6_HELDOUT_ORACLE_PASS
