#!/bin/sh
# Starts the development backing services in Docker. Web and worker run on
# the host with hot reload: `pnpm dev`.
set -eu

. scripts/dev-env.sh

compose() {
  docker compose -f compose.yml -f compose.adventureworks.yml -f compose.dev.yml "$@"
}

mkdir -p "$OPENNEKO_DEV_STATE/graphjin" "$OPENNEKO_CONFIG_VOLUME"
printf '%s\n' "$COMPOSE_PROJECT_NAME" > "$OPENNEKO_DEV_STATE/compose-project"

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

compose up -d --wait neko-db
(cd apps/openneko && go run ./cmd/openneko migrate)

# GraphJin in serve reads its auth key only at start, so the per-org secret
# goes into agentic.yml before serve starts.
compose build openneko-serve
compose run --rm --no-deps openneko-serve-init

# Compose reuses a built image after the GraphJin pin moves. Rebuild
# neko-graphjin when it reports another version, or the worker stops at start.
if ! compose run --rm --no-deps -T --entrypoint graphjin neko-graphjin version 2>/dev/null \
  | grep -q "GraphJin $graphjin_version"; then
  echo "Rebuilding neko-graphjin for GraphJin $graphjin_version"
  compose build neko-graphjin
fi

compose up -d \
  neko-graphjin openneko-serve \
  adventureworks-simulator adventureworks-scenario-injector

echo "Backing services are up. Start web and worker with: pnpm dev"
