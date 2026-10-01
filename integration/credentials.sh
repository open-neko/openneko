# Sourced by run.sh: all state belongs to the isolated qualification gateway.
probe() {
  "${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="${MODEL_API_KEY:-$api_key}"; exec /usr/local/bin/harness-probe "$@"' sh "$@"
}
# Provider watchers converge asynchronously; bound polls and every exec.
await_probe() {
  for ((n=0; n<${PROVIDER_POLL_ATTEMPTS:-15}; n++)); do
    if probe -url "$model_http" "$@" > "$state/provider-probe.log" 2>&1; then cat "$state/provider-probe.log"; return; fi
    sleep 1
  done
  cat "$state/provider-probe.log" >&2; return 1
}
# Query replacement is a Go HTTP transport check; the Ax adapter uses Bearer auth.
probe -query -url "$model_http"
"${compose[@]}" exec -T model-fixture /usr/local/bin/harness-probe -configure-key synthetic-M2-rotated
"${oss[@]}" provider update harness-m2 --credential "$credential_key=synthetic-M2-rotated"
await_probe
printf '%s\n' '{"check":"static_credential_rotation","ok":true}'
# Distinct provider slots and endpoint bindings coexist in the same sandbox.
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="${ALTERNATE_API_KEY:-$alternate_key}"; exec /usr/local/bin/harness-probe -url "$1"' sh "$model_http/alternate"
printf '%s\n' '{"check":"two_provider_bindings","ok":true}'
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty -- sh -c 'printf %s "${MODEL_API_KEY:-$api_key}" > /tmp/original-placeholder'
"${oss[@]}" sandbox provider detach harness-m2-probe harness-m2
revoked=false
revoke_started=$SECONDS
for ((n=0; n<15; n++)); do
  if "${oss[@]}" sandbox exec -n harness-m2-probe --no-tty --timeout 30 -- sh -c 'export MODEL_API_KEY="$(cat /tmp/original-placeholder)"; exec /usr/local/bin/harness-probe -revoked -url "$1"' sh "$model_http" > "$state/revocation.log" 2>&1; then revoked=true; break; fi
  sleep 1
done
[[ "$revoked" == true ]] || { cat "$state/revocation.log" >&2; exit 1; }
printf '{"check":"detach_propagation","seconds":%d}\n' "$((SECONDS-revoke_started))"
printf '%s\n' '{"check":"provider_detach","ok":true}'
"${oss[@]}" sandbox provider attach harness-m2-probe harness-m2
await_probe
# Managed refresh uses a synthetic OAuth server on the gateway's loopback.
"${compose[@]}" exec -T model-fixture /usr/local/bin/harness-probe -configure-key synthetic-M2-refreshed
"${oss[@]}" provider refresh configure harness-m2 --credential-key "$credential_key" --strategy oauth2-client-credentials \
  --material client_id=fixture \
  --material client_secret=synthetic-refresh-secret --secret-material-key client_secret \
  --credential-expires-at 2025-01-01T00:00:00Z
# The pinned gateway runs its refresh worker every 60 seconds.
PROVIDER_POLL_ATTEMPTS=75 await_probe
"${compose[@]}" logs --no-log-prefix oauth-fixture | grep -q '"check":"oauth_token_minted","ok":true'
printf '%s\n' '{"check":"managed_expiry_refresh","ok":true}'
"${oss[@]}" provider refresh delete harness-m2 --credential-key "$credential_key"
"${oss[@]}" provider update harness-m2 --credential "$credential_key=synthetic-M2-credential"
"${compose[@]}" exec -T model-fixture /usr/local/bin/harness-probe -configure-key synthetic-M2-credential
await_probe
# Restart only this suite's gateway; its database, PKI and live sandbox persist.
"${oss[@]}" sandbox exec -n harness-m2-probe --no-tty -- sh -c 'printf retained > /tmp/gateway-restart-proof'
"${compose[@]}" restart openshell-gateway
ready=false
for ((attempt=0; attempt<30; attempt++)); do
  if "${oss[@]}" sandbox list >/dev/null 2>&1; then ready=true; break; fi
  sleep 1
done
[[ "$ready" == true ]] || { echo 'Gateway restart failed' >&2; exit 1; }
await_probe
[[ "$("${oss[@]}" sandbox exec -n harness-m2-probe --no-tty -- cat /tmp/gateway-restart-proof)" == retained ]] || { echo 'Restart lost sandbox state' >&2; exit 1; }
printf '%s\n' '{"check":"gateway_restart","ok":true}'
