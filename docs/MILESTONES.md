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
M5b and full M5c acceptance need the feature worker, harness image and matching
OpenShell gateway; demo health alone does not qualify them.
Hermes remains the default until rollout.

## Discovery that changes the plan

OpenNeko does not supply one fixed Hermes prompt. `buildWorkPrompt` assembles
sections from run capabilities, data surface, channel, installed skills, operator
context and conversation. Tool schemas/descriptions supply additional instructions.
`agent-core.ts` selects logical MCP servers; the Hermes adapter mounts a trusted
multiplexed stdio bridge. Workflow/agent-job modes have additional or narrower
surfaces. Native file/terminal/delegation capabilities are separate from MCP.

The current Harness backend still advertises `mcpTools: false` because the local
Go MCP adapter is not yet connected to OpenNeko's bridge. The product prompt now
defers tool contracts to the run's admitted Go catalog and lists only held pack
action candidates. It does not claim that all Hermes MCP capabilities are present.

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
| M3: First consumer slice | Browser/queue → Harness → broker → GraphJin; scoped evidence, accepted-input identity and output | [Consumer acceptance](../integration/m3/README.md) |
| M4: Durable governed operations | Receipt recovery, ownership, approval continuity, claimed effects, crash reconciliation and honest unknown outcomes | [Recovery and crash matrix](M4-RECOVERY.md) |

M4 covers the implemented lookup/proposal/effect paths, not every tool in the
inventory. New tools inherit its rules and need their own acceptance evidence.
Durable receipts do not imply exactly-once external effects. Ax resume remains a
bounded new attempt from evidence, not restoration of a JavaScript execution stack.

## M5a — Shared capability catalog and invocation boundary

**Local status (2026-09-26):** the Go catalog now validates names, JSON schemas,
origin/effect metadata, results and collisions; dispatches native, direct and local
MCP tools through the same Ax callback and durable operation journal; pins a
catalog hash on new checkpoints; retains old lookup/proposal checkpoint decoding;
and emits tool origin, effect and duration. The official Go MCP SDK is pinned at
v1.8.0 with Go 1.25. An Ax run exercised native + MCP fixture calls and terminal
replay. Only read-classified MCP tools are admitted until remote effect handlers
are qualified. The OpenNeko bridge remains unwired, so this is local M5a evidence,
not connected product acceptance.

**Deliver:** a run-scoped catalog and dispatch path for native Go, MCP and direct
service tools. Reuse the existing operation journal, Ax callback bridge, policy and
result validation. Each admitted entry binds a stable identity, schema/version,
trusted origin, eligibility, limits, effect classification and recovery policy.
MCP annotations and descriptions are untrusted hints, not permission decisions.
Unknown tools are not presumed read-only, concurrent-safe or retryable.

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

**Schedule:** parked until an OpenNeko/GraphJin instance is connected. The batch
design and deterministic fixture work can be prepared without one, but the real
script → authorized GraphJin → file → browser acceptance below cannot be claimed
from a simulated endpoint.

**Deliver:** connect the Go runtime to OpenNeko's existing logical servers through
its trusted bridge. Reuse protocol support available in the pinned Ax Go stack or
a maintained Go MCP implementation; do not invent JSON-RPC framing. Qualify the
actual bridge's stdio lifecycle first; support remote MCP transport only when an
installed capability needs it. Launch commands/endpoints come from trusted host
configuration, never model arguments.

Support discovery (including pagination), schema mapping, bounded structured/text
results, tool errors, deadlines, progress and cancellation for the exercised
servers. Explicitly reject unsupported content/protocol features. Keep raw results
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
Workflow, rule, records, memory, skill, plugin and admin writes are not enabled.
Local Go and OpenNeko checks pass; connected browser/queue acceptance must be
repeated against the updated image when an instance is available.

Source inventory for the next M5c slice:

| Handler | Current effect boundary | Required harness qualification |
| --- | --- | --- |
| `neko_pack_actions` / `neko_plugin_actions` in `work/tools.ts` | Policy evaluation, action request, optional enqueue and wait | Pack proposal uses M4 approval/claim path; plugin auto mode needs a separately qualified queue/effect receipt before admission |
| `neko_workflow_builder` in `workflows/builder-server.ts` | `saveWorkflowWithTrigger` or destructive `deleteWorkflow`, then confirmation card | Journal at the host write, bind exact workflow identity and confirmation, reconcile partial trigger wiring and lost card delivery |
| `neko_rule_builder` in `workflows/rule-builder-server.ts` | `upsertActionPolicyByName`, then confirmation card | Bind policy revision and actor; prove retry cannot silently overwrite a changed rule |
| `neko_memory` / `neko_skills` in `work/tools.ts` | Durable memory write or sandbox file write | Add host idempotency or exact file-version guard, then prove crash and duplicate-delivery behavior |
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

**Deliver:** Read, Edit and shell/process tools under OpenShell, file read-version
checks, read-parallel/write-exclusive scheduling, output/process limits and scoped
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

**Deliver:** child agents using the same Go/Ax engine with narrowed capabilities,
explicit context, child ownership and shared parent budgets. OpenNeko decides
whether delegation is enabled. No named-agent framework, swarm scheduler or new
background-work system. Children cannot expand authority, bypass approval or
retain execution after parent cancellation.

**Verify:** a real parent task delegates two bounded investigations, combines
verified results and exposes correlated progress. Exercise child failure, parent
cancellation, capability escalation attempts, spawn/depth limits, crash recovery
and usage rollups without double counting. Test the disabled-delegation mode too.

**Exit:** existing delegation use cases have verified equivalents or are explicitly
excluded from the rollout cohort. Do not claim full Hermes parity while they are excluded.

**Dependency:** M5a–d and M6 shared budget enforcement.

## M6 — Ax routing, context and efficiency

**Deliver:** approved Ax model profiles and fallback; aggregate limits across model
stages, tools, retries and child/remote work; context compaction preserving original
intent and unresolved operations. Qualify two approved OpenShell-bound routes first.
Keep durable state authoritative; prompt compaction is only a model-context projection.
Reserve budget before each new model or remote call. Check observed usage on every
event, but do not rely on a provider's final cumulative snapshot to halt a run;
missing usage requires a conservative admission limit.

Persist large observations with scoped retrievable references and bounded excerpts.
Evaluate the [SoL-Pi ideas](https://arxiv.org/html/2609.20519v1) individually: observation references first; cache-aware
compaction next; edit-and-verify and cheaper-model log reduction only when measured
benefit justifies them. Include summarization/retrieval costs and deterministic
validation/fallback. No autonomous production harness evolution.

**Verify:** real multi-route requests under rate limits and transient failures,
with no fallback around policy denial. Long mixed-tool conversations retain pending
approvals, user constraints, tool-result pairing and evidence after compaction.
References survive restart and reject another tenant. Retry/compaction loops hit
ceilings; unavailable usage cannot authorize unlimited work. Measure cost per
successful task, cache traffic, repeated-output bytes and retrieval/compaction cost.

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
