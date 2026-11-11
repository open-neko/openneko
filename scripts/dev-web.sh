#!/bin/sh
# Runs `openneko web` on port $1 in front of `next dev`, as the web image
# runs it in front of `next start`. Go answers the sign-in routes; Next
# answers the rest of the API and the server pages.
set -eu

web_port="${1:?usage: dev-web.sh <port>}"
next_port="${OPENNEKO_NEXT_PORT:-$((web_port + 1))}"
bin_dir="${OPENNEKO_DEV_STATE:-$PWD/.openneko/dev}/bin"

mkdir -p "$bin_dir"
(cd apps/openneko && go build -o "$bin_dir/openneko-web" ./cmd/openneko)
if [ ! -f apps/web/dist/index.html ] || [ -n "$(find apps/web/src apps/web/public -newer apps/web/dist/index.html -print -quit)" ]; then
  pnpm --filter @neko/web build:spa
fi

exec "$bin_dir/openneko-web" web \
  --addr "${OPENNEKO_WEB_ADDR:-:$web_port}" \
  --dir apps/web/dist \
  --node-addr "127.0.0.1:$next_port" \
  -- pnpm --filter @neko/web exec next dev --hostname 127.0.0.1 --port "$next_port"
