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
| M5b reads/batch | Seeded memory search, real pgvector/entitlement library search, and an empty-registry records-only catalog turn passed worker/OpenShell/broker/MCP/Ax; records find/get passed the real bridge with a synthetic broker; controlled query-to-file runner and scoped batch-read grant pass local fixtures; a separate no-provider/no-network OpenShell sandbox completed a synthetic CSV with host-owned query handoff and mandatory deletion; response-before-receipt crash recovery passed a local no-redispatch check; run-bound sandbox inventory/deletion refuses foreign labels; the production pg-boss queue used the Go runner, OpenShell, host-only GraphJin broker and seeded `REF-42` to publish one validated workflow CSV; duplicate delivery produced one artifact event; the workflow-run web route returned the exact CSV and attachment headers. The public workflow API also accepted a query-to-file run, kept its contract through a definition edit, executed it via the worker, returned status and exact CSV over HTTP, and fenced a stale attempt in isolated Postgres. A connected API admission reached the same Go/OpenShell/GraphJin path and published one validated CSV. | Replace the single-workflow binding with a versioned executor registry, test public HTTP through Go/OpenShell/GraphJin in one check, connect real GraphJin data, check the workflow detail page in a rendered browser, then qualify data-backed records find/get, uploaded-document/browser search and clarification/UI paths |
| M5c mutations | Pack-action proposal is narrowed to host-admitted action kinds and governed effects | Qualify remaining product writes at their actual effect boundaries, including crash and duplicate delivery |
| M5d local work | Opt-in Go file Read/Edit/Write/search uses `os.Root`, read-version checks, create-only writes, read/write exclusion and durable state restoration; staged uploads passed queue/OpenShell isolation; an artifact-only root produced a CSV and one Work artifact event; the Work browser rendered and requested its link, and the live route returned exact bytes | Add process/skill paths with credential isolation, validate larger artifacts and exercise hostile-secret checks |
| M5e delegation | Ax API and ownership design researched | Implement narrowed child execution with shared budgets and cancellation, then connected verification |
| M6 efficiency | Durable pre-call model request limit survives resume; two-call regression passes; Jev budget triage is specified for shadow evaluation | Token/cost accounting, approved routing/fallback, class-calibrated dynamic budgets, context references/compaction and connected multi-route tests |
| M7 parity | Capability inventory and evaluation rules documented | Full Hermes outcome comparison, tenant/load checks and task-quality evidence |
| M8 rollout | Branch and local integration path exist | Staging upgrade/rollback, canary, hosted PR checks and separately budgeted live-provider smoke |

Local tests do not close M5b–M8. The Daily Lead Union case still lacks its
real GraphJin source and a validated real-data workflow artifact path. The
seeded `REF-42` GraphJin path used no live Gemini key.

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
The 2026-09-26 isolated M3 rerun passed `M5_QUEUE_UPLOAD_PASS` and the existing
Hermes, approval and worker-death gates; the accepted upstream idle-cancellation
warning remained visible, while sandbox-delete closure passed. No real key was used.
The subsequent rerun also passed `M5_QUEUE_ARTIFACT_PASS`: OpenShell returned the
exact small CSV, Work emitted its artifact event and another run's file was not
searchable. The isolated live Work HTTP route returned the exact `lead_id\nLEAD-42\n`
bytes as a CSV attachment for the solo owner and rejected an unissued filename
with 404. A fresh isolated run opened the owned Work thread at the documented
`localhost:18121` origin, rendered the `result.csv` link, and requested it through
the browser with HTTP 200. A direct fetch of that same link matched the sandbox
bytes (SHA-256 `5ef30c71075a31302e3aa6f04ef043686bcf04ccf1eae271f487b43d0db3970b`).
The earlier loading screen was caused by opening the Next dev server via
`127.0.0.1`, whose dev resources it rejected as a cross-origin request. This
qualifies the small solo-owner artifact path; multi-user authorization and the
full batch artifact still need connected verification.
The optional M3 web run now repeats the exact-byte/attachment/404 route checks
against each newly created queue artifact and reports `M5_WEB_ARTIFACT_PASS`.
On 2026-09-27 the same isolated suite reported `M5_QUEUE_BATCH_GRAPHJIN_PASS`
and `M5_WEB_BATCH_PASS`: the workflow-run URL returned the pinned script's
`reference\r\nREF-42\r\n` bytes with CSV attachment headers; an unknown run
returned 404. The earlier Work file route correctly denied a workflow-channel
thread. A stale Next dev route cache once returned HTML 404 for every API route;
the test now moves that generated cache aside before starting Next. The
workflow detail page still needs a rendered browser check. No real model key
or customer database was used.
The isolated M3 suite also passed a library search against its actual Postgres
pgvector row and server-side run entitlement, using a deterministic embedding
response. The fixture organization is deleted before queued acceptance so it
cannot be selected as the queue's default org. This is not yet a browser task
against an uploaded customer document.
The isolated records-only turn reached the live records catalog through the
worker, OpenShell, MCP bridge and actor-scoped broker, with no generated apps in
the fixture registry. GraphJin lookup, general GraphJin agent and customer memory
were denied for that run. Find/get passed the real bridge with a synthetic broker;
data-backed records and browser acceptance remain open.
