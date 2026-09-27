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

**Partial status (2026-09-26):** the existing M4 pack-action proposal/effect path
is narrowed to the host-filtered action kinds for each Work run. No eligible pack
actions means no proposal tool and a read-only broker token. The Go callback
rejects an unlisted action before broker dispatch; the broker still rechecks
readiness, actor entitlement, exact schema and policy, and approved effects use
the existing claim/reconciliation path. Prompt claims match this admitted slice.
The direct `memory_save` capability now binds an explicit customer Work-run grant,
validates the model's bounded text/kind/scope, and persists host operation intent
before calling OpenNeko's existing memory service. A successful connected
OpenShell/Ax turn stored one real memory row and matching host/Go receipts.
If the host commits the memory but loses its receipt, the outcome stays unknown
and later effects are fenced; no automatic redispatch is permitted. Workflow
definition and rule writes are limited to the separately qualified paths below;
records, skill, plugin and admin writes are not enabled.
Local Go and OpenNeko checks pass; connected browser/queue acceptance must be
repeated against the updated image when an instance is available.

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
its version token. This grant admits cron triggers; data-change subscriptions
and watchers need a separate recovery contract because their wiring can fail after the
definition commits. Workflow deletion remains unqualified.

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

Source inventory for the next M5c slice:

| Handler | Current effect boundary | Required harness qualification |
| --- | --- | --- |
| `neko_pack_actions` / `neko_plugin_actions` in `work/tools.ts` | Policy evaluation, action request, optional enqueue and wait | Pack proposal uses M4 approval/claim path; plugin auto mode needs a separately qualified queue/effect receipt before admission |
| `neko_workflow_builder` in `workflows/builder-server.ts` | `saveWorkflowWithTrigger` or destructive `deleteWorkflow`, then confirmation card | Cron/batch create/edit uses a direct journaled broker call with a version precondition and host-persisted card. Qualify deletion, data-change/watch trigger wiring and lost-card recovery separately. |
| `neko_rule_builder` in `workflows/rule-builder-server.ts` | `upsertActionPolicyByName`, then confirmation card | Admin Work create/edit uses a host journal, current-role check, listed revision and persisted card. Rule deletion and broader auto-execution remain separate gates. |
| `neko_memory` / `neko_skills` in `work/tools.ts` | Durable memory write or sandbox file write | Direct `memory_save` has a host receipt and unknown-outcome fence; MCP save and skill creation remain excluded. Add idempotency before any automatic retry of a lost save receipt. |
| Admin manager tools in `work/tools.ts` | `proposeAdminAction` creates internal action requests | Reuse admin approval and current-role checks; keep internal scope separate from pack actions |
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
a small artifact through the browser, not the Daily Lead Union batch. The validated
large batch artifact and general shell path remain open. External writers do not share
the Go gate; the second version check narrows but cannot eliminate their
check-to-rename race, so strict cross-process coordination needs a host lock.

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
and denied direct network access inside the process compartment. Larger files,
cancelled/failed artifact behavior and broader process limits remain to qualify
through the full product path. The separate real OpenShell process suite now
also rejects publication after a script writes an output and exits nonzero,
after a partial output is cancelled, and when a declared output exceeds the
16 MiB per-file limit.
A separate production-queue Work fixture now runs a script that writes a
partial CSV and exits nonzero. The Work run fails, emits no artifact event,
leaves no published file, and retains one unresolved host operation rather
than redispatching on retry. Product-level cancellation, larger artifact and
process-tree cases remain open.

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

**Local status (2026-09-27):** Ax's invocation-scoped rate limiter now admits a
model request only after a content-free `model.request.started` event is durably
recorded. A trusted `max_model_calls` limit (16 by default, at most 64) is
enforced across Ax attempts; resume reconstructs spent calls from the checkpoint.
A two-call fixture proved that interruption and resume do not authorize a third
request. This limits calls even when provider usage is missing, but does not yet
enforce a token or cost ceiling or cover multi-provider/child work. The same
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
incomplete after resume. General claim-to-receipt verification remains open.

**Deliver:** approved Ax model profiles and fallback; aggregate limits across model
stages, tools, retries and child/remote work; context compaction preserving original
intent and unresolved operations. Qualify two approved OpenShell-bound routes first.
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

Add a Jev-style budget triage experiment using Ax Go's existing Typesafe native
decision client. At admission, classify an approved bounded task summary plus
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

Persist large observations with scoped retrievable references and bounded excerpts.
Measure admitted-tool schema cost and selection errors. If they are material,
add search/describe over the pinned run catalog while keeping frequent tools
eager; discovery must not grant a new capability. Verify a previously discovered
tool after compaction and catalog-version change on resume.
Evaluate the [SoL-Pi ideas](https://arxiv.org/html/2609.20519v1) individually: observation references first; cache-aware
compaction next; edit-and-verify and cheaper-model log reduction only when measured
benefit justifies them. Include summarization/retrieval costs and deterministic
validation/fallback. No autonomous production harness evolution.

**Verify:** real multi-route requests under rate limits and transient failures,
with no fallback around policy denial. Long mixed-tool conversations retain pending
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

1. Finish M5a packaging and recovery checks for the local native/MCP catalog.
2. Continue M5c handler inventory and isolated governed mutation fixtures;
   retain pack proposal as the only enabled product write path.
3. Use the isolated local demo as the connected M5b target: real query-to-file
   batch, MCP reads, clarification and UI through OpenNeko/GraphJin; then
   connected M5c writes. Keep the released demo and feature integration
   evidence distinct until the worker and gateway versions match.
4. M5d/M6: local file/process safety, bounded batch output, routing, context
   and pre-call budgets. M5e adds bounded delegation after shared budgets.
5. M7/M8: parity evidence, staging and rollback.

No new TUI, generic plugin framework, speculative swarm, or wholesale rewrite of
OpenNeko's tools is required. Reuse existing capabilities and qualify their contracts.
