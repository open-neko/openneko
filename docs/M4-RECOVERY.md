# M4 progress: host admission and completion receipts

The M3 Go checkpoint was durable inside the sandbox, but the host retrieved it only
after execution. A worker death left a deterministic sandbox name that the legacy
launcher would reclaim by deleting. The boot-time orphan reaper could also delete
it. Both paths could destroy evidence before recovery.

The optional OpenNeko adapter now writes an exclusive, fsynced admission record
before staging or creating a Harness sandbox. It hashes the accepted input, run,
tenant, thread, principal/authorization revision, model route, image and trusted
environment. Records live under the host's `runs/.harness-launches/<run-hash>`;
that sibling directory is not staged into the sandbox. There are no credentials or
prompts in the admission record, only its fingerprint. Result receipts contain
application content and need the same host retention/access policy as checkpoints.

After execution, the launcher retrieves the Go checkpoint and atomically saves a
bounded 8 MiB host completion receipt before deleting the sandbox. A duplicate with
the same fingerprint returns the receipt without contacting OpenShell. A changed
input or authorization scope is rejected. Concurrent launches admit one owner.
Incomplete/corrupt records produce `outcome unknown` and prohibit automatic
relaunch, even when a Go checkpoint happens to be present.

Harness sandboxes are labelled `openneko.recovery=retain`. The generic restart
reaper skips them, and name-collision handling cannot delete them. An execution or
checkpoint error retains the sandbox. Explicit cancellation still tears down the
process boundary; it may leave an unresolved admission, which cannot be retried
silently. Broker capabilities are released in existing cleanup paths. Hermes
continues using its original warm pool, collision recovery and orphan cleanup.

## Verification

- Real subprocess SIGKILL after admission: a new process cannot relaunch the run.
- Real subprocess SIGKILL after durable completion: the saved result is recovered.
- Concurrent admission, changed input/scope and corrupt/version-mismatched receipts.
- Launcher name collision and missing result stream: sandbox not deleted; redelivery
  performs no additional CLI calls.
- Restart reaper preserves recovery-labelled boxes and still deletes old Hermes boxes.
- Real OpenShell → Go/Ax → broker → GraphJin lookup, completed host-receipt replay,
  altered-input rejection, and unresolved-admission rejection passed.
- The production pg-boss handler completed live run
  `6f36f348-5cbf-4ebc-83d1-d1d6fa2bf02e`; phase observations include
  `sandbox.harness_admission` and `sandbox.harness_receipt`.

The real integration command is unchanged: `integration/m3/run.sh` with the
consumer checkout and pinned CLI variables documented in its README. Its final
nonzero exit remains the known M2 upstream idle-stream cancellation failure;
M4 changes do not resolve that proxy limitation.

## Remaining M4 work

This is a conservative admission fence, not automatic recovery. It does not infer
that an external effect did or did not happen. Interrupted attempts need a scoped
reconciliation flow that inspects retained evidence and remote operation status.
A retained sandbox can keep running until its deadline if the worker dies; the
reconciler must terminate or fence old execution before adopting it. A host crash
after receipt commit but before deletion can leave an orphan; replay does not yet
clean that orphan automatically.

Still required: host-owned per-operation journal in the existing database/queue,
controlled mutation crash matrix, explicit idempotency/status contracts, durable
approval continuations with current authorization checks, remote cancellation
reconciliation, and retention/cleanup for unresolved sandboxes. No production
mutations are enabled, and this work does not claim M4 completion or exactly-once
effects. Arbitrary Go/Ax continuation remains disabled.
