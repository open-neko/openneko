# Harness stocktake

Reviewed 2026-09-26 against the current Harness worktree and milestone plan.

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

## Milestone audit

| Stage | Current evidence | Remaining exit work |
| --- | --- | --- |
| M1–M4 | Locally qualified at their bounded scope; recovery and real OpenShell/consumer checks recorded above | Preserve regression coverage as capabilities expand; accepted idle-proxy cancellation remains observable |
| M5a catalog | Native/direct/MCP fixture calls share Ax, schema admission, operation journal and catalog binding | Package and compare the full consumer capability inventory and recovery behavior on connected runs |
| M5b reads/batch | One seeded memory read passed the worker/OpenShell/broker/MCP bridge; controlled query-to-file runner and scoped batch-read broker grant pass local fixtures | Admit the batch as host-owned work, connect GraphJin data, publish/download the CSV, then qualify records/library reads and clarification/UI paths |
| M5c mutations | Pack-action proposal is narrowed to host-admitted action kinds and governed effects | Qualify remaining product writes at their actual effect boundaries, including crash and duplicate delivery |
| M5d local work | Opt-in Go file Read/Edit/Write/search uses `os.Root`, read-version checks, create-only writes, read/write exclusion and durable state restoration; feature OpenNeko backend now passes a separate staged upload root to read-only upload tools; local Ax journal, resume and race checks pass | Verify upload reads through worker/OpenShell, mount only an unprivileged mutable run workspace, add process/skill/artifact paths, prove hostile-secret isolation and browser download |
| M5e delegation | Ax API and ownership design researched | Implement narrowed child execution with shared budgets and cancellation, then connected verification |
| M6 efficiency | Durable pre-call model request limit survives resume; two-call regression passes | Token/cost accounting, approved routing/fallback, context references/compaction and connected multi-route tests |
| M7 parity | Capability inventory and evaluation rules documented | Full Hermes outcome comparison, tenant/load checks and task-quality evidence |
| M8 rollout | Branch and local integration path exist | Staging upgrade/rollback, canary, hosted PR checks and separately budgeted live-provider smoke |

Local tests do not close M5b–M8. The Daily Lead Union case still lacks a connected
GraphJin source and the worker/web artifact path. No live Gemini key was used.

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

All owned M2/M3 test services and volumes were cleaned up. The local Go race
suite passes on 2026-09-26. The demo stack remains stopped to respect the Mac's
Docker memory limit; no current live GraphJin database is connected. Browser
acceptance for the batch artifact and the remaining M5 tool families is open.
