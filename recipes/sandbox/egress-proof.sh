#!/usr/bin/env bash
# Usage: recipes/sandbox/egress-proof.sh <sandbox image>
# Proves the sandbox's egress controls with real containers: the network
# checks in TestEgressProof, then the neko_data and declared-query tests
# against a sandbox on the closed network with an empty egress list.
set -euo pipefail

image="${1:?give the sandbox image, for example openneko-sandbox:local}"
here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/../.." && pwd)"

(cd "$here" && OPENNEKO_EGRESS_PROOF_IMAGE="$image" go test -count=1 -run TestEgressProof -v -timeout 20m .)

work="$(mktemp -d)"
project=openneko-egress-data-e
compose() {
  OPENNEKO_SANDBOX_DIR="$work/socket" OPENNEKO_SANDBOX_GID="$(id -g)" OPENNEKO_SANDBOX_KEY="$key" \
    docker compose -p "$project" -f "$here/compose.native.yml" -f "$work/proof.yml" "$@"
}
key="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
mkdir "$work/socket"
cat > "$work/proof.yml" <<YAML
services:
  openneko-sandbox:
    image: $image
    build: !reset null
    pull_policy: never
    container_name: openneko-sandbox-data-e
  openneko-egress:
    image: $image
    build: !reset null
    pull_policy: never
    container_name: openneko-egress-data-e
networks:
  openneko-sandbox:
    name: openneko-sandbox-data-e
  openneko-egress:
    name: openneko-egress-data-e
volumes:
  openneko-sandbox-runs:
    name: openneko-sandbox-runs-data-e
  openneko-sandbox-homes:
    name: openneko-sandbox-homes-data-e
  openneko-egress-control:
    name: openneko-egress-control-data-e
YAML
trap 'compose down -v -t 1 >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
compose up -d --no-build --wait --wait-timeout 60
timeout 30 bash -c "until [ -S '$work/socket/sandbox.sock' ]; do sleep 0.2; done"

(cd "$repo/apps/openneko" && OPENNEKO_TEST_SANDBOX_URL="unix://$work/socket/sandbox.sock" OPENNEKO_TEST_SANDBOX_KEY="$key" \
  go test -count=1 -v -run 'TestScriptDataIn|TestScriptDataSandboxHoldsNoSecret|TestDeclaredQueriesInTheRealSandbox' ./internal/webfront)
