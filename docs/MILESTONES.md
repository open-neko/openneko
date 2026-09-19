# Go harness implementation milestones

Status: M1 execution-contract and M3 read-only consumer gates have local evidence.
M2 credential lifecycle, gateway restart and OTLP delivery now pass, but upstream
idle-stream cancellation remains open. M4 terminal recovery is implemented;
distributed PostgreSQL ownership/receipts have live crash evidence; operation
recovery and governed effects remain in progress. Broker lookup intent/result journals
now have live crash evidence. M5–M8 are not qualified.
Implementation is on `feat/openneko-harness` in Harness and the optional OpenNeko
integration worktree; no further changes are being made on main.

Companions: [design](DESIGN.md) and [OpenShell qualification](OPENSHELL.md).

Each milestone ends with a runnable acceptance check and retained evidence: exact dependency/image revisions, command, result and a redacted trace or journal where relevant. Commands and test locations become concrete when the Go module is created; this plan does not claim those commands exist today. Use deterministic model fixtures and synthetic credentials for correctness tests, and separately label live-model evaluations.

Telemetry is part of every milestone. Add observations with the behavior they describe, export neutral telemetry and map it to OpenNeko through its adapter, and never put credentials or raw customer content into default traces. No new UI or independent orchestration framework is required.

The standalone suite must build and run without an OpenNeko checkout. Consumer
worker/web checks qualify the optional adapter and remain required for OpenNeko
acceptance; they are not prerequisites for building the core. Prefer existing product contracts; propose a small product integration change when
it is demonstrably simpler than retaining an adapter workaround.

## Progressive integration gates

Unit tests alone cannot complete a milestone. Grow one executable integration suite with the product, retaining earlier scenarios as regression checks. Use an isolated OpenNeko test deployment with real database/queue, worker, broker, OpenShell gateway and sandbox processes. From M3 onward, drive the existing web app in a real browser through its normal authenticated APIs and event transport; do not substitute direct harness calls for browser acceptance.

Deterministic provider endpoints may replace the external model for repeatable failure injection, but requests must traverse Ax and actual OpenShell credential/policy enforcement. Use a real GraphJin server and seeded database for delegation checks. Clearly distinguish these runs from small, budgeted live-provider smoke tests. A mocked broker, worker or sandbox is useful for unit tests but does not satisfy the corresponding integration gate.

| Milestone | Required integrated path | Observable acceptance |
| --- | --- | --- |
| M1 | Go/Ax → controlled HTTP model endpoint → governed callbacks | Actual streaming transport and callback execution agree with recorded lifecycle events; cancellation closes the request |
| M2 | Real worker job → existing launcher → OpenShell → Go/Ax → controlled provider; sandbox → broker | Queue job launches the correct image, credentials resolve only at allowed destinations, events return to the worker, and cancellation/cleanup leave no running child job |
| M3 | Browser → web/API → queue → worker → OpenShell harness → broker → GraphJin → seeded database → browser | User submits a question, sees progress and a verified answer; reload/reconnect preserves one run and its result; browser cancellation reaches execution |
| M4 | Same browser path with real worker/broker/sandbox failures and controlled external effects | Approval survives reload and worker restart; repeated delivery does not duplicate the effect; ambiguous outcomes appear honestly in the UI |
| M5 | Browser task → sandbox filesystem/processes → artifact storage → browser download | Downloaded artifact matches validated bytes; cancel stops its process tree; another user cannot access the artifact |
| M6 | Browser conversation → worker → Ax → two OpenShell-bound provider routes | Injected failure switches to an approved route, context survives a long conversation, limits stop work, and the displayed terminal state matches persisted state |
| M7 | Concurrent browser sessions across test tenants and the complete deployed stack | Isolation, reconnect, overload and telemetry-outage scenarios pass alongside measured task quality |
| M8 | Packaged staging deployment → browser workflows → rollback → same workflows | Install/upgrade, restart and rollback preserve the verified product path in both supported deployment topologies |

For every integrated scenario, check three things together: what the browser/API reports, what the durable run/operation records contain, and what the worker/sandbox or controlled upstream actually executed. Retain correlated run/attempt/operation/sandbox IDs, redacted logs/traces and browser evidence for failures. A green UI with a failed or still-running process is a failed test; a successful backend with a broken user flow also fails.

Run focused deterministic checks on each change and the cumulative integration suite before closing each milestone. Run live-provider smoke tests when provider/transport contracts change and before release; do not make nondeterministic model wording an exact-match assertion. Reuse existing test runners and deployment scripts where possible. Test setup owns only its isolated services and data; it must not restart the user's active development stack.

## M1 — Prove Ax's execution contract

**Implementation evidence:** [module and findings](../README.md). Ax is pinned to `5c43344f9ef3`. Real HTTP and Goja tests cover native AxGen tool pairing, actual AxAgent actor callbacks, cancellation, incomplete streamed calls, run-scoped spans, missing usage, CPU timeouts and Agent-level JSON snapshot restoration. The checks exposed loss of run context in Ax's synchronous `ContextHandler` path; a small run-bound adapter preserves cancellation and native-tool spans. The verified snapshot boundary is completed-step JSON data, not arbitrary crash recovery. Standalone CI wiring and a core-to-adapter dependency check are present; a hosted CI run has not occurred.

**Deliver:** a small Go compatibility program against a pinned Ax revision. Exercise AxAgent's stages, streaming, native tools and actor callbacks. Establish which hooks permit authorization before dispatch and durable recording around each operation. Record the supported checkpoint boundary and gaps requiring an upstream change.

**Verify:** deterministic model fixtures produce a normal answer, two external calls inside one actor step, a denied call, a malformed call, an interrupted stream and cancellation during a callback. Assert every external invocation reaches our boundary; denied work never executes; committed calls receive terminal results; partial calls never dispatch. Inspect state export/restore, including omitted functions or truncated runtime values. Correlate model/stage/tool events and mark absent usage explicitly.

**Exit gate:** a repeatable test demonstrates governed execution on both call paths. If Ax cannot expose the required boundary, resolve the integration or upstream change before expanding the harness. Record actual supported resume semantics instead of promising arbitrary VM recovery.

## M2 — Qualify OpenShell 0.0.116 with Go/Ax

**Latest gate:** HTTPS/Go CA trust and local Ax cancellation pass. Upstream idle
stream cancellation through 0.0.116 fails the ten-second observation limit while
the direct HTTPS control passes. The extended live suite exits nonzero; M2 stays
open. See [the integration record](../integration/README.md).

**Partial implementation evidence:** [isolated OpenShell suite](../integration/README.md) passes mTLS lifecycle, actual Ax HTTP streaming through the proxy, synthetic credential replacement, destination-path rejection and unauthorized-binary rejection. It reproduces `MainProcessExited` for the existing cold-create `/bin/sh -lc true` command; the standalone adapter translates that lifecycle without modifying the launcher. HTTPS, static rotation/detach, two bindings, managed OAuth refresh, gateway restart and actual OTLP delivery now pass. The M3 suite provides real worker/queue/broker evidence; real Hermes cold/warm/reuse regressions pass; hosted CI remains open. The test stack cleans up without changing the active gateway.

**Deliver:** an isolated gateway and minimal Go agent image with standalone lifecycle checks plus optional existing-host-launcher adapter checks. Pin the CLI/gateway/image tuple. Configure exact executable egress rules, endpoint-bound synthetic credentials and two distinct provider slots.

**Verify:** Go/Ax sends header and query authentication through the proxy to controlled endpoints, trusts the sandbox CA, streams responses and cancels bounded work. The upstream receives the expected synthetic secret while workload output and telemetry do not. Wrong destinations and unauthorized binaries are denied. Exercise managed token expiry/refresh, static-key rotation and provider detach separately. Check SSE idle periods, gateway restart, broker reachability, Docker OTLP export and the existing Hermes launch path.

**Exit gate:** transport and credential tests pass on the pinned tuple, with measured cancellation and explicit revocation behavior. Record actual deployment versions and migration requirements. This qualifies a target; it does not upgrade the user's running stack. Do not depend on the SDK's unimplemented default file-transfer transport.

**Dependency:** M1 is required for full Ax integration; gateway and synthetic transport setup can proceed independently.

## M3 — First usable GraphJin run

**M3 acceptance implemented and exercised (2026-09-19):** see the
[run protocol](RUN-PROTOCOL.md) and [acceptance record](../integration/m3/README.md).
OpenNeko's actual browser path is in-process; its channel path uses the queue
worker. Both existing paths are tested separately. M2 remains open, not waived;
M3 completion does not qualify production rollout or remote cancellation.

**Deliver:** one headless Go/Ax runtime launched by OpenNeko and usable through the existing web app, one approved model route, and read-only delegation through the existing `/v1/graphjin/agent` broker route. Keep the harness run/event/result contract neutral. Translate the existing product launch and event contracts in the optional adapter. Justify any required product integration change separately. Add stable run/attempt/operation IDs, durable input acceptance and operation records, bounded outputs/deadlines, and a typed answer/clarification/partial/failure envelope.

**Verify:** on a seeded GraphJin test dataset, a question with a known answer returns correct evidence and preserves remote refusal/error types. An unauthorized source request fails even when model arguments forge identity. Duplicate delivery of an input creates one accepted input. Cancellation closes the run and ignores late results; report whether remote work has actually stopped. Trace the run through Ax, broker and GraphJin, linking remote trace IDs and counting remote usage once. Restart after a completed read and show its stored result is retained.

**Exit gate:** the known-answer scenario and authorization/error cases pass through the real browser, web, worker, OpenShell and GraphJin path. Verify progress rendering, disconnect/reconnect, page reload, cancellation and terminal error rendering against durable state. A live-model smoke test is reported separately from deterministic correctness tests. This is the first usable internal demo; mutations and arbitrary crash resume remain disabled.

**Dependency:** M1 and M2.

## M4 — Durable recovery and governed effects

**In progress (2026-09-19):** automatic terminal reconciliation now adopts validated
Go checkpoints from the host or retained OpenShell sandbox without model/tool replay.
A PostgreSQL session lock fences concurrent launch/recovery across hosts; a
local file lock protects legacy admissions. Broker lookup operations now persist
intent/results before dispatch/delivery and reject duplicate execution after crashes.
Broker disconnects cancel the real GraphJin model request; ambiguous operation
records remain unknown without a receipt. Saved broker receipts can repair missing
checkpoint tool results without execution. The launcher now starts a bounded new
Ax attempt from repaired evidence; the live gate verifies saved lookup reuse.
A separate live host-launcher SIGKILL gate proves overlap refusal while the remote
Go process remains active and terminal adoption after it finishes, without new
model or lookup calls. A production queue-handler process is also killed after
a saved lookup: pg-boss expires and redelivers the same job, and a replacement
worker adopts the answer without duplicate messages or calls. The earlier crash
windows and approval/effect matrix remain open.
Accepted context survives changed
prompts on real queue redelivery without new model/tool calls. Legacy mutation
fences and legacy broker routes are disabled for Harness: its host-bound token
permits only the journaled lookup route. Hermes behavior has regression coverage.
Receipts are durable
before cleanup; unknown operations and changed input/scope fail closed. Hermes
retains its existing lifecycle. Host-side proposal storage now also preserves one prepared approval per runtime
operation, freezes its arguments and refuses replay after interrupted preparation.
The Go proposal tool and governed effect dispatch remain disabled. This completes
the read-only terminal recovery slice and proposal storage, not full M4. See [M4 progress](M4-RECOVERY.md) for checks and deployment limits.

**Deliver:** host-owned journal/checkpoint recovery using existing database/queue facilities; input deduplication; operation intent/result records; durable approval continuations; broker idempotency/status contracts where supported; cancellation propagation and reconciliation. Use controlled mutation fixtures before real application actions.

**Verify:** kill the worker before dispatch, after dispatch, after the external service commits but before the result is saved, and after the result is saved. Resume without duplicate accepted inputs or duplicate effects on an idempotent test service. For a service without reconciliation, surface `outcome unknown` and do not replay automatically. Restart while awaiting approval; reject changed arguments, wrong caller scope and revoked permissions. Reject incompatible/incomplete snapshots. Denial and cancellation still yield valid terminal tool results; late work cannot reopen the run. Journal failure prevents new effects.

**Exit gate:** the crash matrix passes with journal evidence of each transition. Document which operations support reconciliation and which intentionally stop on ambiguity. Transcript pairing must not be described as an exactly-once effect guarantee.

**Dependency:** M3.

## M5 — Safe local tools and verified artifacts

**Deliver:** Read, Edit and Bash through the same governed boundary; read-parallel/write-exclusive scheduling; read-version checks; bounded process/output handling; scoped artifact publication. Treat arbitrary shell commands as unsafe operations. Enforce the tested process boundary around broker capabilities before allowing model-generated subprocesses.

**Verify:** synchronized tests prove two safe reads overlap and mutations do not overlap other scheduled tool work. Change a file between read and edit: the edit is rejected and external changes survive. Exercise concurrent harness writers and an external editor; document any remaining atomicity limits. Terminate a process tree on cancellation/timeout, cap large output, reject path escapes and verify artifact size/type/content before completion. Attempt broker-token access from generated child code and test cross-run filesystem/capability isolation.

**Exit gate:** contention, containment and cleanup tests pass. If broker capabilities cannot be isolated from arbitrary generated code, keep that execution path disabled until the boundary is fixed. No success event precedes required artifact validation.

**Dependency:** M4; tool unit tests can begin earlier.

## M6 — Routing, context and budget controls

**Deliver:** Ax-backed approved model profiles and fallback, per-run limits across model corrections/retries/tools/GraphJin, bounded context compaction, and explicit usage coverage. Start with two qualified routes; do not add every provider at once.

**Verify:** inject rate limits, transient failures and invalid structured responses. Fallback chooses only authorized, capable routes with the correct credentials. Policy denial does not trigger a less-restricted route. Unresolved tool calls retain a valid transcript across permitted fallback or fail explicitly. Retry/correction/compaction loops hit configured ceilings. Compaction preserves instructions, active constraints, unresolved operations and required evidence references. Missing provider usage remains unknown; it cannot permit unlimited calls. Check GraphJin aggregate versus child usage for double counting.

**Exit gate:** routing/failure fixtures and context-preservation cases pass, including multi-provider OpenShell tests. Retry ownership and conservative behavior when exact usage is unavailable are explicit.

**Dependency:** M4 and the M2 multi-provider qualification; can proceed alongside M5.

## M7 — Evaluation and operational readiness

**Deliver:** a versioned task suite covering GraphJin answers/refusals, local artifacts, clarification, cancellation and recovery; comparison against the current backend; latency/cost/quality reports; telemetry health checks. Select release thresholds before running the comparison.

**Verify:** run both backends on the same seeded tasks and held-out cases. Separate deterministic checks, user/evaluator judgments and model variability. Report task success, evidence correctness, policy violations, cost per successful task, usage coverage, first useful output and completion latency. Exercise simultaneous tenants, slow consumers, collector outage and queue saturation. Trace loss is visible and bounded; mandatory persistence failure stops effects. Inspect retained observations for credential/content leakage and enforce deployment retention settings.

**Exit gate:** all security/recovery invariants pass and pre-agreed quality/latency/cost thresholds are met. Retain the exact run manifest and comparison report. Insufficient sample size or missing usage is disclosed, not presented as a proven improvement.

**Dependency:** M5 and M6. Build evaluation fixtures throughout earlier milestones.

## M8 — Staged rollout and rollback proof

**Deliver:** harness-owned image and deployment configuration with matching OpenShell version pins, deployment migration notes, separate warm-pool identities, and a reversible backend rollout using existing selection controls. Qualify backup/restore before changing a live gateway.

**Verify:** in staging, restore the old gateway state with its matching binaries, switch back to the previous backend and prove accepted runs/effects are neither lost nor duplicated. Drain old sandboxes, verify current provider/policy revisions before new runs, and test both host-development and Compose topology. Canary the new backend against M7 thresholds, then increase traffic only while gates hold. Avoid shadow execution of real mutations.

**Exit gate:** rollback rehearsal and canary evidence pass; operators can locate a failed run across application, broker and sandbox observations. Production rollout is a separate authorized implementation action, not part of this design-only work.

**Dependency:** M7.

## Delivery boundaries

- **After M2:** the technology and sandbox integration are proven enough to build upon.
- **After M3:** a usable internal GraphJin assistant path exists.
- **After M4–M6:** recovery, local work and routing have executable correctness gates.
- **After M8:** the qualified path is ready for operational use at the tested scope.

Defer subagents, general background work, a new TUI, additional provider protocols and automatic prompt/playbook evolution. Add them as separate milestones only when a concrete product task needs them, preserving the same recovery and telemetry gates.
