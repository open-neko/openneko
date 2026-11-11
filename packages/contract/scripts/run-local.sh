#!/bin/sh
# Runs the contract tests against a local `next start` on a new database.
# OPENNEKO_CONTRACT_PG: a Postgres URL that may create and drop databases.
# Build the web app first: pnpm --filter @neko/web build
set -eu

: "${OPENNEKO_CONTRACT_PG:?set OPENNEKO_CONTRACT_PG to a Postgres URL that may create databases}"
port="${OPENNEKO_CONTRACT_PORT:-3310}"
database="${OPENNEKO_CONTRACT_DATABASE:-openneko_contract}"
here="$(cd "$(dirname "$0")/.." && pwd)"
root="$(cd "$here/../.." && pwd)"
work="$(mktemp -d)"
web=""

cleanup() {
  [ -n "$web" ] && kill "$web" 2>/dev/null || true
  (cd "$here" && pnpm exec tsx scripts/database.ts drop "$OPENNEKO_CONTRACT_PG" "$database") || true
  rm -rf "$work"
}
trap cleanup EXIT INT TERM

database_url="$(node -e 'const u = new URL(process.argv[1]); u.pathname = "/" + process.argv[2]; console.log(u.toString())' "$OPENNEKO_CONTRACT_PG" "$database")"
printf '%s' "$database_url" > "$work/database-url"
chmod 600 "$work/database-url"
mkdir -p "$work/config/openneko"
echo '{"provider":{"pluginName":"magic-link"}}' > "$work/config/openneko/auth-gate.json"
closed="$(node -e 'const s = require("net").createServer().listen(0, "127.0.0.1", () => { console.log(s.address().port); s.close(); })')"

export XDG_CONFIG_HOME="$work/config"
export OPENNEKO_DATABASE_URL_FILE="$work/database-url"

(cd "$here" && pnpm exec tsx scripts/database.ts create "$OPENNEKO_CONTRACT_PG" "$database")
(cd "$root/apps/openneko" && OPENNEKO_WORKSPACE_ROOT="$root" go run ./cmd/openneko migrate --local >"$work/migrate.log")
(cd "$here" && pnpm exec tsx scripts/seed.ts "$work/tokens")

(cd "$root/apps/web" && \
  WORKER_ADMIN_URL="http://127.0.0.1:$closed" \
  OPENNEKO_SERVE_URL="http://127.0.0.1:$closed" \
  OPENNEKO_GRAPHJIN_URL="http://127.0.0.1:$closed" \
  OPENNEKO_PUBLIC_URL="http://127.0.0.1:$port" \
  exec pnpm exec next start --port "$port" >"$work/web.log" 2>&1) &
web=$!

tries=0
until curl -fsS "http://127.0.0.1:$port/api/v1/version" >/dev/null 2>&1; do
  tries=$((tries + 1))
  if [ "$tries" -gt 60 ]; then
    cat "$work/web.log" >&2
    exit 1
  fi
  sleep 1
done

set -a
. "$work/tokens"
set +a
cd "$here"
OPENNEKO_CONTRACT_URL="http://127.0.0.1:$port" pnpm exec vitest run test/contract.test.ts "$@"
