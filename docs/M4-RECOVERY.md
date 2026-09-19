# M4 progress: automatic terminal reconciliation

The optional OpenNeko adapter fences a run before sandbox creation using an
exclusive, fsynced host admission record. The fingerprint binds input, run, tenant,
thread, principal, tool policy, allowed skills, model route, image and environment.
Records live in `runs/.harness-launches/<run-hash>`, outside the uploaded workspace.
Changed input or scope is rejected before inspecting or returning evidence.

Every Harness launch holds a POSIX file lock through `harness-inspect --lock DIR`.
The helper signals readiness and holds the lock until the host closes stdin; host
SIGKILL closes that pipe too. Another live owner is refused, with no timeout-based
lock stealing. Unexpected helper exit aborts execution and prevents normal receipt
publication/cleanup. All deliveries of a run must share the same persistent POSIX
filesystem with working flock semantics. Separate host disks are not a distributed
ownership scheme; database fencing is required before that deployment topology.
Hermes does not use this helper or change its existing warm-pool lifecycle.

On redelivery, a valid host receipt is returned. If the receipt is missing, the
launcher automatically inspects the downloaded Go checkpoint, or invokes the Go
inspector in the retained sandbox when no local checkpoint exists. It supplies the
exact trusted run specification, including remapped paths and user message. The
inspector acquires the Go execution lock, validates bounded checkpoint contents,
and classifies the outcome:

- `terminal`: adopt the saved result through the same mapper as live execution.
- `interrupted`: retained read evidence, but no final answer; do not re-execute.
- `outcome_unknown`: a durable intent lacks a result; do not re-execute.

Busy locks, corrupt/version-mismatched records, conflicting input, unavailable
sandboxes and invalid inspection output fail closed. A corrupt local checkpoint
is not silently replaced. Recovery never calls a model or tool. The terminal host
receipt is atomically saved and fsynced before sandbox deletion. Receipt replay
also retries deletion, covering a crash between receipt publication and cleanup.
Cleanup failure preserves the receipt and is visible in phase telemetry.

Harness sandboxes carry `openneko.recovery=retain`; generic restart reaping and
name-collision handling cannot destroy unresolved evidence. Explicit cancellation
still tears down the sandbox process boundary and can leave an unresolved admission.
Local cancellation does not prove an upstream operation stopped. Checkpoints and
result receipts contain application content and need host access/retention controls.

## Packaging and inspection

Install the matching native `harness-inspect` on every worker/web host that can
launch Harness, on PATH or via absolute `HARNESS_INSPECT_BIN`. Missing helpers fail
before launch. `adapters/openneko/build-image.sh` also installs the Linux inspector
inside the sandbox. No inspector provider credentials or model egress are needed.

```sh
go build -o bin/harness-inspect ./cmd/harness-inspect
HARNESS_STATE_DIR=/trusted/run/.harness ./bin/harness-inspect < accepted-run.json
```

Inspection never changes the checkpoint. Its JSON includes operation content;
only source/outcome classifications and phase timings go to recovery telemetry.
Normal replay and inspection share validation of version, exact input, event
sequence, tool/result pairing, bounded operations and consistent terminal results.

## Verification

`go test -race ./...` covers active execution locks, saved versus unknown evidence,
corrupt checkpoints and rejection by inspection and normal replay. Product tests
cover admission/receipt conflicts, corrupt receipts, terminal adoption, preserved
Hermes cleanup, and actual host SIGKILL while holding the native helper lock.
Run the lock test with `HARNESS_INSPECT_BIN` pointing to the built host binary.

The isolated `integration/m3/run.sh` suite runs a real OpenShell sandbox, Go/Ax,
broker, GraphJin and PostgreSQL. It injects a checkpoint-transfer failure after
execution, recovers from the retained sandbox, then removes the receipt and
recovers from the downloaded checkpoint. Concurrent recovery admits one owner.
Repeated unknown-operation recovery stays blocked. Model request counters must
remain unchanged across every recovery. The production pg-boss run follows this
check. No browser UI changed in this slice; earlier M3 browser qualification is
not a claim of a fresh browser recovery test.

Verified 2026-09-19: Go race tests and vet passed; 99 product tests passed (six
metadata-DB-dependent resolver tests skipped in the local regression command),
worker typechecking passed, and the live recovery assertions passed. The production
queue run `afc0aa95-b727-4223-9065-5ec442ba430f` completed.

The suite's final nonzero exit remains the known M2 upstream idle-stream
cancellation failure. Terminal reconciliation does not fix that proxy limitation.

## Remaining M4 work

This completes automatic terminal reconciliation for the current read-only slice,
not all M4 acceptance gates. Still required: distributed database ownership and
per-operation journals, governed mutation/idempotency crash tests, durable approval
continuations with fresh authorization, remote cancellation reconciliation, and
retention policy for unresolved sandboxes. No mutation capability or arbitrary
Go/Ax continuation is enabled. Transcript pairing is not exactly-once effects.
