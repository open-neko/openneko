#!/usr/bin/env bash
# Owns only the fixed, isolated M2 test project. Never targets the active gateway.
set -euo pipefail
cd "$(dirname "$0")/.."
consumer_checks=${1:-}
[[ $# -le 1 && ( -z "$consumer_checks" || -f "$consumer_checks" ) ]] || { echo "Usage: $0 [consumer-check-script]" >&2; exit 1; }
cli=${OPENSHELL_TEST_CLI:?Set OPENSHELL_TEST_CLI to a verified OpenShell 0.0.116 binary}
[[ "$("$cli" --version)" == 'openshell 0.0.116' ]] || { echo 'OpenShell 0.0.116 required' >&2; exit 1; }
if docker network inspect harness-m2 >/dev/null 2>&1; then
  echo 'M2 network already exists; stop the previous isolated test first.' >&2
  exit 1
fi
state=$(mktemp -d "$HOME/.harness-m2.XXXXXX")
mkdir "$state/telemetry"
export HARNESS_STATE="$state" XDG_CONFIG_HOME="$state/config"
compose=(docker compose -p harness-m2 -f integration/compose.yml)
oss=("$cli" --gateway harness-m2)
cleanup() {
  "${oss[@]}" sandbox delete harness-m2-probe >/dev/null 2>&1 || true
  "${compose[@]}" down --remove-orphans >/dev/null 2>&1 || true
  if docker network inspect harness-m2 >/dev/null 2>&1; then
    echo "Cleanup incomplete; retained test state at $state" >&2
    exit 1
  fi
  rm -rf "$state"
}
trap cleanup EXIT
arch=$(docker info --format '{{.Architecture}}')
case "$arch" in aarch64|arm64) arch=arm64;; x86_64|amd64) arch=amd64;; *) echo 'Unsupported Docker architecture' >&2; exit 1;; esac
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o integration/probe-bin ./integration/probe
mkdir "$state/tls"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=Harness-Test-CA -keyout "$state/tls/ca.key" -out "$state/tls/ca.crt" >/dev/null 2>&1
openssl req -new -newkey rsa:2048 -nodes -subj /CN=model-fixture -keyout "$state/tls/fixture.key" -out "$state/tls/fixture.csr" >/dev/null 2>&1
printf '%s\n' 'subjectAltName=DNS:model-fixture' 'basicConstraints=critical,CA:FALSE' 'extendedKeyUsage=serverAuth' > "$state/tls/extensions"
openssl x509 -req -in "$state/tls/fixture.csr" -CA "$state/tls/ca.crt" -CAkey "$state/tls/ca.key" -CAcreateserial -days 1 -extfile "$state/tls/extensions" -out "$state/tls/fixture.crt" >/dev/null 2>&1
cp "$state/tls/ca.crt" integration/fixture.crt
docker build -q -t harness-m2:local integration
cat > "$state/gateway.toml" <<TOML
[openshell]
version = 1
[openshell.gateway.otlp]
endpoint = "http://otel-collector:4317"
service_name = "harness-m2-gateway"
[openshell.drivers.docker]
network_name = "harness-m2"
grpc_endpoint = "https://openshell-gateway:18116"
host_gateway_ip = "${HARNESS_HOST_GATEWAY_IP:-172.30.116.2}"
TOML
"${compose[@]}" run --rm certgen
reg="$XDG_CONFIG_HOME/openshell/gateways/harness-m2"
mkdir -p "$reg/mtls"
cp "$state/pki/ca.crt" "$reg/mtls/ca.crt"
cp "$state/pki/client/tls.crt" "$reg/mtls/tls.crt"
cp "$state/pki/client/tls.key" "$reg/mtls/tls.key"
chmod 600 "$reg/mtls/tls.key"
cat > "$reg/metadata.json" <<'JSON'
{"name":"harness-m2","gateway_endpoint":"https://127.0.0.1:18116","is_remote":false,"gateway_port":0,"auth_mode":"mtls"}
JSON
"${compose[@]}" up -d openshell-gateway model-fixture otel-collector oauth-fixture
ready=false
for ((attempt=0; attempt<60; attempt++)); do
  if "${oss[@]}" sandbox list >/dev/null 2>&1; then ready=true; break; fi
  sleep 1
done
[[ "$ready" == true ]] || { echo 'M2 gateway did not become ready' >&2; exit 1; }
"${oss[@]}" provider profile import --file integration/provider.yaml
"${oss[@]}" provider create --name harness-m2 --type harness-m2 --credential api_key=synthetic-M2-credential
sed -e 's/harness-m2/harness-m2-alt/g' -e 's/api_key/alternate_key/g' -e 's/MODEL_API_KEY/ALTERNATE_API_KEY/g' -e 's|/v1/|/alternate/v1/|g' integration/provider.yaml > "$state/alternate-provider.yaml"
"${oss[@]}" provider profile import --file "$state/alternate-provider.yaml"
"${oss[@]}" provider create --name harness-m2-alt --type harness-m2-alt --credential alternate_key=synthetic-M2-alternate
if [[ ${HARNESS_M5_FAST:-0} == 1 ]]; then
  [[ -n "$consumer_checks" ]] || { echo 'HARNESS_M5_FAST requires consumer checks' >&2; exit 1; }
  echo 'M5_FAST_GATEWAY_READY'
  bash "$consumer_checks"
  exit 0
fi
# v0.0.116 treats this as the canonical main process; a short `true` can exit
# before readiness. Keep the workload alive and use exec for each probe.
"${oss[@]}" sandbox create --name harness-m2-probe --from harness-m2:local --provider harness-m2 --provider harness-m2-alt --no-auto-providers --no-tty --detach --policy integration/policy.yaml -- sleep infinity
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="$api_key"; exec /usr/local/bin/harness-probe'
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="$api_key"; exec /usr/local/bin/harness-probe -deny'
# Do not accept a DNS/connectivity failure as evidence of binary-policy denial.
status=$("${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 10 -- curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://model-fixture:8080/v1/chat/completions)
[[ "$status" == 403 ]] || { echo "Expected curl policy denial, got HTTP $status" >&2; exit 1; }
"${compose[@]}" logs --no-log-prefix model-fixture | grep -q '"check":"upstream_auth_verified","ok":true'
source integration/credentials.sh
# Inspect actual collector output, not gateway log declarations.
python3 integration/check-telemetry.py "$state/telemetry/traces.jsonl"
# Optional consumer checks reuse this isolated gateway; none are required by default.
if [[ -n "$consumer_checks" ]]; then bash "$consumer_checks"; fi
# HTTPS uses the same real proxy and synthetic credential binding.
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="$api_key"; exec /usr/local/bin/harness-probe -url https://model-fixture:8443'
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="$api_key" SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt SSL_CERT_DIR=/nonexistent; exec /usr/local/bin/harness-probe -url https://model-fixture:8443 -untrusted'
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="$api_key"; exec /usr/local/bin/harness-probe -url https://model-fixture:8443 -cancel'
for ((attempt=0; attempt<12; attempt++)); do
  "${compose[@]}" logs --no-log-prefix model-fixture > "$state/fixture.log"
  if grep -q '"check":"upstream_stream_cancelled","ok":true' "$state/fixture.log"; then break; fi
  sleep 1
done
cancellation_observed=true
if ! grep -q '"check":"upstream_stream_cancelled","ok":true' "$state/fixture.log"; then
  cat "$state/fixture.log" >&2
  echo "Upstream idle stream did not observe cancellation within 10 seconds" >&2
  cancellation_observed=false
  printf '%s\n' '{"check":"upstream_idle_cancellation","ok":false,"severity":"warning","accepted_limitation":true}'
fi
# Qualify the hard process-boundary cleanup used by Harness cancellation too.
# Keep the accepted context-only limitation observable independently of cleanup.
prior_closed=$(grep -c '"check":"upstream_stream_cancelled","ok":true' "$state/fixture.log" || true)
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="$api_key"; exec /usr/local/bin/harness-probe -url https://model-fixture:8443 -cancel'
"${oss[@]}" sandbox delete harness-m2-probe
closed=false
for ((attempt=0; attempt<8; attempt++)); do
  "${compose[@]}" logs --no-log-prefix model-fixture > "$state/fixture.log"
  now_closed=$(grep -c '"check":"upstream_stream_cancelled","ok":true' "$state/fixture.log" || true)
  if ((now_closed > prior_closed)); then closed=true; break; fi
  sleep 1
done
[[ "$closed" == true ]] || { echo 'Sandbox deletion did not close upstream stream' >&2; exit 1; }
printf '%s\n' '{"check":"sandbox_delete_closes_upstream","ok":true}'
printf '{"check":"openshell_transport_suite","ok":true,"upstream_idle_cancellation_observed":%s}\n' "$cancellation_observed"
