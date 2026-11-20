#!/usr/bin/env bash
# Usage: recipes/sandbox/prove-desktop.sh [sandbox image]
# Starts compose.desktop.yml under its own project and prints one PASS or
# FAIL line per check: the sandbox answers on 127.0.0.1 with its key only,
# and neither the sandbox nor a command in it has a route out. With no
# image, it builds one from this folder. Set OPENNEKO_BIN to an openneko
# executable to add its release-check. Needs bash, curl and Docker.
set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
image="${1:-}"
port="${OPENNEKO_PROVE_PORT:-18091}"
project="openneko-sandbox-desktop-proof"
failed=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; failed=1; }

if [ -z "$image" ]; then
  image=openneko-sandbox:desktop-proof
  echo "building $image from $here"
  docker build -q -t "$image" "$here" >/dev/null || { echo "FAIL the image does not build"; exit 1; }
fi
key="$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')"
run="run-proof$(head -c 6 /dev/urandom | od -An -tx1 | tr -d ' \n')"
compose() {
  OPENNEKO_SANDBOX_IMAGE="$image" OPENNEKO_SANDBOX_KEY="$key" OPENNEKO_SANDBOX_PORT="$port" \
    docker compose -p "$project" -f "$here/compose.desktop.yml" ${OPENNEKO_PROVE_OVERLAY:+-f "$OPENNEKO_PROVE_OVERLAY"} "$@"
}
trap 'compose down -v -t 1 >/dev/null 2>&1' EXIT

compose down -v -t 1 >/dev/null 2>&1
if ! compose up -d --wait --wait-timeout 120; then
  compose logs --tail 40
  echo "FAIL the recipe does not start"
  exit 1
fi
url="http://127.0.0.1:$port"
for _ in $(seq 1 60); do
  curl -fsS "$url/v2/health" >/dev/null 2>&1 && break
  sleep 1
done

published="$(compose port openneko-sandbox-publish 8091)"
case "$published" in
  127.0.0.1:*) pass "the sandbox port is published on $published only" ;;
  *) fail "the sandbox port is published on '$published', want 127.0.0.1" ;;
esac

code="$(curl -s -o /dev/null -w '%{http_code}' "$url/v2/check")"
[ "$code" = 401 ] && pass "a request with no key gets 401" || fail "a request with no key gets $code, want 401"
code="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer wrong-$key" "$url/v2/check")"
[ "$code" = 401 ] && pass "a request with a wrong key gets 401" || fail "a request with a wrong key gets $code, want 401"

check="$(curl -s -H "Authorization: Bearer $key" "$url/v2/check")"
case "$check" in
  *'"egress":"closed"'*) pass "the sandbox reports egress closed: $check" ;;
  *) fail "the sandbox check says $check, want egress closed" ;;
esac

exec_in_run() {
  local fresh="$1" script="$2" body
  body="$(printf '{"fresh":%s,"command":"echo %s | base64 -d | bash","timeout_seconds":90,"max_output":20000}' \
    "$fresh" "$(printf '%s' "$script" | base64 | tr -d '\n')")"
  curl -s -X POST -H "Authorization: Bearer $key" -H 'Content-Type: application/json' -d "$body" "$url/v2/runs/$run/exec"
}

answer="$(exec_in_run true 'echo "hello from $(id -u)"')"
case "$answer" in
  *'"exit_code":0'*'hello from '*|*'hello from '*'"exit_code":0'*) pass "a command runs in the sandbox" ;;
  *) fail "a command does not run: $answer" ;;
esac

container() { compose ps -q "$1"; }
address() { docker inspect -f "{{(index .NetworkSettings.Networks \"$2\").IPAddress}}" "$(container "$1")"; }
inner="${project}_openneko-sandbox"
outer="${project}_openneko-sandbox-publish"
publish_ip="$(address openneko-sandbox-publish "$inner")"
egress_ip="$(address openneko-egress "$inner")"

probe='probe() { if timeout 5 bash -c "exec 3<>/dev/tcp/$2/$3" 2>/dev/null; then echo "$1 connected"; else echo "$1 blocked"; fi; }
dns() { if timeout 5 getent ahosts "$1" >/dev/null 2>&1; then echo "dns:$1 connected"; else echo "dns:$1 blocked"; fi; }
'
outside="probe public:1.1.1.1:443 1.1.1.1 443 &
probe public:8.8.8.8:53 8.8.8.8 53 &
probe host:host.docker.internal:$port host.docker.internal $port &
probe metadata:169.254.169.254:80 169.254.169.254 80 &
dns example.com &
python3 -c '
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.settimeout(4)
try:
    s.sendto(bytes.fromhex(\"123401000001000000000000076578616d706c6503636f6d0000010001\"), (\"8.8.8.8\", 53)); s.recv(512); print(\"udp-dns:8.8.8.8 connected\")
except OSError:
    print(\"udp-dns:8.8.8.8 blocked\")' &
wait"
inside="probe publish:$publish_ip:8091 $publish_ip 8091 &
probe egress-direct:$egress_ip:3128 $egress_ip 3128 &
wait"
own="probe own-listener:127.0.0.1:8091 127.0.0.1 8091 &
wait"

control() { docker run --rm --network "$1" --entrypoint bash "$image" -c "$probe$2"; }
result() { grep -E "^$1 (connected|blocked)\$" <<<"$2" | awk '{print $2}'; }
from_run() { exec_in_run false "$probe$1" | sed 's/\\n/\n/g' | grep -oE '[a-z-]+:[^ "\\]+ (connected|blocked)'; }

control_out="$(control "$outer" "$outside")"
control_in="$(control "$inner" "$inside")"
for need in public:1.1.1.1:443 public:8.8.8.8:53 dns:example.com udp-dns:8.8.8.8; do
  if [ "$(result "$need" "$control_out")" != connected ]; then
    fail "control: $need is not reachable from an open network here, so a blocked result proves nothing"
  fi
done
for need in "publish:$publish_ip:8091" "egress-direct:$egress_ip:3128"; do
  if [ "$(result "$need" "$control_in")" != connected ]; then
    fail "control: $need is not reachable from the sandbox network, so a blocked result proves nothing"
  fi
done

sandbox_out="$(compose exec -T openneko-sandbox bash -c "$probe$outside")"
run_out="$(from_run "$outside")"
run_in="$(from_run "$inside")"
run_own="$(from_run "$own")"
while read -r label; do
  [ -z "$label" ] && continue
  if [ "$(result "$label" "$control_out")" != connected ]; then
    echo "SKIP $label: the control does not reach it either"
    continue
  fi
  [ "$(result "$label" "$sandbox_out")" = blocked ] && pass "the sandbox container cannot reach $label" || fail "the sandbox container reaches $label"
  [ "$(result "$label" "$run_out")" = blocked ] && pass "a command cannot reach $label" || fail "a command reaches $label ($(result "$label" "$run_out"))"
done < <(awk '{print $1}' <<<"$control_out")
for label in "publish:$publish_ip:8091" "egress-direct:$egress_ip:3128"; do
  [ "$(result "$label" "$run_in")" = blocked ] && pass "a command cannot reach $label" || fail "a command reaches $label ($(result "$label" "$run_in"))"
done
[ "$(result own-listener:127.0.0.1:8091 "$run_own")" = blocked ] && pass "a command cannot reach the sandbox's own listener" || fail "a command reaches the sandbox's own listener"

routes="$(compose exec -T openneko-sandbox cat /proc/net/route)"
if awk 'NR > 1 && $2 == "00000000" { found = 1 } END { exit !found }' <<<"$routes"; then
  fail "the sandbox container has a default route"
else
  pass "the sandbox container has no default route"
fi
forward="$(compose exec -T openneko-sandbox-publish cat /proc/sys/net/ipv4/ip_forward | tr -d '\r')"
[ "$forward" = 0 ] && pass "the publish container does not route packets (ip_forward=0)" || fail "the publish container has ip_forward=$forward"

if [ -n "${OPENNEKO_BIN:-}" ]; then
  if out="$(OPENNEKO_SANDBOX_URL="$url" OPENNEKO_SANDBOX_KEY="$key" "$OPENNEKO_BIN" release-check --sandbox 2>&1)" && grep -q '^sandbox_egress=closed' <<<"$out"; then
    pass "openneko release-check --sandbox: $(tr '\n' ' ' <<<"$out")"
  else
    fail "openneko release-check --sandbox: $out"
  fi
else
  echo "SKIP openneko release-check: set OPENNEKO_BIN to run it"
fi

curl -s -X DELETE -H "Authorization: Bearer $key" "$url/v2/runs/$run" >/dev/null
if [ "$failed" = 0 ]; then echo "PASS all checks"; else echo "FAIL one or more checks"; fi
exit "$failed"
