#!/bin/sh
set -eu

config_dir="${OPENNEKO_GRAPHJIN_CONFIG_DIR:-/config}"
request_file="${config_dir}/.openneko-graphjin-restart"
ack_file="${config_dir}/.openneko-graphjin-restart-ack"
ping_file="${config_dir}/.openneko-graphjin-supervisor-ping"
ping_ack_file="${config_dir}/.openneko-graphjin-supervisor-ping-ack"
child_pid=""
last_token=""

rm -f "${config_dir}/.openneko-graphjin-agent-key"

if [ -f "$ack_file" ]; then
  last_token=$(cat "$ack_file" 2>/dev/null || true)
fi

start_graphjin() {
  graphjin "$@" &
  child_pid=$!
}

stop_graphjin() {
  if [ -n "$child_pid" ] && kill -0 "$child_pid" 2>/dev/null; then
    kill -TERM "$child_pid" 2>/dev/null || true
    wait "$child_pid" 2>/dev/null || true
  fi
}

trap 'stop_graphjin; exit 0' TERM INT EXIT
start_graphjin "$@"

while :; do
  if ! kill -0 "$child_pid" 2>/dev/null; then
    wait "$child_pid" 2>/dev/null || true
    start_graphjin "$@"
  fi

  if [ -f "$request_file" ]; then
    token=$(cat "$request_file" 2>/dev/null || true)
    if [ -n "$token" ] && [ "$token" != "$last_token" ]; then
      stop_graphjin
      start_graphjin "$@"
      temporary="${ack_file}.$$"
      printf '%s' "$token" > "$temporary"
      mv "$temporary" "$ack_file"
      last_token="$token"
    fi
  fi
  if [ -f "$ping_file" ]; then
    ping_token=$(cat "$ping_file" 2>/dev/null || true)
    if [ -n "$ping_token" ]; then
      ping_temporary="${ping_ack_file}.$$"
      printf '%s' "$ping_token" > "$ping_temporary"
      mv "$ping_temporary" "$ping_ack_file"
    fi
  fi
  sleep 1
done
