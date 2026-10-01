# Go harness delivery plan

Revised 2026-09-26 after inspecting OpenNeko's Hermes prompt assembly, capability
registration and MCP bridge. This supersedes the GraphJin-centric future roadmap;
M1–M4 numbering and their historical acceptance evidence remain unchanged.

**Target:** an independent Go/Ax agent runtime that can use native tools, MCP tools
and direct service adapters through one governed execution boundary. OpenNeko
supplies its product capabilities, identity and policy. GraphJin's server-side
agent is one tool, not the organizing principle of the harness.

M1–M4 have local acceptance at their implemented scope. M2 accepts delayed
upstream cancellation as a nonblocking limitation; mandatory sandbox cleanup
remains tested. This is not Hermes capability parity or production qualification.
M5a has a local Go/Ax + official MCP SDK fixture acceptance. M5c has a narrowed
pack-action proposal slice, with the other mutation families still open. A
separate OpenNeko v3.5.6 demo instance was started and verified locally on the
Mac, then stopped to respect the host's 4 GB Docker memory limit; its volumes
remain. Onboarding was skipped, so no admin password or model key was configured.
The released worker still uses Hermes and OpenShell 0.0.54.
The first M5b memory and library search slices passed through the feature worker,
Harness image, OpenShell 0.0.116, real MCP bridge, scoped broker, Ax and checkpoint.
The records-only catalog turn passed the same path with GraphJin lookup and customer
memory denied; find/get are qualified against the real bridge with a synthetic broker.
The memory result and library database row were seeded. A queued clarification
now passes the real bridge, broker, Ax and worker path: the question is persisted
once, the model stops, and redelivery adds no question or assistant answer.
Validated card rendering passes the bridge and broker with a synthetic sink;
broader MCP reads and writes remain open. Full M5c remains open. Demo health alone does
not qualify either milestone.
Hermes remains the default until rollout.

The isolated v3.5.6 M3/M4 consumer suite passed serially with the demo stopped:
20 product tests, the approval, worker-death and approval-restart queue checks,
and the pinned OpenShell 0.0.116 transport suite. The accepted upstream idle
cancellation warning remains; sandbox teardown passed. The suite left no test
containers or networks running under Docker's 4.87 GiB limit.

## Discovery that changes the plan

OpenNeko does not supply one fixed Hermes prompt. `buildWorkPrompt` assembles
sections from run capabilities, data surface, channel, installed skills, operator
context and conversation. Tool schemas/descriptions supply additional instructions.
`agent-core.ts` selects logical MCP servers; the Hermes adapter mounts a trusted
multiplexed stdio bridge. Workflow/agent-job modes have additional or narrower
surfaces. Native file/terminal/delegation capabilities are separate from MCP.

The current Harness backend still advertises `mcpTools: false` because only a
narrow search-only memory server is admitted through OpenNeko's bridge. The
product prompt defers tool contracts to the run's admitted Go catalog and lists
only held pack action candidates. It does not claim that all Hermes MCP
capabilities are present.

Sources in the isolated OpenNeko integration checkout:

- `packages/llm/src/work/prompt.ts` and `packages/llm/src/prompts/sections.ts`
- `packages/llm/src/work/agent-core.ts` and `work/run-chat-turn.ts`
- `packages/llm/src/agent-backends/hermes.ts` and `agent-backends/harness.ts`
- `apps/worker/src/agent-sandbox/mcp-bridge.ts` and `entry.ts`

## Capability coverage to deliver

This is an acceptance inventory, not a promise that every tool is mounted for every
user. M5a records exact tool names, schemas, eligibility and backing routes from the
running catalog. Include enabled native Hermes toolsets (such as web research when configured),
not only named MCP servers. Freeze that inventory for the parity test; review catalog changes
explicitly rather than automatically granting newly discovered capabilities.

| Family | Existing OpenNeko surface | Planned delivery / proof |
| --- | --- | --- |
| Business data | GraphJin server-agent delegation; catalog/filter validation needed by workflow tools | Retain delegated lookups; M5b adds only required supporting tools without a raw-query bypass for denied lookups |
| App records | `neko_records`, governed records actions | M5b scoped reads; M5c writes, exact record identity and current actor grants |
| Knowledge | `neko_memory`, `neko_library`, installed skill files, `neko_skills` | M5b search; M5c saves/creation; M5d file-backed skill use and uploaded documents |
| Conversation and UI | `neko_interaction`, `neko_ui`, closing metadata and channel-specific rendering | M5b clarification/resume, validated cards, reload, channel fallback and no duplicate output |
| Automation | `neko_workflow_builder`, `neko_rule_builder`, `neko_action`, `neko_workflow_output` | M5c creation/update/delete and workflow-only calls with trusted workflow identity |
| Integration actions | `neko_plugin_actions`, `neko_pack_actions` | M5c reuse existing descriptors, preflight, approval and execution contracts |
| Administration | Plugin, user, channel, data-source and source-config managers; `neko_audit` | M5b authorized reads; M5c governed changes, installer approvals and role revocation |
| Local work | File/terminal tools, skill instructions, uploads, run artifacts | M5d isolation, file freshness, process termination and browser downloads |
| Delegation | Hermes `delegate_task` when enabled | M5e equivalent bounded child work; no Hermes-specific API in core |

No automatic parity claim extends to arbitrary future MCP servers, native Hermes
extensions or provider-specific recovery. Explicitly list unsupported tools and
protocol features. Preserve equivalent user outcomes, not Hermes' internal tool
implementation or its model name prefixes inside the core.

## Verification rules for every milestone

- Keep the standalone Go suite independent of OpenNeko. Test adapters separately.
- Progressively extend the existing isolated OpenShell + worker + broker + database
  + queue + web deployment. Deterministic model endpoints still traverse real Ax
  and OpenShell. Use the real MCP bridge/server and GraphJin where relevant.
- Verify browser/API output, durable records and actual execution together. Every
  scenario records run/attempt/operation IDs and a versioned dependency manifest.
- Exercise both browser in-process execution and production queue/channel paths;
  they are distinct. Preserve records-only, customer and workflow/agent-job scopes.
- Include denial, malformed arguments, cancellation, timeout, restart, duplicate
  delivery and changed authorization. A discovered tool is not authorization.
- Correlate model, tool, broker and remote spans. Report missing usage, telemetry
  loss and unknown effects explicitly. Default traces exclude customer content.
- Keep Hermes regression checks throughout. No active user stack is restarted.
- Live-provider quality checks are separate from deterministic acceptance and use
  an explicit budget. Run hosted CI/PR checks before merge, not as claimed local evidence.

## M1–M4 — Retained foundation

| Milestone | Completed local scope | Authoritative evidence |
| --- | --- | --- |
| M1: Ax execution | HTTP/Goja execution, callbacks, pairing, cancellation and snapshot boundary | `compat/`, Go race suite, [design findings](DESIGN.md#3-axagent-findings-and-implications) |
| M2: OpenShell | Pinned 0.0.116 transport, credential lifecycle, policy enforcement, gateway restart, OTLP and teardown | [Live transport record](../integration/README.md) |
| M3: First consumer slice | Browser/queue → Harness → broker → GraphJin; scoped evidence, accepted-input identity and output | [Consumer acceptance](../integration/openneko/README.md) |
| M4: Durable governed operations | Receipt recovery, ownership, approval continuity, claimed effects, crash reconciliation and honest unknown outcomes | [Recovery and crash matrix](M4-RECOVERY.md) |

M4 covers the implemented lookup/proposal/effect paths, not every tool in the
inventory. New tools inherit its rules and need their own acceptance evidence.
Durable receipts do not imply exactly-once external effects. Ax resume remains a
bounded new attempt from evidence, not restoration of a JavaScript execution stack.

## M5a — Shared capability catalog and invocation boundary

**Partial status (2026-09-27):** the Go catalog now validates names, JSON schemas,
origin/effect metadata, results and collisions; dispatches native, direct and local
MCP tools through the same Ax callback and durable operation journal; pins a
catalog hash on new checkpoints; retains old lookup/proposal checkpoint decoding;
and emits tool origin, effect and duration. The official Go MCP SDK is pinned at
v1.8.0 with Go 1.25. An Ax run exercised native + MCP fixture calls and terminal
replay. Pinned OpenNeko read, clarification and card tools now use the same
catalog and journal in the connected worker path. The trusted launcher now pins
12 operations and 24 model calls in the run spec, broker token and checkpoint;
the host journal accepts a fifth operation when explicitly bound, while legacy
callers retain four. Postgres stores the admitted limit for each run and allows
at most one unfinished host operation, so an unknown effect prevents later
dispatch. Its hard ceiling of 32 matches Go.
Durable MCP product writes remain excluded until their effect boundaries are
qualified. The [consumer tool inventory](TOOL-INVENTORY.md) records admitted
and excluded surfaces; a recovery matrix for each remaining write is open.

**Deliver:** a run-scoped catalog and dispatch path for native Go, MCP and direct
service tools. Reuse the existing operation journal, Ax callback bridge, policy and
result validation. Each admitted entry binds a stable identity, schema/version,
trusted origin, eligibility, limits, effect classification and recovery policy.
MCP annotations and descriptions are untrusted hints, not permission decisions.
Unknown tools are not presumed read-only, concurrent-safe or retryable.
The admitted catalog is sorted by tool name before binding to Ax, so the same
run scope and capability set produce deterministic tool order.

The OpenNeko adapter derives prompt sections and tool bindings from the same
admitted catalog. Retain consumer-neutral core types and static adapters; no
plugin-loading framework. Preserve old lookup/proposal checkpoint decoding and
explicitly version the wider operation contract. Replace the initial four-call
ceiling only with explicit trusted run-wide budgets, never unlimited execution.

**Verify:** the same real Ax run invokes one native callback and one local MCP
fixture tool with
consistent validation, operation IDs, outcomes and telemetry. Fixture checks cover
name collisions, schema changes, unauthorized discovery, forged scope, unknown
calls, oversized results, multiple calls per actor step and old checkpoint replay.
Compare prompt claims to admitted tools for customer, records and workflow modes.

**Exit:** no dispatch path bypasses the journal/governance boundary; no prompt
advertises unavailable capabilities. Existing M1–M4 tests still pass.

**Dependency:** M4. This is the next implementation milestone.

## M5b — File-backed batch path and MCP read/interaction slice

**Partial status (2026-09-27):** customer-surface memory and library search, actor-filtered workflow list, plus
actor-scoped records catalog/find/get, blueprint browse, and recycle-bin reads are admitted through the actual OpenNeko
stdio bridge with pinned schemas. The broker binds org/run identity; records-only
Harness turns cannot use GraphJin lookup or customer memory. Isolated OpenShell +
worker + broker + Ax catalog and search turns each recorded one finished Go
operation and returned the answer. The library
run now uses the real entitlement lookup and pgvector search against an isolated
Postgres row, with a deterministic embedding fixture. Hermes regression checks
passed. Records catalog and shipped blueprint browse were tested end to end in a
records-only OpenShell turn. A second isolated turn seeded an active app, row and
recycle entry, then read all five through real Records Postgres, GraphJin,
actor-bound broker, MCP bridge, OpenShell and Ax. The queued
customer Work fixture also listed one entitled workflow through the same path;
the broker denied an ungranted token and filtered an unknown run to no workflows.
Workflow definitions are read-only here; execution remains a separately admitted
workflow run.
Six additional read-only management catalogs (plugins, users, groups, channels,
data sources and action rules) now pass the pinned MCP bridge. A queued Work
turn called all six through the actor-bound broker, received a seeded rule, and
recorded no GraphJin or mutation operation. Adjacent request/save/delete routes
remain denied. The separate audit-trail read passed queued admin and member
Work turns: the admin received a seeded action request; the member received
only the denial. An admin run from another org is rejected by the host gate.
Three source-configuration metadata reads now pass a separately feature-gated,
admin-only queued Work turn: source graph description, source secret names (never
values), and imported OpenAPI asset metadata. The model received seeded secret
and asset names; the member actor was denied by the host. Import, config-agent,
preview and proposal routes remain excluded. Other management reads still need
actor-specific qualification.
The queued
clarification reached `needs_input` after two outer model
calls; broker events entered the turn reducer, and queue redelivery preserved one
question and surface without a model replay or assistant answer. A second queued
turn accepted the operator's answer and completed the same thread. Broker validation
denied forged completion events, and a connected worker/OpenShell turn rendered
one validated card through the real stdio bridge. Chromium opened the completed
Work thread and found the same card exactly once before and after reload.
The uploaded-document path now passes a connected Work HTTP upload, queued
Markdown extraction, deterministic librarian distillation, durable embedding
dispatch, pgvector indexing and a separate queued Ax library search. The
returned concept keeps the uploaded document as its source. Broader channel
presentation and the real Daily Lead Union batch remain unqualified.

The controlled query-to-file runner now consumes the pinned script's cache-miss
requests, calls a trusted read callback, writes bounded response files and query
receipts, reruns the script without model turns per query, and publishes only a
CSV whose header and row count match the compact summary. The OpenNeko broker
has a separately bound `batchRead` grant for its existing read-only GraphJin
query route; a standalone adapter CLI can use it. A pinned whole-bundle snapshot
runs in a separate OpenShell 0.0.116 sandbox with no provider, network grant or
broker token. An isolated live fixture verifies a cache miss, host-owned query,
response upload, validated CSV, HTTP 403 on model egress, and sandbox deletion.
Artifact publication is blocked if sandbox deletion fails.
This executor is distinct from OpenNeko's existing compiled API batch, which
projects admitted input records to CSV without querying GraphJin. Both belong
to workflow definitions and produce `workflow_run` results; neither is a skill
execution mode.
Query responses now persist before their receipts; if a host dies in that gap,
resume validates the saved response and repairs its receipt without dispatching
the read again. A receipt without response still fails closed.
With a trusted run ID, the runner also names and labels its OpenShell sandbox
deterministically; a retry can remove an exact-match orphan but refuses to
delete a mismatched owner. The host must hold database ownership before using
this retry path. The opt-in worker queue binds a `workflow_run` and locks
the database owner before execution. An isolated production-queue run now uses
the Go runner and OpenShell, obtains the seeded `REF-42` row through the host-only
GraphJin broker, and publishes a validated CSV with one query. The duplicate
delivery fixture proves one artifact event. The workflow contract supplies the
CSV columns and artifact filename; neither is fixed to Daily Lead Union.
A real-data Daily Lead Union run remains open. The isolated web route returned
the exact 19-byte CSV through the authorized workflow-run artifact URL with
attachment headers and rejected an unknown run with 404. The workflow detail
page also rendered a completed API run and its CSV link in the isolated browser;
the clicked link returned HTTP 200 and the exact 19-byte CSV. This is **not yet** an Ax tool: do
not mint that grant in normal runs until general workflow admission and batch
recovery are fully qualified.
The 20-minute pipeline should run as a host-owned operation with progress and
durable continuation, rather than blocking the current two-minute Ax callback.
Reuse OpenNeko's pg-boss queue, workflow-run and Work artifact contracts;
bind a dedicated workflow run and its actor before minting `batchRead`. The worker
restart reconciler must preserve a batch run with a retryable queue owner, while
the host verifies sandbox teardown and CSV bytes before publishing the event.
Batch contracts belong to workflow definitions, never skill entitlements. A
workflow may use zero or more skills. Skill files and scripts must not call
LLMs directly. Harness/Ax alone owns model routing, budgets and telemetry;
scripts exchange governed data requests and files.
Web and worker now share a trusted versioned executor registry. Admission
snapshots the active revision and configuration fingerprint with the selected
contract; the worker can execute that pinned revision after a later active
switch, but refuses a changed or removed entry. The caller submits immutable
input and receives `202 Accepted`
with a run ID; it cannot edit or approve the run while queued. Any different
input requires a new admission and idempotency key. A later operator edit to
the workflow's name or output contract affects only later admissions;
disabling the workflow may revoke a queued run. The caller polls for completed,
failed, or cancelled status and downloads an artifact only after successful
validation and publication.
The dispatch outbox, pg-boss expiry for long executions, and cancellation
remain part of that admission path.

The isolated API acceptance now covers `POST` through the real web server,
transactional admission, a worker claim, file-backed execution with no provider
key in the child, `GET` status and exact CSV download. A definition edit after
admission did not change the accepted artifact. A stale query-to-file lease
returns to the queue with a new attempt, while an old attempt cannot claim or
finalize it. The public HTTP worker test uses a deterministic local executor.
A connected public HTTP admission through the production queue, Go, OpenShell
and a seeded GraphJin broker also passed, including status polling and exact
CSV download. The isolated worker also ran a queued v1 revision after v2 became
active, and a focused Postgres admission test retained the v1 binding through
definition edits, replay and stale-attempt fencing. Real data remains open.

The remaining Harness MCP routes are denied by the broker profiles. Qualify
each route with bound actor/run identity and the existing operation journal
before admission. A working stdio connection alone does not authorize tools.

The Go MCP SDK now has an opt-in integration check against the real OpenNeko
multiplexed stdio bridge (`OPENNEKO_TEST_SOURCE=... go test ./integration/batch`).
With a synthetic broker and read-only memory, library and records servers, it
verifies discovery, scoped calls, and child-process exit on close. The separate
isolated OpenShell check qualifies memory/library search and records catalog,
not general MCP.
The generic adapter now keeps bounded MCP resource links alongside text,
structured content and the `is_error` status. An in-memory SDK server with
one-tool pages verified paginated discovery, typed tool errors, rejection of
unsupported image content and oversized results, and a call deadline. A
second real OpenNeko stdio-bridge test killed the child after admission and
verified that its next call failed. A stalled synthetic broker read made the
Go call hit its deadline; closing the bridge terminated its child and closed
the broker request. A nonterminal checkpoint rejected an MCP version change
before any new model or tool call, while the unchanged catalog resumed its
saved read receipt. These checks qualify the exercised read path; they do not
imply that every logical MCP server has end-to-end cancellation propagation.

**Deliver:** connect the Go runtime to OpenNeko's existing logical servers through
its trusted bridge. Reuse protocol support available in the pinned Ax Go stack or
a maintained Go MCP implementation; do not invent JSON-RPC framing. Qualify the
actual bridge's stdio lifecycle first; support remote MCP transport only when an
installed capability needs it. Launch commands/endpoints come from trusted host
configuration, never model arguments.

Support discovery (including pagination), schema mapping, bounded structured/text
results, resource links where supplied, tool errors, deadlines, progress and
cancellation for the exercised servers. Explicitly reject unsupported
content/protocol features. Keep raw results
and typed status available to recovery; do not flatten everything into prose.
The first integrated path is a governed batch run: an admitted, versioned script
requests authorized GraphJin reads through the trusted host and receives results
as scoped files, without a model turn per query. The host records each query,
identity, policy decision and file receipt. The sandbox gets no GraphJin credential
or unrestricted broker token. Row data and command stdout stay out of model
context; the agent receives bounded counts, errors and file handles. Validate
the final CSV against its declared schema before publishing it. This controlled
runner precedes general model-generated shell access in M5d.
Expose authorized record, memory, library and administrative reads plus clarification
and rendering. UI effects need deduplication even when they do not change business
records. Persist clarification as a waiting continuation, not an open model call.

**Verify:** first port the 2026-09-15 Daily Lead Union case to the controlled
batch path. Drive its real script and GraphJin against seeded data; inspect output
CSV bytes, counts and schema, broker authorization, durable query receipts, model
turns, tool calls, tokens, time and artifact visibility. Inject a denied query,
missing source, partial file, process crash, cancellation and duplicate delivery.
No query miss may require the model to copy results into a cache file. Then real
browser tasks search the document library, read authorized app
records, ask a clarification and continue, and render a validated card. Reload and
worker restart preserve one question/answer/card. Exercise org/user isolation,
records-only restrictions, channel rendering fallback, bridge death, tool error,
malformed result, stalled server, cancellation and changed tool catalog on resume.
GraphJin investigation still uses its server agent; supporting catalog/filter
calls do not create an alternative route around a refusal.

**Exit:** the batch task produces one validated downloadable CSV with bounded
model context and no per-query model mediation. A non-GraphJin business task also
works end to end through real MCP. Every
inventory read/interaction entry has an acceptance case or explicit unsupported
status; no hidden mock adapter. Real Hermes cold/warm/reuse checks remain green.

**Dependency:** M5a.

## M5c — Governed product mutations and workflow execution

**Partial status (2026-09-28):** the existing M4 proposal/effect path now binds
eligible pack and plugin action kinds, source, scope and installed-plugin name
to the exact Work run. With no eligible action, there is no proposal tool or
proposal broker route. The Go callback rejects an unlisted action
before broker dispatch; the broker rechecks pack readiness/schema or plugin
entitlement and current policy. Approved effects use the existing one-claim,
reconciliation-or-unknown boundary. A connected OpenShell run passed both a
pack action and a synthetic internal-scope plugin action on a Records-only turn,
each with pending human approval and one approved effect. Product-level Records
and installed-plugin fixtures now cover the narrower paths below.
An additional populated Records fixture passed a real approved `record_update`
through its worker adapter and GraphJin, then restored the execution receipt
on a duplicate call; the row and audit log each reflected one mutation.
Worker preflight now checks Harness CRUD payload shape and actor availability
before the approval is issued; legacy Hermes requests retain their existing path.
A production pg-boss Work run now proposes `record_update` for a second seeded
row, awaits approval, applies the real GraphJin effect once, and restores the
completed result on duplicate delivery without another model call or action
request. The same isolated production queue and real Records GraphJin then
qualified `record_create`, `record_delete` and `record_restore`: each waited
for approval, changed the expected row state once, wrote one audit entry, and
restored the effect receipt on duplicate execution. A later connected gate
replayed each queued CRUD run before approval without a second model call or
action request. It then deliberately lost the Harness receipt after each
real GraphJin effect committed. The Harness marked the effect unknown, then
read the Records engine's completed receipt and audit to reconcile it without
dispatching the mutation again. A later connected queue gate injected a failure
after GraphJin committed `record_create` and wrote its trigger audit, but before
the Records engine saved its receipt. Reconciliation derived the exact mutation
identity from the frozen action and pre-dispatch target context, verified the audit, repaired the engine receipt
and restored the Harness effect without a second GraphJin write. If the audit
does not prove the mutation, the outcome stays unknown and is never permission
to retry it. A restored terminal run now records an already-absent sandbox
as successful cleanup; other deletion failures stop replay so teardown
cannot be silently ignored. The connected data-source gate verified the
already-absent case. Restored-run usage remains marked unavailable in
that gate and needs separate telemetry reconciliation.
An installed-manifest `PluginRegistry` fixture now passes the production
pg-boss Work queue, Harness OpenShell agent, actor-bound proposal, human
approval and worker action executor. Duplicate delivery does not repeat the
model call or action request; duplicate execution restores the one plugin
effect. The worker rechecks current installed kind, owner, scope, version and
integrity before execution claims the effect: removing or changing the plugin
after approval leaves no claim or RPC, and restoring the identical manifest
permits the same approved request to execute once. The fixture runs its
synthetic effect in a separate real OpenShell plugin VM. A later connected gate
used the current plugin-base image to execute an approved external HTTP effect
from that VM once and restore the same receipt on duplicate execution. The
older v3.5.6 plugin image lacks the OCI `USER` required by OpenShell 0.0.116.
The direct `memory_save` capability now binds an explicit customer Work-run grant,
validates the model's bounded text/kind/scope, and persists host operation intent
before calling OpenNeko's existing memory service. A successful connected
OpenShell/Ax turn stored one real memory row and matching host/Go receipts.
If the host commits the memory but loses its receipt, the outcome stays unknown
and later effects are fenced; no automatic redispatch is permitted. A direct
`skill_create` capability now binds the Work run to its trusted org skills root,
rechecks current administrator authority, journals intent and publishes a
complete new skill directory. A connected Ax/OpenShell turn created a skill
with a supporting file; a separate run staged and read its `SKILL.md`, and an
ungranted token was denied. A second connected sequence inspected the entire
installed skill tree, submitted its exact version to `skill_update`, replaced
the instructions and supporting file, and read the new version in another run.
The host locks publication across workers. Unit tests reject stale versions
and restore the old or retain the new tree after interruption on either side
of the directory swap. Customer Work recovers pending swaps before staging.
A production pg-boss Work queue then created the skill, read it in a later run,
inspected and updated the whole tree, and read the new version in another run.
Redelivery of both writes reused the host receipt without another model call
or user-visible result. Chromium found each completed answer exactly once before
and after reload. Most admin writes remain unqualified. The full grouped
OpenShell/worker/Hermes/browser regression passed on 2026-09-28 with synthetic
data and no real provider key; it does not qualify the excluded admin writes.

**Queued workflow slice (2026-09-27):** Harness now receives the owning
`work_run` and trusted `workflow_run` identity. Its parent Ax agent may run
bounded read-only children and emit typed workflow outputs through a dedicated
host-journaled broker operation. The broker verifies that both run IDs belong
to the same org and workflow execution, records intent before persistence,
and refuses an ambiguous retry. A completed Harness workflow must have an
output receipt; the host restores a missing output event from that receipt.
An isolated pg-boss workflow executed two child GraphJin investigations,
recorded one finding, and completed under OpenShell. An API-admitted run
returned a pollable result and a journaled output receipt; duplicate queue
delivery did not repeat the effect. OpenNeko's existing finding dedupe may
reuse a prior output row and increment its seen count, so the receipt and
current run's output event are the evidence of emission. The API admission's
model-call ceiling now reaches the shared Ax parent/child budget: a four-call
run stopped at four model requests without an output. The full connected suite
and unchanged Hermes checks passed. Broader mutation families remain open.
Server-side GraphJin agent calls remain under
their own service budget and the API guard's conservative estimate.

**Workflow action slice (2026-09-27):** a queued Harness workflow now sees
only ready, held pack action kinds and can submit a governed `propose` call.
The broker binds the request to the exact running `workflow_run` and its
`work_run`; the worker preflight freezes the action, actor, schema and policy
before human approval. The API admission's initial backend label is replaced
with the selected runtime backend before execution, and a running attempt
cannot switch backend on retry. A pg-boss/OpenShell/API acceptance
recorded one pending request and one typed finding, ignored duplicate queue
delivery, then executed the synthetic effect once after approval. Other
product mutation families and auto-execution semantics remain open. The full
isolated connected suite and unchanged Hermes checks passed.

**Workflow definition slice (2026-09-27):** customer Work runs expose a direct
`workflow_save` tool only with a run-bound broker grant. The host validates the
full definition and current actor, records the operation before calling the
existing workflow store, and persists the workflow and subscription confirmation
cards before acknowledging success. A new name requires `expectedVersion=absent`;
an edit must use the exact version token returned by the actor-filtered workflow
list. The store rejects a changed row, preventing a stale edit from overwriting
another actor's change. A connected queued Work fixture created a cron-triggered
workflow with a batch contract, listed it through MCP, edited it, checked the
database and confirmation events, and rejected the old version token. Chromium
found the edited confirmation exactly once before and after a Work-thread reload.
The image builder now includes the local MCP bridge so the list result retains
its version token.

**Workflow trigger slice (2026-09-28):** the same direct grant now admits
`triggers.when` and `triggers.watch`. The host rechecks the current enabled
Work actor and runs each GraphJin read as that run before writing. The Harness
save puts the definition, source-change subscription and condition watcher in
one database transaction, then journals the outcome and persists trigger cards.
Editing `when` updates the one source-change slot instead of appending another.
In the connected queue fixture, creation and edit retained one subscription,
a saved watcher fired on the seeded GraphJin result, and a detected mutation
loop, invalid table, invalid watcher path and disabled author left no new
definition. This establishes trigger wiring and one sweep on synthetic data;
trigger crash-edge qualification is below.

**Cron redelivery hardening (2026-09-28):** a queued firing now rechecks the
current enabled cron definition and persisted schedule version at claim time,
so an edit or disable before the next sweep cannot start an obsolete firing.
Linking the prepared run must affect exactly one running firing. If a linked
run fails after the agent starts, the worker retains that link and settles a
terminal run instead of releasing the firing for another model/effect pass.
An isolated Postgres suite passed the stale-claim, duplicate-claim, terminal
failure and restart-recovery cases; the worker scheduler unit suite passed.
The full queued OpenShell cron path was qualified in the connected trigger
replay gate below.

**Source-change replay hardening (2026-09-28):** stream matching now commits a
durable delivery identity, observation and source audit in one transaction before
queue dispatch. A worker sweep dispatches stranded records, fences stale
subscription revisions, and settles terminal linked runs without replaying a
possible effect. An isolated Postgres and pg-boss test passed duplicate event
delivery, duplicate consumer claims, pre-dispatch crash recovery, subscription
edit rejection, and failed linked work-run reconciliation. This test exercises
the delivery ledger and queue; the connected gate below covers the GraphJin
websocket, workflow turn and reconnect replay.
The isolated queue test also injects a lost acknowledgement after pg-boss
accepts a job: the accepted job claims the pending delivery once, and the
recovery sweep does not dispatch another workflow.

**Connected trigger replay gate (2026-09-28):** the isolated acceptance stack
used the production pg-boss `workflow_run_fire` handler, Ax, the host GraphJin
broker, seeded GraphJin, and real OpenShell 0.0.116. A real GraphJin
source-change websocket match produced one completed workflow and observation.
The handler now commits a claimed delivery link, its workflow thread, work run,
workflow run and spend reservation in one transaction. A connected gate forced
the schedule and source-change link to fail after those inserts and verified
all rows rolled back. Successful cron and websocket deliveries still executed
once, and duplicate handler calls did not repeat a model call. Recovery of a
linked, queued run after a crash immediately following that commit now passed
the same connected queue: an expired delivery lease was swept and re-enqueued,
then the existing run completed its OpenShell turn; a subsequent redelivery
made no model call. An acknowledgement-race fixture reclaimed a linked run
while dispatch still showed an active lease. Only a work run still marked
queued is eligible; a running run may already have performed effects and
remains fenced. The queued-start state transition allows only one worker to
reach the model if an old worker resumes after lease expiry.
A further connected gate edited the workflow definition after preparing each
trigger type but before model start. The revision-bound start refused both;
recovery cancelled the linked queued runs, released spend reservations and
settled the deliveries without a model call. A sub-millisecond Postgres
fixture verified the cron scheduler copies the exact definition revision.
The fixture restarted GraphJin; the subscription manager reconnected, the
same snapshot arrived again, and the durable delivery ledger dropped it
without another workflow or model call. A second handler invocation likewise
created no second run. A due cron firing completed through the same path;
its repeated handler invocation also created no second run or model call.
Both workflows recorded the required output. It uses synthetic model and
GraphJin data. A separate real websocket test checks reconnect and changed
snapshot delivery without the workflow queue.

**Workflow deletion slice (2026-09-27):** the direct `workflow_delete` tool
requires the listed workflow ID, name and exact `versionToken`. Before a hard
cascade, the host reads the current run's persisted user message and requires
`DELETE WORKFLOW ${JSON.stringify(name)} PERMANENTLY` as the entire message
(with the workflow name JSON-quoted). It rechecks that
the run belongs to a current, enabled user who can see the workflow; the SQL
delete matches organization, ID, name and row revision. A missing confirmation,
unlinked or disabled actor, and stale revision leave the definition intact.
The connected queued fixture exercised denial and confirmed deletion through
Ax, OpenShell and the Work broker. It checked that dependent run and trigger
rows disappeared, one host receipt and one card were recorded, and Chromium
found the card once before and after reload. The existing Hermes delete route
is unchanged.

**Approval-rule slice (2026-09-27):** admin customer Work runs expose a
direct `rule_save` tool with a separate broker grant. The host rechecks the
run actor's current admin membership or solo-admin status before writing,
journals the exact request, and persists a confirmation card before returning
the receipt. Create requires `expectedVersion=absent`; edit uses the exact
version returned by the pinned MCP rule list. A short transaction serializes
same-name policy writes and rejects ambiguous duplicates or stale revisions.
The queued OpenShell/Ax fixture created an approval-required synthetic rule,
listed and changed it to a narrowly scoped low-risk auto-approval rule,
rejected a stale edit, a member edit and a disabled-admin edit, and proved two
concurrent create-only attempts yield one row. Chromium found the edited rule
card once before and after reload. Harness action proposals still force
human approval until auto-execution is separately qualified.

**User-administration slice (2026-09-28):** eligible customer Work runs can
propose member invitation, nonadministrator deactivate/reactivate and active
nonadministrator promotion through the run-bound `user_admin` action grant.
The host validates exact actor and target state before creating a pending
internal request. Human approval and the existing worker executor own each
effect. Execution rechecks the frozen definition and current actor/target;
a disabled requester or changed target state prevents an effect claim. A
connected Ax/OpenShell/pg-boss fixture invited a synthetic member, then
discovered it through the pinned management MCP list for the three state
changes. Each operation remained pending before approval, applied once after
approval and restored its saved result on duplicate Work and action delivery.
Chromium checked the answers and terminal approval cards across reload.
Administrator deactivation, demotion and administrator invitations still need
separate lockout and identity-provider qualification; other administration
families remain open.

**Group-creation slice (2026-09-28):** the separate run-bound
`group_admin` grant admits `create_group` with a bounded name and
description. The host records the absence of that name before proposal and
checks it again after human approval. A queued Ax/OpenShell/pg-boss run
created one pending request without a group row, rejected a disabled
requester and an intervening same-name group before claiming the effect,
then created one group through the existing worker adapter. Duplicate Work
and action delivery produced no second request, model call or group.
Chromium found one answer and completed approval card after reload. A
second queued Work turn used pinned management MCP lists to select that
group and an active user, then proposed `add_member` through the same
grant. Approval preceded the local membership row; a changed membership
was rejected before the effect claim, and duplicate delivery reused one
result. Chromium found one membership answer and card after reload.
Other group, item-grant and data-access changes remain unqualified.

**Data-source registration slice (2026-09-28):** a named customer Work
actor can propose `data_source_admin.register` for a bounded source name,
label and kind. The run-bound internal grant creates only a disabled
placeholder after administrator approval; connection details and secrets
remain in Settings. The host checks the current actor and name before
proposal and again before effect claim. A queued Ax/OpenShell/pg-boss run
left the registry unchanged pending approval, rejected a disabled requester
and intervening same-name source, then created one disabled API placeholder.
Duplicate Work and action delivery produced one request and source row;
Chromium found one answer and completed approval card after reload.
Enable, disable, default and remove still require separate effect gates.

Source inventory for the next M5c slice:

| Handler | Current effect boundary | Required harness qualification |
| --- | --- | --- |
| `neko_pack_actions` / `neko_plugin_actions` in `work/tools.ts` | Policy evaluation, action request, optional enqueue and wait | Pack proposal uses M4 approval/claim path; plugin auto mode needs a separately qualified queue/effect receipt before admission |
| `neko_workflow_builder` in `workflows/builder-server.ts` | `saveWorkflowWithTrigger` or destructive `deleteWorkflow`, then confirmation card | Cron/batch and data-change/watch create/edit use a direct journaled broker call. The latter preflights through actor-bound GraphJin reads and commits definition plus trigger rows in one transaction; connected create/edit, one watcher sweep and rollback passed. Revision-bound, explicitly confirmed delete uses a separate direct call. The real GraphJin websocket reconnect/replay gate passed; qualify trigger crash-edge recovery separately. |
| `neko_rule_builder` in `workflows/rule-builder-server.ts` | `upsertActionPolicyByName`, then confirmation card | Admin Work create/edit uses a host journal, current-role check, listed revision and persisted card. Rule deletion and broader auto-execution remain separate gates. |
| `neko_memory` / `neko_skills` in `work/tools.ts` | Durable memory write or sandbox file write | Direct `memory_save` has a host receipt and unknown-outcome fence; MCP save remains excluded. Direct `skill_create` and whole-tree CAS `skill_update` journal and publish host-owned org skills; connected Ax/OpenShell and queued worker/browser gates pass, including redelivery and interrupted-swap recovery. Add idempotency before any automatic retry of a lost memory-save receipt. |
| Admin manager tools in `work/tools.ts` | `proposeAdminAction` creates internal action requests | Member invitation, nonadministrator deactivate/reactivate and promotion, group creation, custom-group `add_member` and disabled data-source registration use run-bound internal grants, human approval and worker effects with actor/target recheck. Qualify administrator lockout-sensitive changes and other group, channel, data-source, plugin and source-config mutations separately. |
| `neko_records` in `work/tools.ts` | Read-only registry-backed browse/get; record writes are governed action kinds | Qualify exact record IDs and current grants at the action executor, not by treating a read tool as mutation authority |

These are source-level contracts, not acceptance claims. A successful MCP reply
alone cannot establish whether a host write or queued effect happened once.

**Deliver:** enable the existing action, workflow, rule, memory, skill and admin
write contracts through the shared boundary. Inventory which handlers already
create approvals, which mutate synchronously and which enqueue effects. Attach
journal/claim/recovery at the actual effect boundary; wrapping an MCP response
alone does not protect a handler that already committed a write.

Reuse OpenNeko's existing authorization, preflight, approval UI and queue executors.
Do not blanket auto-approve MCP calls, create duplicate approval systems, or grant
all broker routes to enable one tool. Bind approvals to exact arguments, schema,
actor and target; recheck policy at execution. Preserve documented auto/ask/deny
semantics only where execution is qualified; the existing conservative proposal
path remains human-approved until then. Workflow-only tools require host-supplied
workflow/run identity. Legacy fenced output must not accidentally execute alongside
a tool call; normalize any necessary compatibility behavior inside the adapter.

**Verify:** through the real browser, create/edit a workflow and rule, propose an
integration action, save a memory, and perform allowed records/admin changes on
synthetic data. Confirm unauthorized variants never execute. Run workflow jobs
through the actual queue and verify outputs. Test approval reload/restart, changed
arguments, revoked roles and destructive-action confirmation. Kill execution before
dispatch, after commit and before receipt, and after receipt. Reconcile only with a
qualified status/idempotency contract; otherwise report unknown without redispatch.

**Exit:** every enabled mutating tool has a documented effect/recovery contract and
real persistence/queue acceptance; approval cards and receipts match actual effects.

**Dependency:** M5a and existing M4 effect infrastructure for the pack proposal
slice; the remaining MCP-backed product writes depend on M5b bridge qualification.

## M5d — Local tools, skills and artifacts

**Partial status (2026-09-27):** an opt-in Go file capability now reads bounded
UTF-8 files from a host-selected workspace, atomically replaces an existing
file only after a same-run read at the matching content version, creates new
files without replacement and searches small text files by literal path/content.
Go's `os.Root`
contains paths; regular-file checks reject final symlinks, oversized files and
cross-run traversal. A per-workspace read/write gate allows concurrent reads and
serializes harness edits/writes. Saved read/edit receipts restore the freshness state
for a new Ax attempt. A synthetic Ax run exercised Read → Edit → Write through the
durable operation journal, and the full race suite passes. The feature OpenNeko
backend binds the writable Go file tools only to this run's artifact directory;
it does not expose the checkpoint directory or a general shell. It supplies
its staged thread-upload root to a separate read-only `upload_read`/`upload_search`
catalog. A 2026-09-26 production queue run passed these calls through OpenShell,
found the current thread's CSV, excluded a sibling thread's upload and recorded
no GraphJin operation. A second queued run created a small CSV with `file_write`,
pulled it back from OpenShell, emitted one Work artifact event and excluded a
sibling run's file. The existing web download-route authorization suite passes,
and the 2026-09-26 isolated Work page rendered `result.csv` as an artifact link.
Clicking it returned HTTP 200; fetching the same URL returned the exact
`lead_id\nLEAD-42\n` CSV bytes with attachment headers. A separate connected
worker/OpenShell turn read a staged `SKILL.md` through a read-only skill tool and
created the expected CSV artifact; the script never called a model. This qualifies
a small artifact through the browser, not the Daily Lead Union batch. A separate
connected Work queue gate now read a staged `SKILL.md`, then generated a real
XLSX and DOCX from one selected CSV inside the no-provider/no-network process
sandbox. The host verified both Office
ZIP/XML packages, source value, exact output hashes, artifact event count and
authenticated Work download bytes, MIME types and attachment headers. Chromium
downloaded both files after a page reload with one link per artifact. This
qualifies synthetic Office generation; the real customer lead union remains
unconnected. Harness processes now
take shared or exclusive advisory locks on the workspace directory inode around
reads, searches, edits and writes. A real second-process test passed concurrent
readers, a writer blocked until both readers released, and a reader blocked
behind a writer. The second version check catches many noncooperating external
edits, but external writers must follow the same lock protocol for a strict
atomic edit contract.

An opt-in model-visible `process_run` tool now reaches a run-bound Work broker
operation. The trusted host stages only selected current-thread uploads and
invokes the pinned `processshell` executable. It starts a separate OpenShell
sandbox without provider credentials or network grants, captures bounded output,
and publishes only declared regular files into a fresh run-owned directory
after successful execution and sandbox deletion. It rejects symlinked inputs
and outputs, limits individual and aggregate output size, and treats sandbox
deletion failure as fatal before making a file visible. Its
admission and result are journaled as one durable operation. A connected
production queue run read a synthetic upload, wrote a CSV, emitted one artifact
event and persisted one operation receipt. With the real web API enabled,
authenticated download returned the exact CSV and attachment headers, rejected
an unissued filename, and Chromium found the artifact link exactly once before
and after reload. The fixture also verified absent broker/provider credentials
and denied direct network access inside the process compartment. A second
queued run published a 2 MiB artifact with an exact host hash and one artifact
event; the authenticated web route returned the exact bytes with attachment
headers. The selected-upload fixture excluded an unselected sibling file and a
host-only environment canary from the process compartment. The separate real
OpenShell process suite now
also rejects publication after a script writes an output and exits nonzero,
after a partial output is cancelled, and when a declared output exceeds the
16 MiB per-file limit.
A separate production-queue Work fixture now runs a script that writes a
partial CSV and exits nonzero. The Work run fails, emits no artifact event,
leaves no published file, and retains one unresolved host operation rather
than redispatching on retry.

**Connected Work process cancellation (2026-09-28):** the web Stop route's
durable cancellation transition is now observed by the worker. A queued Work
run started a model-visible `process_run` whose script wrote a partial CSV and
spawned a child process. The fixture applied that same durable transition,
then observed cancellation telemetry, deletion of the agent and process
OpenShell sandboxes, no artifact event or published file, and no successful
operation receipt. A late handler invocation and a late attempt to mark the
run running could not reopen it or call the model. A second isolated run
issued the authenticated HTTP Stop request while the process was active;
the route returned `recovered: true`, and the same sandbox, artifact and
late-delivery assertions passed.

**Connected process limits (2026-09-28):** a queued Work run started the
same long-lived process with a host-configured two-second deadline. Docker
inspection of its active OpenShell compartment verified a one-core CPU limit
and 512 MiB memory limit. OpenShell ended the exec at the deadline; the worker
marked the Work run failed, removed both sandboxes, and published neither an
artifact event nor a file or successful operation receipt. The separate
OpenShell suite covers oversized declared outputs. A further queued Work gate
rejected a 17 MiB declared file without publishing an artifact or recording a
successful receipt. It also ran a script that printed 96 KiB: the process
receipt flagged truncation and kept the model-visible output within 8 KiB,
while publishing the valid CSV exactly once. Both process sandboxes were deleted.

**Deliver:** Read, Edit, Write, file search and shell/process tools under
OpenShell, file read-version checks, read-parallel/write-exclusive scheduling,
output/process limits and scoped
artifact publication. Use OpenNeko's existing skill catalog, upload workspace and
artifact pipeline. Broker credentials and privileged MCP bridge state must remain
in a trusted execution compartment inaccessible to model-generated subprocesses;
merely clearing inherited environment variables is insufficient.

**Verify:** browser task reads an upload, follows an installed skill, generates a
spreadsheet/document and downloads bytes verified against the produced artifact.
Test two overlapping reads, mutation exclusion, changed-file rejection, symlink/path
escapes, cross-run access, hostile child attempts to read broker secrets, process
tree cancellation and resource ceilings. Cancelled/failed tasks do not publish
unvalidated artifacts. Reuse existing document processing tools; do not rebuild them.

**Exit:** full upload → execution → artifact → authorized download path passes with
actual sandbox processes. Keep arbitrary subprocess execution disabled until the
credential boundary passes adversarial checks.

**Dependency:** M5a. Build local tool execution and file safety against fixtures
now; connected artifact publication follows M5b/c. A controlled batch runner may
be implemented locally, but its GraphJin authorization/file path remains unqualified
until M5b.

## M5e — Bounded delegation parity

**Partial status (2026-09-27):** Ax Go now owns one opt-in `team.researcher`
child with a separate runtime and only exact host-admitted read tools. Child
calls share the parent's durable operation journal, model-call limit, usage
receipts and cancellation. Admission rejects missing, duplicate or effectful
child tools and changes the catalog binding on scope changes. A deterministic
parent delegated two investigations; a lower shared model limit and parent
cancellation both stopped the child. A connected OpenShell/OpenNeko run sent
two child memory searches through the real MCP bridge and actor-bound broker,
then recovered two operation receipts and nine model-call receipts from the
checkpoint. The isolated full suite passed and removed its containers.
OpenNeko's Work backend advertises this child only when delegation is enabled;
the current child receives GraphJin lookup and memory search. Child lifecycle
events project into delegation telemetry without a second inner-model usage
entry. One queued workflow now owns two read-only child GraphJin investigations
and one parent-emitted finding. The API-admitted variant has a pollable result,
durable output receipt, duplicate-delivery check and shared Ax call ceiling.
An isolated agent job with an explicit server-side GraphJin grant also ran one
child investigation through the host broker, with one lookup receipt and six
model-call receipts. A model-only job completed without a broker or GraphJin
operation; an ungranted job token was denied at the broker. A local checkpoint
test interrupts a child after its read receipt, then resumes without repeating
the host read. A connected agent job with a GraphJin grant rejected a child call
when delegation was disabled and made no GraphJin operation. A second connected
job killed the Go process after its child's GraphJin read, then resumed with
one reused lookup and no second server-side GraphJin call. The full isolated
suite passed, including Hermes and OpenShell teardown checks.
A deterministic child read returning `is_error` now leaves the parent run
failed and incomplete even when later model text claims the task succeeded.

For queued workflows, the `workflow_run` remains the product outcome, the
owning `work_run` remains the execution/journal parent, and child IDs are
correlated spans under that parent. The host grants each child an exact subset
of the workflow actor's admitted read capabilities. The parent owns output
emission and action requests; an API caller need not stay online for a child
to run. Verify this with a queued synthetic workflow that delegates two
investigations, joins their evidence, emits one output, and returns one
pollable result. Cancellation, duplicate queue delivery, changed actor grants,
and an ambiguous output receipt must preserve the same parent/child lineage
without replaying an effect.

**Deliver:** child agents using the same Go/Ax engine with narrowed capabilities,
explicit context, child ownership and shared parent budgets. OpenNeko decides
whether delegation is enabled. No named-agent framework, swarm scheduler or new
background-work system. Children cannot expand authority, bypass approval or
retain execution after parent cancellation.

Start with Ax Go's `AddChildAgent` for owned, serialized delegation. Qualify
`AxFlow` only for fixed independent work that needs parallel fan-out. A child is
a fresh scoped conversation, not a cloned parent stack or an OS process fork.
The current acceptance covers inline children within one queued `work_run`.
Separately queued child runs are deferred until a workflow actually needs an
independently scheduled, long-waiting or separately retryable stage; they are
not an M5 exit gate. An API caller disconnecting does not create that need:
the queued parent already owns the durable result and polling contract.

**Verify:** a real parent task delegates two bounded investigations, combines
verified results and exposes correlated progress. Exercise child failure, parent
cancellation, capability escalation attempts, spawn/depth limits, crash recovery
and usage rollups without double counting. Test the disabled-delegation mode too.

**Exit:** existing delegation use cases have verified equivalents or are explicitly
excluded from the rollout cohort. Do not claim full Hermes parity while they are excluded.

**Dependency:** M5a–d and M6 shared budget enforcement.

## M6 — Ax routing, context and efficiency

**Local status (2026-09-29):** Ax's invocation-scoped rate limiter now admits a
model request only after a content-free `model.request.started` event is durably
recorded. A trusted `max_model_calls` limit (16 by default, at most 64) is
enforced across Ax attempts; resume reconstructs spent calls from the checkpoint.
A two-call fixture proved that interruption and resume do not authorize a third
request. This limits calls even when provider usage is missing. The same
limiter now journals content-free model finish receipts with Ax-normalized token
counts and terminal `complete`/`partial`/`unavailable` coverage. Go fixtures
verify three-call totals, an omitted provider report and exact aggregation
through checkpoint resume. OpenNeko's Harness adapter projects the outer-model
aggregate into its existing usage event. The connected worker/OpenShell run
recorded 30 input, 30 output and 60 total fixture tokens with complete coverage;
queue redelivery kept exactly one outer usage event. No real provider key was used.
A run-local Goja handoff now preserves bounded distilled evidence for executor
code. A local Ax fixture verified that an 85 KB observation stayed out of the executor model
request while its narrowed reference supported a second tool call; the connected
worker/OpenShell/GraphJin regression passed after this change. Failed or partial
tool outcomes no longer return `status: completed` with the responder's
unverified success claim; the focused durable-write and broker tests cover the
terminal mapping, and a checkpoint test proves an `is_error` result remains
incomplete after resume. An optional, version-pinned host terminal gate now
checks candidate completion against committed operation receipts and journals
`terminal.checked` before `run.finished`. Local no-key fixtures prove a missing
receipt fails, a successful receipt is accepted, invented evidence IDs fail,
and interruption after the saved decision can resume without redispatching the
operation; changing the gate version blocks that resume. The pinned Ax Go
actor loop reads `max_actor_steps`; the Harness now sets an explicit eight-step
parent and three-step child ceiling. When the parent exhausts its steps, an
optional version-pinned host gate may select successful saved receipts for one
bounded, tool-less Ax finalizer call. The ordinary terminal gate checks its
answer afterward. No-key fixtures prove the evidenced case completes, the
no-evidence case fails without a finalizer call, and a completed finalizer
replays from a validated checkpoint on queue redelivery without contacting the
model or tool. Connected source-change and cron worker runs now prove that
`terminal.checked` accepts a broker-confirmed `workflow_output_emit` receipt
before `run.finished`, and that queue redelivery leaves the terminal checkpoint
and upstream model-call counts unchanged. Other artifact kinds still need
host-specific verification. The
standalone OpenNeko adapter now installs these gates only for queued workflow
runs: the selected evidence must be a bound, broker-confirmed
`workflow_output_emit` receipt. A no-key integration fixture runs the model,
session store and broker together; a final answer without output fails, while
one persisted output completes. OpenNeko's existing post-run output query
remains an independent check. Other artifact kinds need their own host gate.
Host-approved Ax stage routes now select distinct context/executor/responder
models. A two-route HTTP
fixture verified model and credential selection on all three stages, model-call
receipts, exact replay without new provider calls, and rejection of a changed
profile at checkpoint recovery. This is local Go coverage; OpenShell multi-route
qualification remains open. A fault-injection run found that this pinned Ax Go
build accepts `executorModelPolicy` but does not use it in live execution. The
Harness now has an opt-in host-approved executor route switch after a committed
actor-code error. A local no-key run observed context, baseline executor,
stronger executor and responder calls with the actual route and price recorded;
checkpoint interruption after the error resumed on the stronger route, and a
model-call ceiling prevented that dispatch entirely. The OpenNeko feature
branch passes the optional route policy and third OpenShell provider through
its trusted manifest. The Go runtime now also accepts explicit, approved
one-step fallback pairs. No-key fixtures confirm a content-free 503 moves to
the secondary route, with both attempts charged; 403 stays on the original
route; a 429 cannot bypass the model-call ceiling; and a journal failure stops
secondary dispatch. The OpenNeko parser includes fallback-only providers and
credential aliases. An isolated OpenShell 0.0.116 run now exercises three Ax
stage routes through separate provider profiles. The upstream fixture rejects
the wrong route model or bearer credential; context, executor and responder
each ran once with complete usage and the expected answer. This qualifies
connected route selection and broker key replacement. The same isolated
gateway also proved one approved fallback after a content-free 503, while a
403 stopped before any secondary request. A 429 under a one-call ceiling
journaled the fallback decision but denied the secondary dispatch before it
could bypass the budget. A connected OpenNeko launcher run
now passed the trusted manifest and distinct provider credentials through the
worker path, recorded a completed answer, and downloaded its checkpoint and
receipt. A separate connected run now confirms that an executor code error
switches only the next call to the approved stronger route, with a durable
`executor.step.failed` receipt and the responder still on its own route. This
exposed two integration drifts after the OpenNeko v3.10 rebase:
the UI MCP server advertises a revised `render_cards` schema, so the pinned
Harness admission snapshot was updated from the actual MCP `listTools`
response; and OpenShell does not make late-attached provider credentials
available to the running process, so the feature worktree now passes all
approved providers at sandbox creation. Broader rate-limit behavior and
streaming first-content qualification remain open. Run the route
check with `HARNESS_M5_FAST=1 HARNESS_M6_ROUTING_ONLY=1` through
`integration/openneko/run.sh` using the pinned CLI and isolated OpenNeko worktree.
Resume now projects saved operations into a bounded 32 KiB evidence index. The
executor can retrieve a full prior operation by ID from the validated checkpoint
without redispatch. A fixture reconciled a 200 KB result, proved the resumed
model requests stayed under 100 KB, retrieved its label in Goja, and completed
with no new lookup. This addresses crash-resume context pressure; ordinary
long-turn compaction remains open. A live large tool result now returns a
bounded run-local reference plus preview to Ax; the full result stays in the
authoritative operation checkpoint and can be inspected with
`harnessSavedOperation(id)` during the same attempt or after resume. A fixture
retrieved an 85 KiB result during distillation without putting its body in the
executor request. Another saved a 200 KiB live result, interrupted after its
tool event, resumed from the reference, and made no second tool call. Terminal
replay now checks a durable hash of the trusted admission scope even when its
tool catalog is no longer installed; OpenNeko binds that scope to its org and
thread IDs. The local cross-scope replay fixture denies a different scope.
An owned child can retrieve its own large read result but cannot inspect that
result from the parent's live runtime through the same numeric ID.
Connected tenant-isolation and larger file-backed result qualification remain
open.
The OpenNeko branch now has an opt-in trusted route manifest for Harness-only
multi-provider OpenShell launches. Local launcher tests check provider order,
distinct credential aliases, route-specific egress, recovery manifest
propagation and Hermes isolation. The Go inspector derives the same route digest
without model credentials and rejects a changed manifest. Connected gateway
credential replacement and worker execution now pass with synthetic providers.
An optional `skill` route now sends semantic skill selection through a separate
Ax call before the main agent. Exact-name matching uses no model. Go tests
verified the cheap selection route, catalog-validated hint, shared model-call
admission, replay without a second request, and rejection when the accepted
skill query changes. OpenNeko passes the bounded current request into the run
and its recovery inspector; launcher tests cover the field. A connected
OpenNeko worker run now selects a staged skill through its own approved
OpenShell provider, performs a brokered GraphJin lookup, and completes through
separate context, executor and responder routes. The durable checkpoint records
the semantic selection, each outer model route, one GraphJin receipt and
separate outer versus remote token totals. The server reports its own pinned
fixture model despite the Harness route manifest. This proves the routing
ownership boundary with synthetic models; the production GraphJin `hard`
profile and its pricing still require deployment qualification.
An outer Ax token-admission ceiling now reserves before every model call,
including skill selection and child turns. It charges missing usage
conservatively, uses the largest reported call as the next reservation, and
reconstructs spent usage from events on resume. A provider can exceed the
reservation within one call; the run then fails and makes no further model
request. OpenNeko pins a 1M-token admission ceiling in its accepted run
identity and lowers it to a workflow API claim's per-run token ceiling. Local
fixtures cover reported usage, missing usage and interruption/resume.
GraphJin lookup admission now reserves 49,152 tokens for its 12-step server
agent before broker dispatch, then charges the server's flat usage receipt
once. A missing receipt retains the reservation. Saved lookup results rebuild
this charge on resume; a depleted allowance blocks both another lookup and
another Ax call. OpenNeko's queued API ceiling now includes the same inner
receipt or conservative missing-usage charge. A content-free
`tool.finished.remote_usage` projection exposes reported GraphJin tokens,
server LLM-call count, usage coverage and the admission charge without merging
it into outer Ax usage; durable replay retains that projection. The OpenNeko
adapter now passes this receipt to API admission and metadata-only telemetry,
so nested `usage` fields in result data cannot masquerade as GraphJin agent
usage. Reported server LLM calls count toward a queued API call ceiling after
the broker receipt is persisted. The connected worker run confirms the server
model profile and separate remote-usage receipt; a priced GraphJin deployment
still needs qualification.
The pinned Ax Go build passed deterministic `AxRunControl.Steer` fixtures:
guidance queued inside an executor tool result reached the responder, while
root-targeted guidance from a distiller result reached both executor and
responder. A `root/executor` target reached only the executor. Therefore a
durable lifecycle hook must choose an explicit stage path and verify the
`applied` event; unscoped steering cannot promise exactly-once delivery.
The Harness now exposes a version-pinned `AfterTool` lifecycle hook for a
host-owned, bounded replacement state snapshot. After a tool result is
committed, the hook's JSON state is journaled and steered to one explicit Ax
stage. Resume reconstructs the latest snapshot from the checkpoint and sends
it as data to the new attempt; a changed hook version is rejected before
model dispatch. Local no-key fixtures verify the responder sees an update
once after a tool result and the resumed distiller sees the same committed
snapshot without repeating the tool. The queued OpenNeko workflow adapter now
derives a responder-only state update from the bound, broker-confirmed output
receipt. A connected OpenShell/worker run records exactly one update after the
committed output, and the synthetic model rejects its responder request unless
that host state is present. The Harness now correlates Ax's queued control ID
with its `applied` event, journals that acknowledgement, and rejects a completed
run if a host update was never applied. Local fixtures cover both paths, and
the connected source-change and cron worker checkpoints each record one Ax
application acknowledgement before their verified terminal result. Queue
redelivery keeps those checkpoints unchanged. A separate connected source-change
queue run now kills the sandbox process after its output and state receipts but
before the responder completes. OpenNeko makes one bounded re-entry into the
same Harness launch journal; checkpoint and broker reconciliation resume the
run without re-emitting the output or calling GraphJin again. The resumed
distiller receives the saved host state, the terminal gate accepts the original
output receipt, and queue redelivery makes no new model call. This exposed and
fixed the host journal's `workflow_output` versus Harness callable
`workflow_output_emit` name mismatch during receipt reconciliation.
Ax's stage-option rate limiter did not override the already-bound outer client
limiter in a no-key route fixture, so call-time stage labels remain unavailable
from that hook. The Harness now emits a separate content-free
`model.stage_usage` projection from Ax's per-stage chat log after each attempt.
Local route and child-agent fixtures prove distiller, executor, responder and
child request counts with reported usage, while ordinary model request events
remain the only admission and total-usage receipts. Extra model calls omitted
from Ax's stage log are marked `unattributed`; this telemetry does not expose
prompts or double-count tokens. Per-call stage attribution and cost accounting
remain open for connected qualification.
The OpenNeko feature branch now validates and forwards that projection as a
metadata-only `model.stage_usage` observation. Its summary accumulator excludes
the attribution view from additive run usage; focused LLM/telemetry tests and
worker typecheck pass. A connected queued worker run now exports
`model.stage_usage`, the stage label and request count through a local OTLP
receiver; the export contains the run ID and excludes the workflow prompt.
Exact per-call stage/cost attribution remains open.
The Go route manifest now accepts a versioned, complete per-route upper-bound
price profile and an optional GraphJin server-agent price. Price changes alter
the trusted route digest, so a resumed run cannot silently switch accounting
rates. When the host supplies `max_cost_micros`, the harness requires this
profile, reserves before each outer Ax or GraphJin dispatch, replaces the
reservation with reported usage when available, and retains the reservation
when usage is absent or dispatch is interrupted. Cost charges are content-free
durable events and a terminal summary; checkpoint resume reconstructs spend
from those events. Local no-key fixtures prove model admission, missing-usage
charging, terminal replay, GraphJin pre-dispatch denial and reported remote
cost replacing its reservation. OpenNeko's feature branch now passes its
queued workflow API cost claim through the accepted launch, recovery input and
sandbox adapter. It preserves the host price manifest and exports one run-level
estimated cost observation with a pinned pricing version. Focused Go, worker
and telemetry fixtures pass. A connected OpenShell run now charges three
distinct priced outer routes at 15, 30 and 45 micro-units, producing a 90-unit
terminal total under the pinned price version. A budget below the first
reservation denied dispatch with zero new upstream calls. A real priced
GraphJin server profile remains to be qualified. A connected queued workflow
API run now rejects an unpriced Harness route with the typed
`harness_pricing_required` code before creating a sandbox or calling a model.
The existing M5 child-workflow acceptance path now provisions a synthetic
versioned route and GraphJin price for its cost-capped API runs. Its manual,
API, model-call-ceiling and governed-action phases pass on the current Ax
runtime. This also corrected a stale fixture check that expected raw GraphJin
evidence inside a child model request: the acceptance assertion now uses the
committed GraphJin receipts and final broker output instead. The price
profile is an operator configuration requirement for cost-capped Harness API
runs; it must not be inferred from an untrusted prompt or hidden by dropping
the API's cost ceiling.
An ordinary-run fixture exposed a context-pressure gap in Ax's default Goja
diagnostics: three read turns with 14 KiB console observations grew an executor
request past 80 KiB and triggered an extra summary request. The Harness now
pins Ax's `checkpointed`/`balanced` context policy and caps each Goja turn's
console diagnostics at 4 KiB for parent and child runs. A no-key six-stage
fixture completes with its original user constraint present in every ordinary
model request, visible truncation, and a largest request below 45 KiB. This
is a bounded-observation regression, not proof that Ax compaction preserves
pending approvals or durable evidence across a longer mixed-tool run; that
exit gate remains open.
An additional no-key mixed-tool fixture combines five read receipts with a
pending approval and deliberately large executor diagnostics. Ax invoked its
trajectory summarizer on the tenth HTTP request. That internal request
originally omitted the accepted no-execution constraint even though ordinary
executor requests retained it. Harness now checks each Ax model request at
the client boundary and restores the accepted prompt when missing; it also
supplies the approved context route when Ax's summarizer omits a model key.
The routed fixture completed with one summarizer call, ten admitted model
requests, the constraint in every provider request, the pending approval still
visible later, and a largest request below 31 KiB. This proves the forced
checkpoint path locally, including accounting; connected artifact-evidence and
multi-route OpenShell checks remain open.
A connected queued source-change run now forces Ax's trajectory summary after
eight ordinary outer requests, then sends a responder request through the
OpenShell worker. Four GraphJin lookups, one 52,008-byte file write and read,
and one committed `file` workflow output precede the summary. The synthetic
model rejects any request, including the summary request, that loses the
accepted no-execution constraint, and rejects the post-summary responder unless
the committed output ID, `result.csv` path and REF-42 evidence are present. The
run completes in nine ordinary plus one summary call; the largest provider
request was under 38 KiB. The connected fixture checks the file's exact SHA-256
against independently generated bytes and the output's artifact path, then
confirms queue redelivery adds no model call, broker effect, output or artifact
change. Run the focused gate with
`HARNESS_M5_FAST=1 HARNESS_M6_COMPACTION_ONLY=1`. This establishes connected
constraint, saved-output and file-backed artifact retention across one
compaction. A larger batch artifact and authorized web download still need
qualification under the same context-pressure scenario.

A second connected workflow API run now keeps a governed action request pending
through Ax compaction. It uses a trusted priced OpenShell route, makes five
GraphJin lookups, records one broker output and one `pending_approval` action
request, then reaches the responder after one trajectory-summary call. The
synthetic provider rejects any request that loses the accepted no-execution
constraint and rejects the post-summary responder unless both the saved output
ID and pending-approval evidence remain visible. The run completes in ten
ordinary plus one summary call; the largest provider request was under 39 KiB.
The action adapter records zero executions, and API queue redelivery makes no
new model call or effect. This test exposed that Harness's terminal gate was
previously skipped for successful approval results. It now verifies approval
outcomes as well as answers, so a pending proposal cannot bypass a workflow's
independent broker-output contract; a no-output approval fails a local
regression with `verification_failed`. Run the focused connected gate with
`HARNESS_M5_FAST=1 HARNESS_M6_APPROVAL_COMPACTION_ONLY=1`.

Use the [Ax Go development guide](AX-DEVELOPMENT.md) to select the relevant
published skill for each M6 change. Verify its APIs against the pinned generated
Go package and a no-key fixture. The process-wide Ax usage observer is
best-effort; admission remains on the invocation-scoped limiter and durable
receipt path.

**Deliver:** approved Ax model profiles and fallback by known work boundary and
agent stage; aggregate limits across model stages, tools, retries and child/remote
work; context compaction preserving original intent and unresolved operations.
Qualify two approved OpenShell-bound Harness routes first. Do not assign one
easy/medium/hard label to the entire user request: its GraphJin lookup, skill
selection, executor turns and final response can have very different costs.
Route by the operation being performed: exact skill-name matching needs no
model; ambiguous skill selection may use a small model; the outer executor and
responder use their own approved routes; GraphJin runs its own server-side
agent. A budget classifier may size the run's allowance but cannot downgrade
the GraphJin route or choose a single model for the whole task.
Configure the separate GraphJin server-side Ax agent with a strong `hard` model
and reasoning profile at deployment. The Harness cannot reliably know whether
one lookup will need difficult discovery, and GraphJin currently owns
`agent.provider`, `agent.model` and `agent.reasoning` server-side rather than
accepting a caller-selected model per lookup. The Harness accounts for the
remote call; it does not route individual GraphJin agent turns.
Use deterministic skill metadata/exact-name lookup first; route any semantic
skill selection through an approved cheap Ax stage, never a model call from a
skill file. Give the outer distiller, executor and responder separate approved
model choices. Start the executor on a baseline; enable error-turn escalation
only for a host-approved alternate route after an actor-code error is durably
recorded. The pinned Ax build fails the native policy gate despite accepting
`executorModelPolicy`. The newer Go module
available on 2026-09-29 (`b780a14a3cb9`) still stores and validates the policy
without a live executor-selection call in generated source, so that update alone
does not clear the native policy gate. The Harness route switch passed local
no-key tests; its live OpenShell qualification remains open. Escalation must
not widen broker grants or replay an effect.
Keep durable state authoritative; prompt compaction is only a model-context projection.
For complex tasks, retain a compact plan with evidence and success criteria;
verify receipts and artifacts before reporting completion. Do not add another
planning loop around AxAgent.
Qualify one lifecycle state update after a tool result and one after resume:
the next model turn must see the updated evidence once, without changing
authorization or repeating a completed effect.
Reserve budget before each new model or remote call. Check observed usage on every
event, but do not rely on a provider's final cumulative snapshot to halt a run;
missing usage requires a conservative admission limit.

Add a Jev-style **budget** triage experiment using Ax Go's existing Typesafe
native decision client. It does not select the entire run's model or override
the GraphJin server agent. At admission, classify an approved bounded task summary plus
trusted signals (requested artifact, admitted tool families and input size) into
short-answer, multi-step, artifact/data-pipeline or uncertain work. Map that
result to a versioned initial budget profile inside the host's fixed hard cap.
Use the native choice probabilities rather than a single score: a split between
short and complex work must not average into an artificially cheap profile.
Record model/version, full class distribution, selected profile and classifier latency/cost; do
not record task content in ordinary telemetry. The classification call has a
short deadline and counts against the run's total cost ceiling. An unavailable
or uncertain classifier uses the current fixed default. Reassess only at durable
checkpoints when actual progress warrants more allocation; persist each extension and never
reset spent budget on resume. The classifier does not choose permissions, broker
grants or model/provider routes.
The shadow evaluator now uses the pinned Ax Go `Typesafe(...).SystemOne` native
Choice API, with a 2 KiB approved-summary cap, trusted input/tool-family
signals, a three-second deadline, a priced pre-call reserve and content-free
observations. No-key HTTP fixtures cover clear short and artifact work,
misleading short prompts, split probabilities, uncertainty, provider failure
and pre-dispatch cost denial. Its recommendation is not applied to live run
limits. The host journal commits the priced reservation before `SystemOne`
and its settlement afterward; both transitions are checkpointed, and a crash
after reservation never silently redispatches the classifier. Failed
reservation or settlement propagates an error. The opt-in OpenNeko workflow
gate now traverses a dedicated OpenShell Typesafe route with broker-replaced
credentials, complete provider usage, a content-free probability receipt,
approval persistence after compaction, and inert queue redelivery. The
classifier's artifact signal now follows OpenNeko's validated workflow-native
batch or query-to-file output contract through launch and recovery; other
artifact requests without a typed contract may still be missed. An operator-owned,
versioned budget policy now maps a valid classifier distribution to a shadow
short, multi-step, artifact or fixed proposal. Candidate call, token and cost
limits are monotonic by class and clipped to the trusted run's hard caps; they
never alter admission. The proposal is journaled separately from the model
settlement, pinned in the tool catalog, and reconstructed after a crash between
those two events without redispatching the classifier. Checkpoint-time
extension rules now emit a second shadow event after a successful, durable
tool receipt when the next model reservation would exceed the current proposed
call, token or cost allowance. Extensions advance one policy tier at a time,
are clipped to the original hard caps, and recover from a crash after the tool
receipt without redispatching the tool or resetting spend. They remain
observational. A GraphJin lookup exposed that waiting for its result was too
late: the proposed multi-step token cap could reject its 49,152-token remote
reservation. The shadow policy now journals one or more tier steps against the
completed Ax model call and a content-free `tool.proposed` intent for the
requested lookup, before remote admission;
journal failure prevents dispatch, and resume keeps the accepted tier without
repeating the classifier. Held-out outcome calibration remains required before enabling
dynamic limits or a canary.
The [budget evaluation protocol](M6-BUDGET-EVAL.md) now reads only validated,
stopped checkpoints and compares shadow admission against independently labelled
outcomes. It records the first counterfactual block, class false-lows, classifier
overhead and usage coverage. Its report cannot authorize a canary: changed-model
behavior and savings still require a controlled live comparison.

Persist large observations with scoped retrievable references and bounded excerpts.
Measure admitted-tool schema cost and selection errors. If they are material,
add search/describe over the pinned run catalog while keeping frequent tools
eager; discovery must not grant a new capability. Verify a previously discovered
tool after compaction and catalog-version change on resume.
Evaluate the [SoL-Pi ideas](https://arxiv.org/html/2609.20519v1) individually: observation references first; cache-aware
compaction next; edit-and-verify and cheaper-model log reduction only when measured
benefit justifies them. Include summarization/retrieval costs and deterministic
validation/fallback. No autonomous production harness evolution.

The queued source-change finalizer gate is connected to the actual OpenShell
worker. A synthetic actor exhausts eight executor steps after committing one
broker-verified `workflow_output_emit` receipt: the host admits one tool-less
finalizer, supersedes only the pending responder state associated with that
admitted receipt, applies the normal terminal output gate, and completes in
10 model calls. The same run without a committed output fails at nine calls
with `actor_steps_exhausted`; no finalizer model call or output is published.
Both cases prove source-change queue redelivery is inert. The fixture is
`HARNESS_M5_FAST=1 HARNESS_M6_FINALIZER_ONLY=1` and exits with
`M6_CONNECTED_WORKFLOW_FINALIZER_PASS`. Unit coverage also protects the
ordinary path from accepting an unapplied Ax state update. A supersession is
journaled separately from Ax's own `runtime.state.applied` acknowledgement.

**Verify:** one connected parent run uses the approved cheap route for semantic
skill selection, the server-owned strong GraphJin route for a lookup, and a
separately selected outer executor/responder route. Record actual model/profile,
stage or capability, usage coverage, latency and cost for each call without
double-counting remote GraphJin usage. Prove Ax executor escalation changes
only subsequent model calls and that GraphJin cannot be forced onto a weaker
model by caller input. Exercise real multi-route requests under rate limits and
transient failures, with no fallback around policy denial. Long mixed-tool conversations retain pending
approvals, user constraints, tool-result pairing and evidence after compaction.
For the GraphJin-inspired executor contract, deny one invocation before any
effect, return one typed receipt from a successful capability, and exhaust actor
steps both with and without sufficient saved evidence. Only the evidenced case
may use a budgeted tool-less finalizer; both outcomes must pass the same terminal
gate after crash/replay and API queue redelivery.
References survive restart and reject another tenant. Retry/compaction loops hit
ceilings; unavailable usage cannot authorize unlimited work. Measure cost per
successful task, cache traffic, repeated-output bytes and retrieval/compaction cost.
First run budget triage in shadow mode on held-out short, investigation and
Daily Lead-style artifact tasks. Compare premature budget failures, verified
completion, tokens/cost and latency against the fixed default; include misleading
short prompts, classifier failure, low confidence and restart/extension cases.
Label complexity from the work actually required to reach a verified outcome,
not from prompt length or the agent's self-assessment. Report false-low decisions
by task class and the classifier's own cost and latency. Canary the chosen profiles
after shadow evaluation, with a switch back to fixed budgets.
Enable dynamic allocation only if it improves cost per verified success without
materially increasing false-low failures; retain a fixed-budget fallback.

**Exit:** routing, budgets and context preservation pass; optimizations require
held-out quality evidence, not token savings alone.

**Dependency:** M2 and M5a. May proceed alongside M5b–d; required before M5e closes.

## M7 — Capability parity and operational qualification

**Deliver:** a versioned comparison against Hermes covering the complete capability
inventory, not just GraphJin questions. Freeze capability eligibility, schemas,
fixtures, model settings and acceptance thresholds before evaluation. Track every
entry as supported, denied by design, or deferred with a rollout exclusion.

**Verify:** run equivalent task outcomes across both backends for data, records,
documents, memory/skills, UI/clarification, workflows, integration actions, admin,
artifacts and enabled delegation. Use real browser and queue paths and separate
workflow/agent-job modes. Exercise concurrent tenants, stale permissions, reconnect,
worker/server failure, collector outage and queue saturation. Include the M4 crash
matrix for every effect class. Use held-out tasks for quality/cost comparison;
keep deterministic integration evidence separate from live-model scores.

**Exit:** all supported capability and recovery gates pass, no silent loss of
existing functionality, and agreed quality/latency/cost thresholds are met. Hosted
CI and branch PR checks are required before merge. Full parity requires all
inventory families; a narrower canary must explicitly state its exclusions.

**Dependency:** M5a–e and M6, subject to explicit canary exclusions.

## M8 — Staging, upgrade and rollback

**Deliver:** harness-owned images, deployment manifests, version pins, migrations,
separate warm-pool identities and reversible backend selection. Preserve Hermes
as a functioning fallback. OpenNeko changes stay on `feat/openneko-harness` until
review; no main changes or implicit rollout authorization.

**Verify:** staging install/upgrade, backup/restore, worker drain and rollback in
host-development and Compose topologies. Confirm approved/pending actions and
accepted inputs survive backend switching without replay. Run representative
browser and scheduled/channel tasks before and after rollback. Canary only the
qualified capability cohort; never shadow real mutations.

**Exit:** installation and rollback evidence plus successful canary thresholds.
Adopt an upstream OpenShell cancellation fix when available; that defect remains
observable and nonblocking. Publishing, merging and production rollout are separate
authorized actions.

**Dependency:** M7.

## Immediate order

1. M5a–M5e admitted-capability gate is closed with connected regressions and
   explicit exclusions in [M5 exit](M5-EXIT.md). Customer Daily Lead Union data
   remains unconnected.
2. Qualify excluded product/admin writes for a later rollout cohort only after
   their host effect boundaries and recovery rules are defined. Retain the
   accepted idle cancellation warning separately.
3. Run the full isolated OpenShell/worker/Hermes/browser gate after each grouped
   product boundary. Use focused connected gates during iteration.
4. Complete M6 routing, budget and context evidence, then M7 parity and M8
   staging/rollback. Run Daily Lead Union against real GraphJin separately
   when that source is connected.

No new TUI, generic plugin framework, speculative swarm, or wholesale rewrite of
OpenNeko's tools is required. Reuse existing capabilities and qualify their contracts.
