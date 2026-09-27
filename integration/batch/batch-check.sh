#!/usr/bin/env bash
set -euo pipefail
HARNESS_BATCH_TEST_GATEWAY=harness-m2 go test ./integration/batch -run '^TestOpenShell(BatchCompartment|Process(Compartment|FailureDoesNotPublish))$' -count=1 -v
