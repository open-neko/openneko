# M4 progress: automatic terminal reconciliation

The optional OpenNeko adapter fences a run before sandbox creation using an
exclusive, fsynced host admission record. The fingerprint binds input, run, tenant,
thread, principal, tool policy, allowed skills, model route, image and environment.
Records live in `runs/.harness-launches/<run-hash>`, outside the uploaded workspace.
Changed input or scope is rejected before inspecting or returning evidence.

PostgreSQL is now authoritative for ownership and receipts. Migration 0085 adds
`harness_run_journal`, scoped by tenant/run. A dedicated PG session holds an
advisory lock for the entire launch or recovery; another worker is refused until
the owner exits. Connection loss aborts the owner. Receipt publication uses that
same connection. An admitted row without a result can only reconcile, never start
another model attempt. Database failure prevents dispatch/publication.

The native `harness-inspect --lock DIR` file lock remains a local guard for existing
filesystem admissions. Legacy fingerprints are validated before inserting a new DB
admission; a rejected import cannot authorize a later retry under changed scope.
All participating hosts share PostgreSQL and the same gateway, not a filesystem.
The accepted prompt is hashed after sandbox path remapping. Hosts must use the
same sandbox workspace layout and unchanged user request/authorization to recover.
Migration 0086 stores the bounded accepted prompt and a scope fingerprint. When a
separate user request is present, recovery restores that prompt instead of accepting
newly generated context. The current principal, policy, request and execution
configuration must still match, and the restored prompt must match the original
full fingerprint. Missing user requests and legacy records retain exact-prompt
matching. Stored context contains application content, never a copy of environment
credentials; it needs the same access and retention controls as result receipts.

Apply migrations 0084–0089 before deploying these workers, and drain older workers first;
mixed versions do not share database ownership. Legacy conflicting records remain
blocked for explicit reconciliation. No Hermes path reads the new table or acquires
these locks. Harness remains opt-in.

On redelivery, a valid database receipt is returned. If the receipt is missing, the
launcher automatically inspects the downloaded Go checkpoint, or invokes the Go
inspector in the retained sandbox when no local checkpoint exists. It supplies the
exact trusted run specification, including remapped paths and user message. The
inspector acquires the Go execution lock, validates bounded checkpoint contents,
and classifies the outcome:

- `terminal`: adopt the saved result through the same mapper as live execution.
- `interrupted`: retained read evidence, but no final answer; only the separately
  admitted bounded continuation described below may execute again.
- `outcome_unknown`: a durable intent lacks a result; do not re-execute.

Busy locks, corrupt/version-mismatched records, conflicting input, unavailable
sandboxes and invalid inspection output fail closed. A corrupt local checkpoint
is not silently replaced. Inspection, receipt repair and terminal adoption never call a model or tool.
Explicit continuation is a new attempt, described below. The terminal database
receipt is committed before sandbox deletion. Receipt replay
also retries deletion, covering a crash between receipt publication and cleanup.
Cleanup failure preserves the receipt and is visible in phase telemetry.

Harness sandboxes carry `openneko.recovery=retain`; generic restart reaping and
name-collision handling cannot destroy unresolved evidence. Explicit cancellation
still tears down the sandbox process boundary and can leave an unresolved admission.
Local cancellation does not prove an upstream operation stopped. Checkpoints and
result receipts contain application content and need host access/retention controls.

## Broker operation records

Migration 0087 adds `harness_operation`, keyed by tenant/run/runtime operation ID
and linked to the admitted run. The Go runtime carries that ID through the callback
context; the fixed adapter posts it to `/v1/harness/lookup`. The broker commits
intent before calling the existing GraphJin control plane and records its bounded
response before delivering it. No admitted run means no dispatch. Duplicate IDs
with changed arguments conflict; unfinished records are unknown and cannot replay.
Finished responses remain available to trusted host recovery, not repeated bearer
requests. Journal/result failure never returns an unpersisted successful response.
Telemetry records run/operation IDs and outcomes without instruction/result content.

Harness broker disconnects now abort GraphJin preflight and lookup requests through
the shared control plane's optional caller signal. The legacy Hermes route keeps
its existing behavior. Cancellation before admission prevents dispatch; cancellation
after admission leaves an unknown operation, and late responses cannot publish a
successful result. Controlled HTTP endpoints verify connection closure in both
phases. A sequential live test also traverses the real GraphJin server: after the
provider fixture observes a model request, disconnecting the broker caller closes
that provider request within the five-second observation window. The operation
stays unknown and redelivery does not increase provider calls. This is separate
from the M2 OpenShell inference-proxy cancellation issue; no remote mutation
rollback or cancellation-status reconciliation is implied.

This records read-only delegation boundaries. It does not authorize mutations or
resume an interrupted Ax VM. A saved GraphJin error response also does not prove
that remote work stopped. When inspection finds unfinished operations, the authorized launcher loads saved
broker receipts and invokes `harness-inspect --reconcile`. Under the same execution
lock, Go checks operation IDs/instructions, rejects conflicting published results,
restores missing tool-result events and atomically saves the checkpoint. Missing
receipts stay unknown. Terminal checkpoints cannot be rewritten. Repaired evidence
is retained and repair alone leaves the run interrupted. The Go continuation API
can explicitly start a new attempt from resolved evidence; the launcher now admits
that path after validating scope, stopped execution and broker receipts. Repair synthesizes no final answer and executes no tool/model.
Remote cancellation-status reconciliation remains open.

## Packaging and inspection

Install the matching native `harness-inspect` on every worker/web host that can
launch Harness, on PATH or via absolute `HARNESS_INSPECT_BIN`. Missing helpers fail
before launch. `adapters/openneko/build-image.sh` also installs the Linux inspector
inside the sandbox. No inspector provider credentials or model egress are needed.

```sh
go build -o bin/harness-inspect ./cmd/harness-inspect
HARNESS_STATE_DIR=/trusted/run/.harness ./bin/harness-inspect < accepted-run.json
```

Default inspection never changes the checkpoint; the explicit trusted-host
`--reconcile` mode repairs only matched operation receipts. Its JSON includes operation content;
only source/outcome classifications and phase timings go to recovery telemetry.
Normal replay and inspection share validation of version, exact input, event
sequence, tool/result pairing, bounded operations and consistent terminal results.

## Verification

`go test -race ./...` covers active execution locks, saved versus unknown evidence,
corrupt checkpoints and rejection by inspection and normal replay. Product tests
cover admission/receipt conflicts, corrupt receipts, terminal adoption, preserved
Hermes cleanup, and actual host SIGKILL while holding the native helper lock. Live PostgreSQL tests
also kill owners before and after receipt publication, reject concurrent ownership,
and recover from a different host directory.
Run the lock test with `HARNESS_INSPECT_BIN` pointing to the built host binary.

The isolated `integration/m3/run.sh` suite runs a real OpenShell sandbox, Go/Ax,
broker, GraphJin and PostgreSQL. It injects a checkpoint-transfer failure after
execution, recovers from the retained sandbox on a different host, then removes the receipt and
recovers from the downloaded checkpoint. Concurrent recovery admits one owner.
Repeated unknown-operation recovery stays blocked. Model request counters must
remain unchanged across every recovery. The production pg-boss run follows this
check. No browser UI changed in this slice; earlier M3 browser qualification is
not a claim of a fresh browser recovery test.

Verified 2026-09-19: Go race tests and vet passed; 99 product tests passed (six
metadata-DB-dependent resolver tests skipped in the local regression command),
worker typechecking passed, and the live recovery assertions passed. The expanded
accepted-context checks also pass: changed dynamic prompts restore the original;
changed requests/principals/policies, missing request identity and tampered stored
context fail closed. The production queue run
`d79859da-7723-4149-8bbe-9cda4226cf5f` completed and was redelivered through
pg-boss after its business context changed. The accepted prompt remained unchanged,
model/GraphJin request counts did not increase and exactly one assistant message
remained. Valid adversarial action/workflow/policy/memory fences created no rows
on either delivery; denial telemetry was observed. Existing Hermes memory-fence
persistence tests also passed against the isolated database. The latest cumulative
run also verifies one durable broker operation/result remains unchanged after
queue redelivery. Two additional real-PostgreSQL crash tests kill the caller after
an HTTP response with and without a saved result; concurrent/repeated dispatch and
changed arguments never call the endpoint twice. Missing admissions, invalid IDs
and loss of the result journal cannot yield an unrecorded success. A further
live recovery check removes the local checkpoint result while keeping the real
broker receipt: conflicting receipt instructions fail, the matching receipt restores
one tool-result event, and request counters do not increase. This injects a missing
checkpoint result; it is not a full worker-death continuation test.

The suite's final nonzero exit remains the known M2 upstream idle-stream
cancellation failure. Terminal reconciliation does not fix that proxy limitation.

The read-only Harness cannot execute the legacy action/workflow/policy/memory
fences in model answers. Those remain available on the Hermes path. Harness
mutations must pass the future operation-journal/approval boundary rather than
piggybacking on text parsing. Rejected fence attempts emit metadata-only telemetry.

## Ax continuation boundary

`TestAgentSnapshotDoesNotResumeForwardCursor` qualifies pinned Ax
`5c43344f9ef3` over real HTTP and Goja. A failed responder occurs after one lookup.
Calling `ExportSessionState` after failed `Forward` panics because the runtime
session is no longer exportable. A separately completed actor-step snapshot does
round-trip through JSON, but restoring it and calling `Forward` starts the
distiller/executor/responder stages again and invokes the lookup callback again.
This test strengthens the earlier globals-only snapshot check; that check never
proved a resumable execution cursor.

The supported harness continuation must therefore be an explicit new attempt:

- Preserve the original accepted input and resolved operation evidence; do not
  treat an exported JavaScript globals map as a program counter.
- Refuse continuation while any operation remains unknown. Reconcile durable
  results before asking a model for more work.
- Seed Ax with the recovered evidence and identify the attempt in the event stream.
  Keep operation identity and total operation/attempt budgets across attempts.
- Admit and persist the new attempt before model calls. Returning the stored
  terminal result remains ordinary replay and needs no model call.
- Approval continuation must use the saved proposal and current authorization;
  it must not recreate an action by rerunning old actor code.

The Go core now implements this contract through `session.Resume` and the explicit
trusted-host `HARNESS_RESUME=1` switch. It starts a new Ax attempt with a declared
`recoveredOperations` input, reuses matching saved lookups, allocates new operation
IDs after the saved prefix, and keeps span/event sequences monotonic. Total limits
are three attempts and four dispatched lookups per accepted run. The `run.resumed`
event is durable before model execution; `tool.reused` references existing evidence
without admitting another operation. Terminal results still replay without a model.

Real HTTP/Goja tests verify evidence reaches Ax, one old lookup is reused, one new
lookup receives ID 2, unknown outcomes are refused, and attempt/lookup budgets do
not reset. The optional OpenNeko launcher now invokes this new attempt path after
validating scope, stopped remote execution, broker receipts and transferred state.
Active and terminal browser reload have passed; approval restart and the full
worker crash matrix remain open.
No old JavaScript stack or function closure is restored.

The inspector now exposes `can_resume` and `next_attempt` from the same Go
validation used by execution. Regression tests cover repaired evidence, unknown
operations, terminal replay, and exhaustion of the three-attempt budget. The host checks for a retained sandbox before
using a nonterminal local checkpoint, and inspects its execution lock when present:
a local file lock alone cannot prove that the remote execution has stopped. OpenShell
0.0.116 `sandbox get -o json` formats failures as diagnostics, not structured JSON;
never treat an arbitrary command error as proof of absence. Its successful
workspace-scoped list response is structured and capped at 1,000 entries by the
server, so an incomplete page cannot establish absence either.

## Qualification scope and limits

The bounded read-only recovery and governed pack-action paths now have real
OpenShell, broker, PostgreSQL, queue, controlled HTTP-effect and browser evidence
(see the final acceptance section below). A surviving active sandbox is refused,
never duplicated. Queue expiry/retry is qualified after its original model turn
finishes; earlier retry is covered at the launcher overlap boundary, not by a
separate full queue timing matrix. This is not an arbitrary Go/Ax VM cursor resume.

Provider reconciliation is opt-in: only adapters with a real status lookup may
recover an external receipt. Other interrupted effects stop as unknown, with no
automatic redispatch. No existing provider is silently treated as idempotent.
Raw OpenShell idle-proxy cancellation remains the M2 failure; whole-sandbox
cancellation passes. Retention is deliberately conservative as documented below.
Transcript pairing is not an exactly-once external-effect guarantee.


## Launcher continuation verification (2026-09-19)

The isolated OpenShell 0.0.116 / real GraphJin suite now repairs a missing lookup
result from the broker journal and continues the same accepted run in a new
sandbox. Its assertions require exactly one `run.resumed`, one `tool.reused`, one
operation record, three additional outer model requests, and no additional
GraphJin request. A subsequent delivery restores the terminal receipt without
model or tool calls. The controlled model checks that recovered evidence reaches
Ax on the first request of the new attempt.

Launcher checks also reject a live remote process despite a stale local copy,
incomplete/invalid sandbox inventory, mismatched downloaded evidence, unknown
broker outcomes, and exhausted attempts. Trusted launch code sets `HARNESS_RESUME`
after environment overrides. The native inspector includes the durable event
sequence so a transferred checkpoint must match the inspected recovery report.

Evidence: `/tmp/harness-launcher-continuation-live.log`; queued run
`8d92f803-2ffa-42a2-8896-e432d1405bae` passed initial execution and redelivery. The
same suite passed Hermes cold/warm and memory-fence regressions. It exited nonzero
only at the separately reported M2 idle upstream cancellation gate. That run used
an injected checkpoint gap. The subsequent `/tmp/harness-recovery-browser-final.log`
run also passed an actual `SIGKILL` of the in-sandbox Go process while its final
model response was pending. There was no host checkpoint; recovery downloaded the
retained sandbox checkpoint, validated it, replaced the stopped sandbox and
completed attempt 2. Assertions prove one saved operation, one reused lookup,
unchanged broker receipts, three new outer model requests and no new GraphJin
request. Killing the entire queue worker remains a separate acceptance gate.


The real process-kill gate exposed an ordering bug in receipt comparison: a
PostgreSQL JSONB receipt can reorder object keys relative to the HTTP result saved
inside Go. Recovery now compares decoded JSON structure, using `json.Number` when
decoding checkpoints and receipts so distinct large integer IDs cannot collapse
through floating-point rounding. The regression verifies reordered nested objects
and rejects distinct IDs above JavaScript's exact-integer range. This guarantee
covers the Go checkpoint boundary; it does not change JavaScript provider parsing.


The fixture now waits for PostgreSQL TCP readiness rather than its temporary
initialization socket and fails if metadata/GraphJin readiness expires. Generated
GraphJin discovery/artifact state lives in a suite-owned volume removed at teardown,
so an unsuccessful startup cannot contaminate the next run through the config
bind mount. The test configuration itself is mounted read-only.


Fresh browser verification on the same isolated stack passed active-page reload
and terminal reload for run `bbece083-d945-4a5a-a4f4-db626648210b`, thread
`89a93409-eeac-43aa-9ba5-dd6e0374afb3`: the rendered answer was `REF-42`; PostgreSQL
held one user message, one assistant message and one completed broker operation.
This does not substitute for worker-process death or approval restart tests.

The delayed browser fixture exposed Goja's default five-second wall-clock step
limit, which also includes host lookup wait time. The configured lookup runtime
now allows 60 seconds per step, within the two-minute attempt limit and above the
broker's 45-second timeout. Tool-free runs keep the SDK default. A six-second
lookup regression and the rebuilt image's delayed browser run both pass. No
separate JavaScript CPU-time accounting is claimed.


## Host launcher death verification (2026-09-19)

The live suite now starts the production sandbox launcher in a separate Node
process and sends it SIGKILL after its GraphJin receipt is saved, while the remote
Go responder is paused. PostgreSQL ownership releases, but Docker inspection proves
the original Go process remains alive. A new host admission refuses recovery while
that remote execution owns its checkpoint lock; model and GraphJin counters do not
increase. After the original process finishes, the host adopts its terminal
checkpoint with no new attempt, model call or lookup. The saved checkpoint has no
`run.resumed` event and the broker still holds exactly one finished operation.

Evidence: `/tmp/harness-host-death.log`, `harness-live.test.ts` (50 seconds), all
eight live launcher/journal/operation checks passed. The broker deliberately stays
alive in this scenario, isolating launcher ownership from broker loss. This is a
real host-launcher death gate, not yet a complete production pg-boss worker death
or approval restart matrix. Test-owned orphan CLI processes are terminated through
the child process group after recovery. No production behavior changed for this gate.

The same cumulative run passed real GraphJin disconnect cancellation, Hermes
cold/warm reuse and memory-fence regression checks, plus production queue
redelivery for run `59d9c440-7965-4a4d-9904-98fdcab77a7e`.

The full command exited 1 only at the existing M2 idle-stream cancellation gate;
sandbox deletion still closed the upstream request. Owned test services were
removed. Separate launcher regression tests passed all 60 cases and worker
TypeScript checking passed. The OpenNeko test change is commit `87a035e` on
`feat/openneko-harness`.


## Queue worker death verification (2026-09-19)

The queue acceptance driver now runs the production `runWorkRun` handler and its
broker in a separate process. It stores the user message through the normal
`createWorkMessage` entry contract, queues one accepted run, and sends SIGKILL to
the worker after GraphJin's result is durable while the remote responder waits.
The job is still active after worker death. pg-boss's own maintenance expires its
60-second lease and moves that same job to retry; no queue row is edited and no
replacement input is enqueued. A new worker consumes the retry after the fixture's
30-second responder pause has elapsed.

The gate verifies the queue job completes with retry count 1, the original accepted
context is unchanged, one user message and one assistant message remain, and the
single lookup receipt is unchanged. The recovered answer contains `REF-42` and
neither outer model nor GraphJin request counts increase. Recovery telemetry shows
terminal inspection of the retained sandbox, receipt reconciliation and cleanup.
This covers worker and broker loss after a saved read result, followed by terminal
adoption. It does not yet cover worker death before dispatch, unknown external
effects, restart during approval, or retry arriving before remote completion.

Evidence: `/tmp/harness-worker-death-final.log`, run
`09dddd28-d319-4741-a0a9-10a774039751`, pg-boss job
`cc0be4dd-ff63-4730-9189-24ee5d7eb50a`. The same run passes prior launcher/journal/
operation crash checks, direct GraphJin disconnect cancellation, real Hermes
cold/warm and memory-fence checks, and completed-run queue redelivery. The initial
fixture attempt omitted the user-message entry step; its expected message-count
assertion failed even though recovery succeeded. The corrected full rerun passes
that assertion as well as queue completion.

The full suite still exits 1 at the known M2 idle upstream cancellation failure;
sandbox deletion closes that request. Owned services were removed. The queue
fixture change is OpenNeko commit `b59ebee` on `feat/openneko-harness`.


## Approval boundary prerequisites

Source review of OpenNeko's current `action-store.ts`, `action-executor.ts`, broker
and action queue handler identifies the contracts Harness must add before exposing
mutations. Reuse the existing action-request rows, approval UI and event transport,
but do not expose their legacy broker routes directly to Harness:

- `createActionRequest` accepts a supplied status and optional actor fields. A
  Harness proposal endpoint must derive actor and policy decisions from the bound
  run, persist an operation-to-request identity, and reject changed proposals on
  repeated delivery. The model cannot select `approved` or an approver.
- `updateActionRequestPayload` currently permits updates regardless of status;
  preflight adapters use it to prepare execution. Freeze the approved operation
  after preparation, and compare its immutable arguments and relevant resource
  revisions on continuation. Recheck current caller, policy and connection access
  before dispatch; an old approval cannot authorize a changed operation.
- `executeApprovedActionRequest` reads `approved`, writes an execution row, and
  invokes the adapter without an atomic cross-worker claim. Harness effects need
  a durable exclusive admission before dispatch. A prior unresolved dispatch
  cannot be retried merely because its worker died. Use provider idempotency/status
  where available; otherwise preserve an unknown outcome.
- Approval waiting must return a durable continuation and release sandbox/model
  resources. Resume from the saved request and result, not rerun the actor code
  that produced the proposal. Existing action-result events can supply the product
  presentation, but receipt publication must precede continuation.

The current launcher now mints an immutable `harness-read-only` broker binding.
That profile allows only `/v1/harness/lookup`, which has its own operation journal.
All legacy routes, including action requests/enqueue, arbitrary GraphJin tools and
broker-posted events, are rejected with HTTP 403 before dispatch. Thus the existing
answer-fence restriction is backed by the host capability boundary. Tokens cannot
change profile or identity on reuse, and mutation of the caller's binding object
cannot widen a minted token. Denials emit metadata-only
`harness.broker_capability` observations. Hermes retains its existing route access.
This closes a prerequisite gap; it does not enable or complete approval handling.

Verification: `/tmp/harness-broker-profile-live.log` records six denied legacy
routes, no created action request, and successful Go/Ax lookup and recovery with
the restricted token. It also passes Hermes cold/warm and memory-fence regressions,
completed queue redelivery, and worker-death recovery for run
`6d002a6a-42af-4be6-98b7-65b0c01937df`. All 66 focused broker/launcher tests and worker
typechecking passed; the final audit-order adjustment also passed the six broker
tests. The cumulative suite exits 1 only for the existing M2 idle-stream defect;
sandbox deletion closes the upstream and owned services were removed. Product
commit: `45001af` on `feat/openneko-harness`.


## Durable proposal storage (host layer)

OpenNeko migration 0088 extends its existing `action_request` records with a
Harness runtime operation ID, the original proposal and the prepared approval
snapshot. A unique tenant/run/operation index admits one preparation. The store
requires an admitted Harness run, derives the actor from that run, and binds the
proposal to its accepted fingerprint. Admission locks the run and journal rows
while inserting so a concurrent terminal receipt or identity change cannot admit
a stale proposal. No lock is held while preflight hooks execute.

A new proposal starts as `draft`, regardless of a supplied status. Existing
preflight hooks prepare its arguments. Publication atomically checks that those
arguments still match the prepared record, freezes them and changes the request
to `pending_approval`. Repeated identical proposals return the saved request and
decision without rerunning preparation; changed proposals conflict. Interrupted
or failed preparation remains unresolved and never automatically replays.
Payload updates are refused after freezing. Only an explicit, currently authorized
approver identity can decide a Harness proposal; approval/rejection uses a status
compare-and-set so racing decisions cannot both succeed. A preflight cannot
silently auto-approve this initial Harness path. Hermes retains its existing
preparation and approval behavior and legacy record output shape.

This is storage infrastructure, not an enabled action capability. The existing
broker profile still denies proposal/mutation routes, and the legacy executor
explicitly rejects Harness actions. Remaining wiring must include the neutral
Go/Ax proposal tool, a shared operation budget with lookups, trusted descriptor and
policy validation, waiting/continuation events, approval reload/restart acceptance,
fresh execution authorization, and the effect claim/idempotency/reconciliation
path. In particular, a saved approval is not yet permission for the existing
executor to dispatch an effect.

Verified with actual PostgreSQL and a SIGKILL during preflight: one request remains,
repeat admission stays unknown, no preparation reruns, and incomplete proposals
cannot be approved. Tests also cover a post-preflight payload race, changed input
and accepted fingerprint, wrong tenant, forged actor/status fields, approval versus
rejection concurrency, and blocked payload changes after a decision. Final focused
checks in `/tmp/harness-proposal-focused-final.log` pass the new proposal gate and
all seven existing action-flow tests. Four executor unit tests and worker typechecking
pass. The exact final migration applies to a fresh isolated PostgreSQL database;
the unique index and null-safe backend constraint were inspected. Both migration
copies match. Proposal lifecycle telemetry contains IDs and classifications, not
proposal content.

The cumulative `/tmp/harness-proposal-live.log` also passed existing OpenShell,
GraphJin, Hermes, launcher/queue recovery and worker-death gates (worker-death run
`a5580dee-7cbc-46c6-8940-814be6b50c9c`). It exits 1 at the known M2 upstream idle
cancellation failure; sandbox deletion still closes that request. All owned test
services were removed. This does not qualify full M4 approvals or governed effects.

## Typed Go proposal recovery (2026-09-19)

The standalone engine now accepts an optional host-installed `Propose` capability.
Lookup and proposal calls share four operation IDs across all attempts. Proposal
inputs exclude identity and approval state; receipts accept only pending approval
or denial. Checkpoints bind each saved result to its tool type and original input.
Continuation reuses a saved proposal receipt without invoking its writer again.
A completed model turn with a pending proposal reports kind `approval`; this is
not completion of the proposed effect. The command and OpenNeko broker wiring
remain separate work. Existing lookup-only checkpoints remain compatible.

`go test -race ./...` and `go vet ./...` pass. HTTP/Ax tests cover combined
lookup/proposal recovery, terminal replay, cross-tool receipt rejection, rejection
of executed receipts and the shared operation ceiling. The cumulative live
OpenShell/GraphJin suite passed worker-death recovery and queue redelivery with
this binary, and still exited nonzero on the known M2 upstream cancellation test.
Owned fixture services were removed.

## Governed proposal and effect wiring

The Go CLI/optional OpenNeko adapter now sends typed proposals through the broker.
Lookup and proposal requests use one operation-ID namespace and journal. Only
installed, ready pack actions with a validated input schema and current entitlement
can reach worker preflight. Original descriptor and arguments are bound to approval;
actor, approver, policy and descriptor are rechecked before execution.

Migration 0089 adds one partial unique index to the existing action execution table.
Harness effects have a session owner lock plus a durable claim; Hermes execution
is unchanged. Known results are persisted atomically with terminal action status.
An abandoned claim either recovers through an adapter's read-only provider-status
callback or becomes explicitly unknown. It never authorizes another dispatch.
No shipped adapter gains inferred idempotency support: the capability must be
implemented for the actual provider. The controlled HTTP fixture qualifies both
reconcilable and non-reconcilable service contracts.

Cancellation deletes the sandbox as the process-tree boundary. Cleanup failure
now raises an explicit reconciliation error and `harness.cancellation` telemetry;
a local Go cancellation alone is not evidence that the OpenShell proxy stopped
upstream work. The separate M2 idle-proxy regression remains enforced.

Retention policy: unfinished approvals, unknown operations and unreconciled
sandboxes must not be TTL-deleted or automatically replayed. Retain their bounded
checkpoints and host receipts until an operator records a resolution. Terminal
sandbox deletion occurs only after a durable receipt. Host checkpoints and database
receipts must be retained together for the deployment's accepted-input deduplication
window; deleting one is not permission to rerun an accepted input. No background
purge is enabled by this change. Existing organization deletion remains explicit.

## Final governed-action acceptance (2026-09-19)

- A follow-up real filesystem failure check (2026-09-20) replaces the checkpoint
  directory with a file immediately before proposal dispatch and immediately after
  its callback. The first case executes no callback; the second executes once.
  Both reject resume after storage is restored, preventing unreceipted replay.
  `go test -race -count=1 -timeout 60s ./internal/session -run TestProposalCheckpointIOFailureStopsDispatchAndReplay`
  passes both cases.
- Standalone Go race suite and vet pass. Worker typechecking and the focused
  broker/executor/launch regression suite pass (17 tests, one opt-in test skipped).
- The cumulative first live group passes 20 checks, including four effect crash
  cases: before dispatch, after commit without status support, after commit with
  provider-status reconciliation, and after local receipt persistence. Actor,
  schema/descriptor and policy changes deny new execution. A revoked policy during
  recovery still records the abandoned effect as unknown.
- The Go/Ax proposal runs inside OpenShell using credential replacement, reaches
  the scoped broker, produces one durable approval and restores it without another
  model call. Cancellation through the production launcher deletes the sandbox
  and the controlled upstream observes closure.
- Actual queue/worker restart retains the approval. The final replay also asserts
  exactly one persisted assistant message event, fixing the duplicate answer found
  during browser acceptance. `/tmp/harness-projection-queue-final.log` records
  `M4_QUEUE_APPROVAL_RESTART_PASS` for run
  `35658da3-793b-4998-bcf1-1e5c7129e3b6`.
- In the existing web UI, thread `71fcb5aa-322c-4beb-abc9-5b7a65f0ddff`
  restored its approval after reload; clicking Approve traversed the real action
  queue and production handler to one controlled HTTP effect. The card showed Done
  after another reload. Request `ec7e518a-3db5-4503-bcac-328eb1d50ef7` had one
  execution row and executed status.
- A fresh replayed thread `80b47892-5fd9-4ae5-8c03-8833443f093b` rendered one
  answer and one approval. Its controlled HTTP effect then lost its receipt;
  the UI displayed `Effect outcome unknown; automatic redispatch disabled`, including
  after reload. Request `b25f2564-5402-4712-8b8c-35717e5cbb2c` had one execution
  row, failed/unknown status and one assistant event. No UI styling changes were
  needed. Synthetic threads seeded after web startup explicitly use the existing
  solo owner, preserving normal thread access checks.

The optional browser effect worker is reproducible with
`apps/worker/scripts/harness-m3.ts --approval-worker-only` in the isolated M3
environment; `--effect-unknown` injects receipt loss after the controlled HTTP call.
It is guarded by `HARNESS_M3_LIVE=1` and port 18119. Stop it before releasing the
browser phase's `web-done` marker. All credentials and business data in these
checks are synthetic; these are correctness gates, not paid model-quality results.

Final browser-stack cleanup removed all owned M2/M3 services and volumes. The
cumulative command still exits 1 at `upstream_stream_cancelled=false`, followed by
`sandbox_delete_closes_upstream=true`. That remaining M2 dependency defect is
reported, not waived or hidden by the successful Harness cancellation gate.

## Acceptance decision (2026-09-20)

The user accepted delayed upstream idle-stream cancellation as nonblocking, with
possible additional token consumption. Earlier nonzero suite exits above remain
historical evidence. The current runner reports the defect as a warning, retains
the mandatory sandbox-teardown check, and permits M2/M4 acceptance. Keep upstream
OpenShell unchanged and qualify its eventual fix during an upgrade.
