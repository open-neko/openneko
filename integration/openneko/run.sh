#!/usr/bin/env bash
# Owns only the isolated OpenNeko consumer test services. No real provider credentials are used.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${OPENNEKO_TEST_SOURCE:?Point to the optional OpenNeko integration checkout}"
: "${OPENSHELL_TEST_CLI:?Point to OpenShell 0.0.116}"
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) exit 1;; esac
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o integration/openneko/model-bin ./integration/openneko/model
./adapters/openneko/build-image.sh "$OPENNEKO_TEST_SOURCE" "${AGENT_TEST_BASE_IMAGE:-openneko-agent:dev}"
compose=(docker compose -p harness-m3 -f integration/openneko/compose.yml)
trap '"${compose[@]}" down --volumes --remove-orphans' EXIT
"${compose[@]}" up -d
ready=0
for ((n=0; n<60; n++)); do
  if "${compose[@]}" exec -T metadata pg_isready -h 127.0.0.1 -U neko >/dev/null 2>&1 && curl -fsS http://127.0.0.1:18117/health >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
[[ "$ready" == 1 ]] || { echo "Isolated metadata/GraphJin readiness failed" >&2; exit 1; }
# Resolve the host address used by this Docker installation (including OrbStack).
if [[ -z ${HARNESS_HOST_GATEWAY_IP:-} ]]; then
  HARNESS_HOST_GATEWAY_IP=$(docker run --rm debian:bookworm-slim getent ahostsv4 host.docker.internal | awk 'NR==1 {print $1}')
  export HARNESS_HOST_GATEWAY_IP
fi
./integration/run.sh adapters/openneko/consumer-check.sh
# The outer transport suite reports its documented upstream cancellation gap.
