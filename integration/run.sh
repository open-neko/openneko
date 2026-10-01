#!/usr/bin/env bash
# Owns only the fixed, isolated M2 test project. Never targets the active gateway.
set -euo pipefail
cd "$(dirname "$0")/.."
consumer_checks=${1:-}
[[ $# -le 1 && ( -z "$consumer_checks" || -f "$consumer_checks" ) ]] || { echo "Usage: $0 [consumer-check-script]" >&2; exit 1; }
version=${HARNESS_OPENSHELL_VERSION:-0.1.2}
[[ "$version" == '0.0.116' || "$version" == '0.1.2' ]] || { echo 'Only explicitly qualified OpenShell versions are supported' >&2; exit 1; }
export HARNESS_OPENSHELL_VERSION="$version"
cli=${OPENSHELL_TEST_CLI:?Set OPENSHELL_TEST_CLI to a verified OpenShell CLI binary}
[[ "$("$cli" --version)" == "openshell $version" ]] || { echo "OpenShell $version required" >&2; exit 1; }
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
printf '%s\n' 'subjectAltName=DNS:model-fixture,DNS:host.openshell.internal' 'basicConstraints=critical,CA:FALSE' 'extendedKeyUsage=serverAuth' > "$state/tls/extensions"
openssl x509 -req -in "$state/tls/fixture.csr" -CA "$state/tls/ca.crt" -CAkey "$state/tls/ca.key" -CAcreateserial -days 1 -extfile "$state/tls/extensions" -out "$state/tls/fixture.crt" >/dev/null 2>&1
cp "$state/tls/ca.crt" integration/fixture.crt
docker build -q -t harness-m2:local integration
gateway_schema=1
if [[ "$version" == '0.1.2' ]]; then gateway_schema=2; fi
supervisor_image=''
if [[ "$version" == '0.1.2' ]]; then
  docker build -q -f integration/Dockerfile.supervisor-tls -t harness-m2-supervisor:local integration
  supervisor_image='supervisor_image = "harness-m2-supervisor:local"'
fi
callback_endpoint='https://openshell-gateway:18116'
if [[ "$version" == '0.1.2' ]]; then
  # The 0.1.2 supervisor uses host networking; the gateway stays in Compose.
  # Reach its loopback-published port without depending on Compose DNS.
  callback_endpoint='https://127.0.0.1:18116'
fi
legacy_docker_network='network_name = "harness-m2"
host_gateway_ip = "'"${HARNESS_HOST_GATEWAY_IP:-172.30.116.2}"'"'
if [[ "$version" == '0.1.2' ]]; then legacy_docker_network=''; fi
cat > "$state/gateway.toml" <<TOML
[openshell]
version = $gateway_schema
[openshell.gateway.otlp]
endpoint = "http://otel-collector:4317"
service_name = "harness-m2-gateway"
[openshell.drivers.docker]
grpc_endpoint = "$callback_endpoint"
$legacy_docker_network
$supervisor_image
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
model_http='http://model-fixture:8080' model_https='https://model-fixture:8443'
provider_file=integration/provider.yaml policy_file=integration/policy.yaml
if [[ "$version" == '0.1.2' ]]; then
  model_http='http://host.openshell.internal:18080'
  model_https='https://host.openshell.internal:18443'
  provider_file="$state/provider-012.yaml" policy_file="$state/policy-012.yaml"
  sed -e 's/model-fixture/host.openshell.internal/g' -e 's/8443/18443/g' -e 's/8080/18080/g' integration/provider.yaml > "$provider_file"
  sed -e 's/model-fixture/host.openshell.internal/g' -e 's/8443/18443/g' -e 's/8080/18080/g' integration/policy.yaml > "$policy_file"
fi
"${oss[@]}" provider profile import --file "$provider_file"
credential_key=api_key alternate_credential_key=alternate_key
if [[ "$version" == '0.1.2' ]]; then credential_key=MODEL_API_KEY alternate_credential_key=ALTERNATE_API_KEY; fi
"${oss[@]}" provider create --name harness-m2 --type harness-m2 --credential "$credential_key=synthetic-M2-credential"
sed -e 's/harness-m2/harness-m2-alt/g' -e 's/api_key/alternate_key/g' -e 's/MODEL_API_KEY/ALTERNATE_API_KEY/g' -e 's|/v1/|/alternate/v1/|g' "$provider_file" > "$state/alternate-provider.yaml"
"${oss[@]}" provider profile import --file "$state/alternate-provider.yaml"
"${oss[@]}" provider create --name harness-m2-alt --type harness-m2-alt --credential "$alternate_credential_key=synthetic-M2-alternate"
if [[ ${HARNESS_M5_FAST:-0} == 1 ]]; then
  [[ -n "$consumer_checks" ]] || { echo 'HARNESS_M5_FAST requires consumer checks' >&2; exit 1; }
  echo 'M5_FAST_GATEWAY_READY'
  bash "$consumer_checks"
  exit 0
fi
# v0.0.116 treats this as the canonical main process; a short `true` can exit
# before readiness. Keep the workload alive and use exec for each probe.
"${oss[@]}" sandbox create --name harness-m2-probe --from harness-m2:local --provider harness-m2 --provider harness-m2-alt --no-auto-providers --no-tty --detach --policy "$policy_file" -- sleep infinity
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="${MODEL_API_KEY:-$api_key}"; exec /usr/local/bin/harness-probe -url "$1"' sh "$model_http"
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="${MODEL_API_KEY:-$api_key}"; exec /usr/local/bin/harness-probe -deny -url "$1"' sh "$model_http"
# Do not accept a DNS/connectivity failure as evidence of binary-policy denial.
set +e
status=$("${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 10 -- curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$model_http/v1/chat/completions" 2>"$state/curl-denial.log")
curl_exit=$?
set -e
if [[ "$version" == '0.1.2' ]]; then
  # The 0.1.2 network=none workload fence denies this unlisted binary at
  # connect time. The allowed Go probe above reached the same origin first;
  # require curl to resolve it and fail at connect, never at DNS lookup.
  [[ "$curl_exit" == 7 && "$status" == 000 ]] &&
    grep -q 'Failed to connect to host.openshell.internal port 18080' "$state/curl-denial.log" ||
    { echo "Expected unlisted curl connect denial: exit=$curl_exit status=$status" >&2; cat "$state/curl-denial.log" >&2; exit 1; }
else
  [[ "$curl_exit" == 0 && "$status" == 403 ]] || { echo "Expected curl policy denial, got exit=$curl_exit HTTP $status" >&2; exit 1; }
fi
"${compose[@]}" logs --no-log-prefix model-fixture | grep -q '"check":"upstream_auth_verified","ok":true'
source integration/credentials.sh
# Inspect actual collector output, not gateway log declarations.
python3 integration/check-telemetry.py "$state/telemetry/traces.jsonl"
# Optional consumer checks reuse this isolated gateway; none are required by default.
if [[ -n "$consumer_checks" ]]; then bash "$consumer_checks"; fi
# HTTPS uses the same real proxy and synthetic credential binding.
if ! "${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="${MODEL_API_KEY:-$api_key}"; exec /usr/local/bin/harness-probe -url "$1"' sh "$model_https"; then
  "${compose[@]}" logs --tail 30 --no-log-prefix model-fixture | rg 'TLS handshake error|upstream_auth_verified' >&2 || true
  exit 1
fi
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="${MODEL_API_KEY:-$api_key}" SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt SSL_CERT_DIR=/nonexistent; exec /usr/local/bin/harness-probe -url "$1" -untrusted' sh "$model_https"
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="${MODEL_API_KEY:-$api_key}"; exec /usr/local/bin/harness-probe -url "$1" -cancel' sh "$model_https"
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
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="${MODEL_API_KEY:-$api_key}"; exec /usr/local/bin/harness-probe -url "$1" -cancel' sh "$model_https"
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
