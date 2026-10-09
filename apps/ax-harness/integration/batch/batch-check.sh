#!/usr/bin/env bash
set -euo pipefail
state=$(mktemp -d)
trap 'rm -rf "$state"' EXIT
go build -o "$state/harness-process" ./adapters/openneko/cmd/process
HARNESS_PROCESS_TEST_BIN="$state/harness-process" HARNESS_BATCH_TEST_GATEWAY=harness-m2 go test ./integration/batch -run '^TestOpenShell(BatchCompartment|Process(Compartment|FailureDoesNotPublish))$' -count=1 -v
