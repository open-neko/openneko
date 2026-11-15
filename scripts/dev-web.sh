#!/bin/sh
# Builds the static web app and runs `openneko web` on port $1, watching
# apps/web/src so edits rebuild apps/web/dist in place.
set -eu

web_port="${1:?usage: dev-web.sh <port>}"
bin_dir=".openneko/dev/bin"
mkdir -p "$bin_dir"

if [ ! -f apps/web/dist/index.html ] || [ -n "$(find apps/web/src -newer apps/web/dist/index.html -print -quit 2>/dev/null)" ]; then
  pnpm --filter @neko/web build:spa
fi

(cd apps/openneko && go build -o "../../$bin_dir/openneko-web" ./cmd/openneko)

watch_pid=""
if [ "${OPENNEKO_WEB_WATCH:-1}" != "0" ]; then
  pnpm --filter @neko/web exec vite build --config spa.vite.config.mts --watch --emptyOutDir false &
  watch_pid="$!"
fi
web_pid=""
cleanup() {
  [ -n "$web_pid" ] && kill "$web_pid" 2>/dev/null || true
  [ -n "$watch_pid" ] && kill "$watch_pid" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

"$bin_dir/openneko-web" web --addr "${OPENNEKO_WEB_ADDR:-:$web_port}" --dir apps/web/dist &
web_pid="$!"
wait "$web_pid"
