#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
product=${1:?Usage: build-image.sh /absolute/OpenNeko/checkout [base-image]}
base=${2:-openneko-agent:dev}
state=$(mktemp -d)
trap 'rm -rf "$state"' EXIT
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) exit 1;; esac
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$state/harness-openneko" ./adapters/openneko/cmd/harness
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$state/harness-inspect" ./cmd/harness-inspect
(cd "$product/apps/worker" && pnpm exec esbuild src/agent-sandbox/entry.ts --bundle --platform=node --format=esm --banner:js="import { createRequire } from 'node:module'; const require = createRequire(import.meta.url);" --outfile="$state/entry.js")
cat > "$state/Dockerfile" <<'DOCKER'
ARG BASE=openneko-agent:dev
FROM ${BASE}
USER root
COPY harness-openneko /usr/local/bin/harness-openneko
COPY harness-inspect /usr/local/bin/harness-inspect
COPY entry.js /app/entry.js
DOCKER
docker build --build-arg "BASE=$base" -t harness-openneko:m3 "$state"
