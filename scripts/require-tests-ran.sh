#!/usr/bin/env bash
# Usage: require-tests-ran.sh <go test -v log> <test name>...
# Fails when a test in the log skipped or a named test did not pass.
set -euo pipefail
log=$1
shift
status=0
if grep -- '--- SKIP' "$log"; then
  echo "::error::a test skipped; this step must run every one" >&2
  status=1
fi
for name in "$@"; do
  if ! grep -q -- "--- PASS: $name " "$log"; then
    echo "::error::$name did not pass" >&2
    status=1
  fi
done
exit $status
