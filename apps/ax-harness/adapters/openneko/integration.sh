#!/usr/bin/env bash
# Optional consumer checks, invoked by integration/run.sh after transport checks.
set -euo pipefail
cli=${OPENSHELL_TEST_CLI:?}
state=${HARNESS_STATE:?}
oss=("$cli" --gateway harness-m2)
cleanup() {
  "${oss[@]}" sandbox delete harness-m2-short >/dev/null 2>&1 || true
  "${oss[@]}" sandbox delete harness-m2-failed >/dev/null 2>&1 || true
}
trap cleanup EXIT
# Reproduce the current launcher's cold-create command against the new release.
if "${oss[@]}" sandbox create --name harness-m2-short --from harness-m2:local --no-auto-providers --no-tty --policy integration/policy.yaml -- /bin/sh -lc true > "$state/legacy-create.log" 2>&1; then
  echo 'Legacy short main succeeded; re-evaluate the documented lifecycle migration.' >&2
  exit 1
fi
grep -q 'MainProcessExited' "$state/legacy-create.log"
printf '%s\n' '{"check":"legacy_short_main_incompatible","confirmed":true}'
"${oss[@]}" sandbox delete harness-m2-short
go build -o "$state/openshell-compat" ./adapters/openneko/cmd/openshell-compat
mkdir "$state/input"
printf 'uploaded-by-legacy-launcher' > "$state/input/marker"
# Feed the existing launcher's no-op command to the external adapter unchanged.
HARNESS_OPENSHELL_BIN="$cli" "$state/openshell-compat" --gateway harness-m2 sandbox create --name harness-m2-short --from harness-m2:local --no-auto-providers --no-tty --policy integration/policy.yaml --upload "$state/input:/sandbox" --no-git-ignore -- /bin/sh -lc true
actual=$("${oss[@]}" sandbox exec -n harness-m2-short --no-tty --timeout 10 -- cat /sandbox/input/marker)
[[ "$actual" == uploaded-by-legacy-launcher ]] || { echo 'Legacy staging/exec failed through adapter' >&2; exit 1; }
"${oss[@]}" sandbox delete harness-m2-short
printf '%s\n' '{"check":"legacy_launcher_adapter_upload_exec_delete","ok":true}'

# A failed second-stage upload must not strand the successfully created sandbox.
if HARNESS_OPENSHELL_BIN="$cli" "$state/openshell-compat" --gateway harness-m2 sandbox create --name harness-m2-failed --from harness-m2:local --no-auto-providers --no-tty --policy integration/policy.yaml --upload "$state/missing:/sandbox" -- /bin/sh -lc true; then
  echo 'Missing upload source unexpectedly succeeded' >&2
  exit 1
fi
remaining=$("${oss[@]}" sandbox list)
if grep -q 'harness-m2-failed' <<< "$remaining"; then
  echo 'Failed upload leaked its sandbox' >&2
  exit 1
fi
printf '%s\n' '{"check":"legacy_launcher_upload_failure_cleanup","ok":true}'
