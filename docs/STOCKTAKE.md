# Harness stocktake

Reviewed 2026-09-20. This replaces the earlier pre-M3 inventory.

## Implemented and verified locally

The standalone Go runtime uses pinned Ax and has no dependency on an OpenNeko
checkout. Its optional OpenNeko adapter provides server-side GraphJin lookup and
governed action proposals; Hermes remains the default backend.

| Area | Completed scope | Evidence |
| --- | --- | --- |
| M1 execution | Ax streaming, governed callbacks, tool pairing, cancellation, bounded actor work and run-scoped telemetry | Go race tests, vet and HTTP/Goja compatibility tests |
| M2 transport | Real OpenShell 0.0.116, TLS/CA trust, credential replacement, destination/binary restrictions, rotation/detach, two slots, OAuth refresh, gateway restart and OTLP delivery | [Transport record](../integration/README.md) |
| M3 integration | Browser and queue entrypoints, scoped GraphJin lookup, accepted-input deduplication, result/error projection, cancellation and Hermes regression coverage | [Consumer acceptance](../integration/m3/README.md) |
| M4 recovery | Host/session ownership, durable operation receipts, bounded continuation, sandbox/host terminal reconciliation, worker-death redelivery and one restored answer | [Recovery record](M4-RECOVERY.md) |
| M4 effects | Frozen human approvals, fresh authorization before dispatch, durable execution claims, optional provider-status recovery and explicit unknown outcomes without redispatch | Real process-kill, queue, controlled HTTP effect and browser approval/reload checks in the recovery record |

The cumulative live suite has passed the consumer checks. Its previous exit 1
was solely the raw idle-proxy cancellation check. On 2026-09-20 the user accepted
this as a nonblocking upstream limitation. The runner keeps the observation as an
explicit warning and continues to require sandbox teardown to close upstream work.
Cleanup failure remains fatal. M2 and M4 are locally qualified at the documented scope.

## Accepted upstream limitation

OpenShell's idle HTTP response relay does not promptly notice downstream
disconnect. Provider work may continue and consume tokens until closure is observed
or the sandbox is torn down. The direct HTTPS control cancels; the proxied request
remains open beyond the observation window. See the exact
[source trace](OPENSHELL.md#source-trace-idle-response-cancellation).

The latest-release API checked on 2026-09-20 still reports
[v0.0.116](https://github.com/NVIDIA/OpenShell/releases/tag/v0.0.116).
Keep upstream OpenShell unchanged and qualify its eventual fix during an upgrade;
do not maintain a fork for this issue. This limitation does not block M2/M4.

## Deferred work

- M5a has local native/MCP catalog acceptance. M5c currently narrows only the
  existing pack-action proposal path. Existing OpenNeko MCP tools, other governed
  product mutations, local file/process/artifact work and bounded delegation remain
  open. This is not Hermes parity.
- M5b has one isolated OpenShell/worker/MCP memory-read acceptance with seeded
  broker data. The Daily Lead Union batch artifact, library/records reads,
  clarification and rendering still need connected acceptance. Local M5c handler
  qualification, M5d foundations and M6 budget/context work can proceed with
  fixtures; they do not prove the real batch path.
- M6: approved model routing/fallback, context compaction and aggregate budgets.
- M7: full capability parity against Hermes, task-quality evaluation, concurrent
  tenant/load checks and operational limits.
- M8: deployment upgrade, canary and rollback qualification.
- Hosted PR/CI checks and a separately budgeted live-provider smoke test. Current
  model/effect fixtures are deterministic; they establish correctness, not quality.

Recovery is bounded to persisted evidence and new Ax attempts, not arbitrary VM
resumption. Production adapters need explicit read-only status implementations
before ambiguous effects can reconcile automatically. Unknown effects, unfinished
approvals and unreconciled sandboxes are retained for operator resolution; no
purge is enabled. Queue death tests expire after the original sandbox finishes;
overlap refusal is separately tested at the live launcher boundary.

## Repository and delivery state

Both repositories use `feat/openneko-harness`. OpenNeko integration changes are in
`../Open-Neko/OpenNeko-harness-m3`; the original OpenNeko checkout was not changed.
Nothing has been pushed; Harness has no configured remote. No PR or main merge has
been made. Earlier main commits documented in README are historical.

All owned M2/M3 test services and volumes were cleaned up. M5a's local Ax/MCP
fixture, M5b's narrow memory read through OpenShell and M5c's narrowed proposal
checks are local evidence. Browser acceptance, the batch artifact and the
remaining M5c tool families are still open.
