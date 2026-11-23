#!/bin/sh
# Runs `openneko serve` on the host for `pnpm dev`. It serves files over HTTP
# and runs agent turns on OPENNEKO_SERVE_SOCKET. Both GraphJins run in the
# openneko-serve container that `pnpm dev:up` starts, so this one runs none.
set -eu

. scripts/dev-env.sh

mkdir -p "$(dirname "$OPENNEKO_SERVE_SOCKET")" "$OPENNEKO_AGENT_RUNS_DIR" "$OPENNEKO_DEV_STATE/bin"
(cd apps/openneko && go build -o "$OPENNEKO_DEV_STATE/bin/openneko" ./cmd/openneko)
"$OPENNEKO_DEV_STATE/bin/openneko" serve-init --dir "$OPENNEKO_DEV_STATE/serve-credentials" --graphjin-config ""
exec "$OPENNEKO_DEV_STATE/bin/openneko" serve \
  --graphjin-config "" \
  --subscriptions-addr "" \
  --credentials "$OPENNEKO_DEV_STATE/serve-credentials" \
  --addr "127.0.0.1:${OPENNEKO_SERVE_PORT:-8090}" \
  --files-dir "$OPENNEKO_DEV_STATE/files" \
  --index-dir "$OPENNEKO_DEV_STATE/file-index"
