#!/usr/bin/env bash
# Owns only the isolated OpenNeko consumer test services. The opt-in M6
# held-out gate accepts real credentials through its environment preflight.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${OPENNEKO_TEST_SOURCE:?Point to the optional OpenNeko integration checkout}"
: "${OPENSHELL_TEST_CLI:?Point to a matched OpenShell CLI}"
if [[ ${HARNESS_M6_HELDOUT_ONLY:-0} == 1 ]]; then
  source integration/m6-heldout/preflight.sh
fi
base_image=${AGENT_TEST_BASE_IMAGE:-openneko-agent:dev}
if ! docker image inspect "$base_image" >/dev/null 2>&1; then
  echo "OpenNeko agent base image is unavailable locally: $base_image" >&2
  echo "Build it from the selected OpenNeko checkout with: docker build --target agent -t openneko-agent:dev '$OPENNEKO_TEST_SOURCE'" >&2
  exit 1
fi
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) exit 1;; esac
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o integration/openneko/model-bin ./integration/openneko/model
if [[ ${HARNESS_M6_COMPACTION_ONLY:-0} == 1 ]]; then
  scopeprobe=$(mktemp "${TMPDIR:-/tmp}/harness-scopeprobe.XXXXXX")
  go build -o "$scopeprobe" ./integration/openneko/scopeprobe
  export HARNESS_SCOPE_PROBE_BIN="$scopeprobe"
fi
./adapters/openneko/build-image.sh "$OPENNEKO_TEST_SOURCE" "$base_image"
compose=(docker compose -p harness-m3 -f integration/openneko/compose.yml)
trap '"${compose[@]}" down --volumes --remove-orphans; if [[ -n ${scopeprobe:-} ]]; then rm -f "$scopeprobe"; fi' EXIT
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
