#!/usr/bin/env bash
# Reject an incomplete credentialed evaluation before building or starting Docker.
[[ ${HARNESS_M5_FAST:-0} == 1 ]] || { echo 'Held-out gate requires HARNESS_M5_FAST=1' >&2; exit 1; }
[[ ${HARNESS_M6_BUSINESS_SEED:-} == "$PWD/integration/m6-heldout/seed.sql" ]] || {
  echo 'Held-out gate requires the frozen absolute seed path' >&2; exit 1;
}
for name in HARNESS_M6_MODEL_SOURCE_KEY HARNESS_M6_TRIAGE_SOURCE_KEY HARNESS_M6_MODEL_URL HARNESS_M6_MODEL_NAME HARNESS_M6_TRIAGE_URL HARNESS_M6_TRIAGE_MODEL HARNESS_M6_MODEL_INPUT_PRICE HARNESS_M6_MODEL_OUTPUT_PRICE HARNESS_M6_TRIAGE_INPUT_PRICE HARNESS_M6_TRIAGE_OUTPUT_PRICE HARNESS_M6_GRAPHJIN_INPUT_PRICE HARNESS_M6_GRAPHJIN_OUTPUT_PRICE HARNESS_M6_OUTPUT_DIR GRAPHJIN_AGENT_API_KEY GRAPHJIN_AGENT_PROVIDER GRAPHJIN_AGENT_MODEL GRAPHJIN_AGENT_REASONING GRAPHJIN_AGENT_BASE_URL; do
  [[ -n ${!name:-} ]] || { echo "Missing held-out setting: $name" >&2; exit 1; }
done
[[ "$HARNESS_M6_OUTPUT_DIR" == /* ]] || { echo 'HARNESS_M6_OUTPUT_DIR must be absolute' >&2; exit 1; }
[[ ! -e "$HARNESS_M6_OUTPUT_DIR" ]] || { echo 'Held-out output directory already exists' >&2; exit 1; }
