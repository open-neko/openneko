# Harness stocktake

Reviewed 2026-09-28 against the current Harness worktree and milestone plan.

## Implemented and verified locally

The standalone Go runtime uses pinned Ax and has no dependency on an OpenNeko
checkout. Its optional OpenNeko adapter provides server-side GraphJin lookup and
governed action proposals; Hermes remains the default backend.

| Area | Completed scope | Evidence |
| --- | --- | --- |
| M1 execution | Ax streaming, governed callbacks, tool pairing, cancellation, bounded actor work and run-scoped telemetry | Go race tests, vet and HTTP/Goja compatibility tests |
| M2 transport | Real OpenShell 0.0.116, TLS/CA trust, credential replacement, destination/binary restrictions, rotation/detach, two slots, OAuth refresh, gateway restart and OTLP delivery | [Transport record](../integration/README.md) |
| M3 integration | Browser and queue entrypoints, scoped GraphJin lookup, accepted-input deduplication, result/error projection, cancellation and Hermes regression coverage | [Consumer acceptance](../integration/openneko/README.md) |
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
| M5a catalog | Native/direct/MCP calls share Ax, schema admission, operation journal and catalog binding; pinned OpenNeko read, clarification and card tools pass connected worker/OpenShell turns; the host now binds 12 operations and 24 model calls in the run spec, broker token and checkpoint, while old unbound callers retain four; isolated Postgres admits a fifth journaled operation under the explicit limit and refuses later host dispatch after an unknown outcome; [tool inventory](TOOL-INVENTORY.md) separates admitted and excluded surfaces | Qualify effect/recovery behavior for the remaining product writes on connected runs |
| M5b reads/batch | Seeded memory search, real pgvector/entitlement library search, empty-registry catalog/blueprints, and populated Records catalog/find/get/recycle reads passed real GraphJin, actor-bound broker, MCP, OpenShell and Ax. A queued Work run called six management list catalogs through pinned MCP and the actor-bound broker, received a seeded rule, and recorded no GraphJin or mutation operation. Queued admin/member audit turns proved host-gated disclosure and denial; a cross-org admin run was rejected in Postgres. Three feature-gated source metadata reads passed a queued admin Work turn; a member was denied, and import/config-change routes stayed outside the broker grant. A real Work HTTP upload flowed through queued extraction, deterministic distillation, embedding indexing and an Ax library search of its sourced concept. Queued clarification, operator-answer continuation and a validated card passed connected worker/OpenShell turns; Chromium verified the card exactly once before and after reload. The controlled query-to-file runner and scoped batch-read grant passed local fixtures; a separate no-provider/no-network OpenShell sandbox completed a synthetic CSV with host-owned query handoff and mandatory deletion; response-before-receipt crash recovery passed a local no-redispatch check; run-bound sandbox inventory/deletion refuses foreign labels; the production pg-boss queue used the Go runner, OpenShell, host-only GraphJin broker and seeded `REF-42` to publish one validated workflow CSV; duplicate delivery produced one artifact event; the workflow-run web route returned the exact CSV and attachment headers. The public workflow API also accepted a query-to-file run, kept its contract through a definition edit, executed it via the worker, returned status and exact CSV over HTTP, and fenced a stale attempt in isolated Postgres. A connected public HTTP admission reached the same Go/OpenShell/GraphJin path through the versioned executor registry, returned completed status, and downloaded the exact validated CSV. A queued v1 run executed after v2 became active, while changed registry bytes were rejected by the focused parser test. | Qualify remaining reads and broader channel presentation; real customer GraphJin data remains unconnected |
| M5c mutations | Pack and plugin action proposals are narrowed to the exact run-bound kind, source and policy scope; an OpenShell Records-only turn passed a synthetic internal plugin proposal and one approved effect. A second connected turn proposed `record_update` against a seeded Records registry; worker-owned preflight checked its payload and actor before human approval, then the real GraphJin mutation changed the row. Duplicate execution restored the result and the row had one durable Records audit entry. A production pg-boss Work run proposed a second record update, awaited approval, applied one real GraphJin mutation, and restored its result on duplicate delivery with no new model call or action request. The same isolated queue and real Records GraphJin qualified create, soft-delete and restore: each awaited approval, changed the expected row once, wrote one audit entry, and restored its receipt on duplicate execution. A subsequent connected gate redelivered the queued CRUD runs without new model calls or requests, then withheld each Harness receipt after the real GraphJin mutation; the Records engine receipt and audit reconciled all four writes without redispatch. A further connected queue gate withheld the engine receipt for one GraphJin create after its trigger audit committed; pre-dispatch target context and audit-bound reconciliation repaired the engine receipt and restored the Harness effect without another mutation. A queued Work run used an installed plugin manifest and registry, Harness OpenShell agent, approval, and a separate real OpenShell plugin VM for its synthetic effect. Duplicate delivery created no new request or model call; duplicate execution restored its one effect. Removing or changing the plugin version, integrity, owner or policy after approval was rejected before an effect claim or RPC, and restoring the identical manifest allowed execution. A queued API workflow passed a worker-preflight pack proposal, typed output, duplicate-delivery check and one post-approval effect. The selected backend is bound to the API `work_run` before agent execution. A customer Work run created and edited cron/batch and data-change/watch workflows through journaled direct tools, actor-filtered listing and version preconditions; a seeded watcher fired, and invalid triggers rolled back. A real GraphJin websocket match entered the durable source-change ledger, completed one queued Ax/OpenShell workflow, then arrived again after a GraphJin restart; the replay created no second run or model call. An admin Work run created and edited an approval rule with current-role and version checks. Confirmed workflow deletion passed queue and Chromium verification. A connected Ax/OpenShell Work turn created a host-journaled, current-admin org skill with a supporting file; a second turn staged and read it, while an ungranted token was denied. Another connected turn inspected the whole-tree version, updated the instructions and supporting file, and a later turn read the update; interrupted-swap recovery and stale-version tests pass. Production pg-boss Work runs created, read, updated and re-read the org skill through OpenShell; redelivery did not repeat model calls, host writes or user-visible results. Chromium showed the create and update answers once after reload. | Qualify remaining trigger crash edges and admin writes at their effect boundaries; qualify a real external plugin effect when available |
| M5d local work | Opt-in Go file Read/Edit/Write/search uses `os.Root`, read-version checks, create-only writes, read/write exclusion and durable state restoration; a second-process test now proves shared readers and exclusive writers coordinate on the workspace directory lock. Staged uploads passed queue/OpenShell isolation; read-only staged `SKILL.md` access and a small skill-driven CSV passed a connected worker/OpenShell turn; an artifact-only root produced a CSV and one Work artifact event. A separate no-provider/no-network process sandbox now accepts a model-visible `process_run` through the actor/run-bound Work broker; the host stages selected uploads, journals the effect and publishes declared files only after validation and teardown. A production queue fixture wrote one CSV, emitted one artifact event and one operation receipt; it excluded an unselected sibling upload and a host-only environment canary. A second queued run published a 2 MiB artifact with exact hash and one event; the authenticated web route returned those exact bytes and attachment headers. The same route returned the small CSV and rejected an unissued filename. Chromium found the artifact link exactly once before and after reload. The real OpenShell process suite rejects symlink, nonzero-exit, oversized and cancelled output publication. A queued Work run that wrote a partial CSV then failed produced no artifact event or file and retained one unresolved host operation. A second queued run was stopped through the authenticated Work HTTP route while its process was active; the worker cancelled both OpenShell sandboxes, published no artifact, and refused late redelivery. Another queued run verified the active sandbox was limited to one CPU and 512 MiB, then hit a host-configured exec deadline and failed without an artifact or successful operation receipt. Two more queued runs rejected a 17 MiB declared output without publication and bounded 96 KiB of script output to an 8 KiB model receipt while publishing one valid CSV artifact. A connected queued Work run followed a staged skill and used a selected CSV to produce validated XLSX and DOCX packages in the isolated process sandbox; authenticated HTTP returned exact bytes and Chromium downloaded each file once after reload. | Run the full grouped M5 gate; noncooperating external writers cannot receive a strict atomic edit guarantee |
| M5e delegation | Ax child execution shares parent operations, model budget, usage and cancellation; connected Work, queued/API workflow and agent-job runs exercised narrowed inline children through OpenShell and the host broker; model-only jobs had no GraphJin operation; connected disabled delegation rejected a child call without GraphJin dispatch; a killed child run resumed from its journaled lookup without another GraphJin call | Separately queued children are deferred until a concrete workflow needs them; complete M5a-d dependencies before broad parity claim |
| M6 efficiency | Durable pre-call model request limit survives resume; Ax-normalized model-token receipts and coverage aggregate through resume in Go fixtures; the connected worker/OpenShell fixture recorded 30/30/60 outer tokens with complete coverage and exactly one usage event after queue redelivery; an Ax fixture passes a narrow distiller reference without re-reading or sending an 85 KB observation into its model request; failed/partial tool outcomes fail terminal status, including after checkpoint resume; Jev budget triage is specified for shadow evaluation | General claim-to-receipt terminal verification, enforceable token/cost ceilings, approved routing/fallback, class-calibrated dynamic budgets, context references/compaction and connected multi-route tests |
| M7 parity | Capability inventory and evaluation rules documented | Full Hermes outcome comparison, tenant/load checks and task-quality evidence |
| M8 rollout | Branch and local integration path exist | Staging upgrade/rollback, canary, hosted PR checks and separately budgeted live-provider smoke |

The 2026-09-28 M5c user-administration slice adds a run-bound internal
`user_admin` proposal. A connected Ax/OpenShell/pg-boss run created one
pending invitation, inserted no user before approval, rejected execution
when the requester was disabled, and inserted one user after approval.
Duplicate Work and action delivery reused the request and effect; Chromium
found the answer and terminal approval card once after reload. The
invitation path is qualified. Other user-change variants and the group,
channel, data-source, plugin and source-config mutation families remain open.
The same queue and browser path then qualified `group_admin.create_group`:
an intervening same-name group and disabled requester both stopped execution
before an effect claim; approval followed by worker execution created one
group and duplicate delivery reused its receipt. Other group changes remain
open. A second queued Work turn used management MCP lists to propose
`add_member` for that custom group and the active administrator. The
worker added one local membership after approval; replay reused its effect,
and Chromium found one answer and card across reload.

Cron redelivery has a narrower Postgres-backed qualification: a stale firing
cannot be claimed after the definition changes or is disabled; linking must
affect the claimed firing; a linked failed run settles without a second
dispatch. Source-change delivery now commits the observation, audit and durable
identity together; an isolated Postgres and pg-boss test passes replay dedupe,
stranded dispatch, stale subscription rejection and linked failure recovery.
Connected queued OpenShell turns now pass for cron and source-change triggers:
both record a workflow output and reject a second handler invocation without
another run or model call. A real GraphJin websocket delivered the source
match and replayed the same snapshot after restart; the delivery ledger
dropped that replay without another workflow or model call.

The connected cancellation fixture now stops a queued Work `process_run`
after its child has written a partial file: the worker observes the durable
Stop state, both OpenShell sandboxes are deleted, no artifact or successful
receipt appears, and late delivery cannot reopen the run. The HTTP Stop route
was not invoked in that fixture; it uses the route's durable store transition.
The shared Postgres-backed Work-run suite passed all 11 cases after the stop
fence was added, including the deleted-thread path used by Hermes.

Local tests do not close M5b–M8. The Daily Lead Union case still lacks its
real GraphJin source and a validated real-data workflow artifact path. The
seeded `REF-42` GraphJin path used no live Gemini key.
The 2026-09-27 isolated worker/OpenShell run also passed a queued Harness
clarification: one validated surface and question, no assistant answer, two
outer model calls, and no repeat on queue redelivery. The broker rejects
forged completion events. Card rendering passed the real MCP bridge with a
synthetic event sink; browser card/reload and answer continuation remain open.

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
workflow detail page then rendered the completed public API run and its
Download CSV link in the isolated browser. Clicking the link returned HTTP 200;
a direct fetch matched the exact 19-byte CSV and attachment filename. The
subsequent live worker check also confirmed one API queue attempt and complete
zero-model-usage telemetry. No real model key or customer database was used.
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
