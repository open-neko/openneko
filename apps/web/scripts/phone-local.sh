#!/bin/sh
# Runs the phone-width check against a local `openneko web` on a new,
# seeded database. No Node server: the server pages are out of scope.
# OPENNEKO_PHONE_PG: a Postgres URL that may create and drop databases.
# Build the static app first: pnpm --filter @neko/web build:spa
set -eu

: "${OPENNEKO_PHONE_PG:?set OPENNEKO_PHONE_PG to a Postgres URL that may create databases}"
port="${OPENNEKO_PHONE_PORT:-3311}"
database="${OPENNEKO_PHONE_DATABASE:-openneko_phone}"
web="$(cd "$(dirname "$0")/.." && pwd)"
root="$(cd "$web/../.." && pwd)"
contract="$root/packages/contract"
work="$(mktemp -d)"
front=""

cleanup() {
  [ -n "$front" ] && kill "$front" 2>/dev/null || true
  (cd "$contract" && pnpm exec tsx scripts/database.ts drop "$OPENNEKO_PHONE_PG" "$database") || true
  rm -rf "$work"
}
trap cleanup EXIT INT TERM

database_url="$(node -e 'const u = new URL(process.argv[1]); u.pathname = "/" + process.argv[2]; console.log(u.toString())' "$OPENNEKO_PHONE_PG" "$database")"
printf '%s' "$database_url" > "$work/database-url"
chmod 600 "$work/database-url"
mkdir -p "$work/config"
export XDG_CONFIG_HOME="$work/config"
export OPENNEKO_DATABASE_URL_FILE="$work/database-url"

(cd "$contract" && pnpm exec tsx scripts/database.ts create "$OPENNEKO_PHONE_PG" "$database")
(cd "$root/apps/openneko" && go build -o "$work/openneko" ./cmd/openneko)
(cd "$root/apps/openneko" && OPENNEKO_WORKSPACE_ROOT="$root" "$work/openneko" migrate --local >"$work/migrate.log")
(cd "$contract" && pnpm exec tsx scripts/seed.ts "$work/tokens")
(cd "$web" && node test/phone/seed.mjs "$database_url")

"$work/openneko" web --addr "127.0.0.1:$port" --dir "$web/dist" --jobs=false >"$work/front.log" 2>&1 &
front=$!

tries=0
until curl -fsS "http://127.0.0.1:$port/api/v1/version" >/dev/null 2>&1; do
  tries=$((tries + 1))
  if [ "$tries" -gt 30 ]; then
    cat "$work/front.log" >&2
    exit 1
  fi
  sleep 1
done

cd "$web"
OPENNEKO_PHONE_URL="http://127.0.0.1:$port" pnpm exec playwright test --config=playwright.phone.config.ts "$@"
