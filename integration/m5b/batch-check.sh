#!/usr/bin/env bash
set -euo pipefail
HARNESS_BATCH_TEST_GATEWAY=harness-m2 go test ./integration/m5b -run '^TestOpenShellBatchCompartment$' -count=1 -v
