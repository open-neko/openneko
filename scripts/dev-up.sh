#!/bin/sh
# Starts the development backing services in Docker. Web and worker run on
# the host with hot reload: `pnpm dev`.
set -eu

. scripts/dev-env.sh

compose() {
  docker compose -f compose.yml -f compose.openshell.yml -f compose.adventureworks.yml -f compose.graphjin-agent.yml -f compose.dev.yml "$@"
}

mkdir -p "$OPENNEKO_DEV_STATE/graphjin" "$OPENNEKO_DEV_STATE/records-graphjin" "$OPENNEKO_CONFIG_VOLUME" "$OPENSHELL_STATE_DIR"
if [ ! -s .openneko/development-backup-key ]; then
  (umask 077 && openssl rand -hex 32 > .openneko/development-backup-key)
fi

# Host processes validate GraphJin configs with the pinned CLI.
graphjin_version="$(sed -n 's/^export const GRAPHJIN_VERSION = "\(.*\)";$/\1/p' packages/llm/src/graphjin/version.ts)"
if ! "$OPENNEKO_DEV_STATE/bin/graphjin" version 2>/dev/null | grep -q "GraphJin $graphjin_version"; then
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')"
  asset="graphjin_${graphjin_version}_${os}_${arch}.tar.gz"
  download="$(mktemp -d)"
  base="https://github.com/dosco/graphjin/releases/download/v$graphjin_version"
  curl -fsSL -o "$download/$asset" "$base/$asset"
  curl -fsSL -o "$download/checksums.txt" "$base/checksums.txt"
  (cd "$download" && grep " $asset\$" checksums.txt | shasum -a 256 -c -)
  mkdir -p "$OPENNEKO_DEV_STATE/bin"
  tar -xzf "$download/$asset" -C "$OPENNEKO_DEV_STATE/bin" graphjin
  rm -rf "$download"
fi

compose up -d --wait neko-db records-db
(cd apps/openneko && go run ./cmd/openneko migrate)

# GraphJin reads its auth key only at start, so the per-org secret goes into
# agentic.yml before the server starts.
compose run --rm --no-deps graphjin-config-init
GRAPHJIN_SOURCES_CONFIG="$OPENNEKO_GRAPHJIN_CONFIG" \
  pnpm --filter @neko/worker exec tsx ../../packages/llm/src/graphjin/init-secret.mjs

compose up -d \
  neko-graphjin graphjin records-graphjin records-watch-graphjin \
  embedding librarian agent-image openshell-gateway \
  adventureworks-simulator adventureworks-scenario-injector

# Host processes reach the OpenShell gateway on its loopback port.
registration="${XDG_CONFIG_HOME:-$HOME/.config}/openshell/gateways/$OPENSHELL_GATEWAY"
mkdir -p "$registration/mtls"
cp "$OPENSHELL_STATE_DIR/pki/ca.crt" "$registration/mtls/ca.crt"
cp "$OPENSHELL_STATE_DIR/pki/client/tls.crt" "$registration/mtls/tls.crt"
(umask 077 && cp "$OPENSHELL_STATE_DIR/pki/client/tls.key" "$registration/mtls/tls.key")
printf '{"name":"%s","gateway_endpoint":"https://127.0.0.1:%s","is_remote":false,"gateway_port":0,"auth_mode":"mtls"}\n' \
  "$OPENSHELL_GATEWAY" "${OPENSHELL_PORT:-18080}" > "$registration/metadata.json"

# Sandboxes call the host broker; OpenShell needs the host address as an IP.
docker run --rm alpine:3.22 getent hosts host.docker.internal 2>/dev/null \
  | awk '{ print $1; exit }' > "$OPENNEKO_DEV_STATE/broker-host" || true

echo "Backing services are up. Start web and worker with: pnpm dev"
