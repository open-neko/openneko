# General-purpose agent harness: building-block catalog

Status: design inventory, 2026-09-26. This catalogs the runtime before deciding
which capabilities to implement next. It is not a claim of full support or a
requirement to reproduce Claude Code's feature count.

An agent harness can be understood as a small operating environment for tasks:
admit a request, give a bounded agent a view of the world, let it invoke governed
capabilities, preserve its state across interruptions, and verify the outcome.
Coding is one workload. A report, investigation, customer workflow, scheduled
monitor, data pipeline or document production should use the same core contract.

## Four different boundaries

| Boundary | What it separates | Consequence |
| --- | --- | --- |
| Data/control protocol | User content, model deltas, evidence and results vs approval, steering, cancellation and configuration messages | Both may share NDJSON/HTTP, but require distinct schemas, correlation IDs and handling. This is the split described in the [Claude Code SDK chapter](https://y-agent.github.io/inside-claude-code/15-agent-sdk-structured-io.html). |
| Trusted host/execution | Identity, policy, budgets, secrets and durable intent vs model-driven work and sandbox processes | An Ax tool call or prompt cannot grant its own authority. OpenShell and the broker enforce the execution side. |
| Control/data volume | Compact plans, references and receipts vs bulk records, files and outputs | Keep large data in scoped runtime values or artifacts; pass bounded evidence to models. |
| Core/consumer | General task semantics vs OpenNeko routes, GraphJin, product approvals and presentation | Harness remains an independent Go repo. OpenNeko is one adapter and GraphJin is one capability. |

These are logical boundaries, not a demand for four services or four network
protocols. A single process may implement several, provided ownership remains
clear.

## Inventory

`Slice` means a checked-in, locally qualified narrow path. `Ax candidate`
means Ax Go has a relevant primitive, but the harness must still integrate and
test it. `Open` means no general implementation is qualified. A row can have
more than one status. The [stocktake](STOCKTAKE.md) and
[milestones](MILESTONES.md) carry exact acceptance evidence.

| ID | Building block | Purpose and boundary | Owner / current state |
| --- | --- | --- | --- |
| 01 | Entry adapters | Admit chat, API, schedule, webhook and remote events into one versioned run contract. Authenticate before mapping input. | Consumer adapters; browser/queue `Slice`, other inputs `Open` |
| 02 | Data/control protocol | Separate conversation and streaming output from permission requests, user input, steering, cancellation and configuration. | Harness protocol + adapter; headless events `Slice`, bidirectional controls `Open` |
| 03 | Identity and scope | Bind tenant, actor, run, channel, tool eligibility and data residency before model execution. | Trusted host; OpenNeko scope `Slice` |
| 04 | Run scheduler | Own run admission, queueing, deadlines, fair capacity and one active state reducer per run. | Host/supervisor; bounded run `Slice`, general scheduler `Open` |
| 05 | Lifecycle state machine | Represent queued, running, waiting, completed, failed, cancelled and unknown-effect states with legal transitions. | Supervisor; narrow recovery `Slice` |
| 06 | Model and transcript protocol | Normalize provider messages, tool calls, results, streams and stop reasons; preserve one terminal result per committed call. | Ax + supervisor invariant; `Slice` |
| 07 | Model selection | Select approved logical profiles, route/fallback among eligible providers and record the actual model used. | Ax candidate + trusted route policy; `Open` |
| 08 | Agent reasoning loop | Distill, execute, respond, call tools, clarify and finish with typed output without a duplicate outer planner. | AxAgent; bounded `Slice` |
| 09 | Task contract and plan | Capture requested outcome, constraints, dependencies, evidence and success criteria; support optional read-only planning. | Ax reasoning + supervisor record; `Open` |
| 10 | Flow orchestration | Run an application-owned dependency graph when ordering, joins or parallel independent steps are explicit. | AxFlow candidate; `Open` |
| 11 | Child agents | Delegate a scoped task with separate conversation, narrower tools, shared budgets and parent cancellation; join concise results. | Ax `AddChildAgent` candidate + supervisor governance; `Open` |
| 12 | Human interaction | Ask for clarification or approval, suspend durably, correlate a reply and resume without redoing effects. | Host/control protocol; approval `Slice`, general clarification `Open` |
| 13 | Prompt assembly | Combine stable instructions, task context, admitted tools, current state and user preferences in deterministic priority order. | Ax + harness/adapter; narrow prompt `Slice`, general composer `Open` |
| 14 | Context and working memory | Keep large inputs outside the prompt; expose compact references, recent actions, live state and pressure-aware compaction. | Ax context/runtime + artifact store; `Open` beyond bounded inputs |
| 15 | Persistent memory | Recall durable knowledge with provenance, access scope and freshness; never promote memory to authority. | Ax recall candidate + consumer store; `Open` |
| 16 | Skills and instructions | Load task expertise separately from tool permissions, with version, provenance and budget. | Ax skills candidate + consumer catalog; `Open` |
| 17 | Lifecycle hooks | At safe boundaries update plan/context/guidance, gate work, or observe progress. Persist state changes needed on resume. | Ax steering/state APIs + supervisor checkpoints; telemetry `Slice`, state updates `Open` |
| 18 | Capability registry | Bind exact tool name, schema, effect class, version, transport and eligibility for one run; defer large schemas without expanding rights. | Supervisor; local native/MCP catalog `Slice` |
| 19 | Tool transports | Adapt native Go callbacks, MCP, direct services, process tools and future UI tools to the same governed invocation. | Transport adapters; native/direct/MCP narrow `Slice` |
| 20 | Tool invocation pipeline | Parse complete call, validate schema and semantics, authorize, persist intent, dispatch, bound result and return a paired model-visible result. | Supervisor; narrow `Slice` |
| 21 | Tool scheduling | Parallelize proven independent reads; serialize conflicting mutations and coordinate shared resources across runs. | Supervisor; design rule, full acceptance `Open` |
| 22 | Permission and approval | Decide whether a specific invocation is allowed; bind approval to exact arguments and recheck before effect. | Trusted host; proposal/effect `Slice` |
| 23 | Sandbox and broker | Limit process, filesystem, network and credential reach independently of permission policy. | OpenShell + broker; qualified transport `Slice` |
| 24 | Secret and credential lifecycle | Issue scoped temporary access, replace placeholders, rotate or revoke it and prevent prompt/telemetry disclosure. | Host/broker/OpenShell; qualified paths `Slice` |
| 25 | Effect identity and reconciliation | Record operation ID before external dispatch; reuse receipts, query provider status or report unknown rather than replay ambiguously. | Supervisor + adapter; narrow `Slice` |
| 26 | Processes and background work | Start, monitor, cancel and reap long-running work without holding a model turn open; report progress through durable continuation. | Host/worker + sandbox; controlled batch fixture `Slice`, general continuation `Open` |
| 27 | Artifact/data workspace | Store uploads, intermediate files and final artifacts with scoped handles, retention and authorized download; avoid flooding context. | Host/artifact store; local batch fixture `Slice`, connected publication `Open` |
| 28 | Verification gate | Compare actual receipts, files, schemas, counts or external state with the task contract before reporting success. | Supervisor + task-specific verifier; `Open` as general gate |
| 29 | Durable journal and checkpoints | Persist accepted input, state changes, intent, result and unresolved effects; snapshots accelerate recovery, not define truth. | Supervisor/session store; narrow `Slice` |
| 30 | Resume and cancellation | Reconstruct from durable evidence, recheck authority, carry remaining budgets, stop children/processes and fence late results. | Supervisor + Ax control + host; narrow `Slice` |
| 31 | Observability | Correlate run, model, tool, child, broker and artifact spans; report usage coverage and telemetry loss without leaking content. | Ax hooks + neutral harness events; narrow `Slice` |
| 32 | Evaluation and optimization | Replay fixtures, compare task outcomes/quality/cost, validate learned guidance and promote versions deliberately. | Harness eval pipeline + Ax optimization candidate; `Open` |
| 33 | User surfaces | Render the same event/control protocol in web, CLI, SDK or other channels; present approvals, progress, evidence and artifacts. | Consumer/frontends; OpenNeko web `Slice`, generic SDK/TUI `Open` |
| 34 | Configuration and extension packaging | Version models, tools, instructions and adapters; expose supported extension points without dynamic loading until needed. | Host + static Go adapters; `Slice` for static wiring, plugin loader `Open` |
| 35 | Resource budgets | Reserve and account for model calls, tokens, wall time, tool effects, children, storage and background work across the whole run. | Supervisor + Ax usage signals; local limits `Slice`, aggregate enforcement `Open` |
| 36 | Evidence and provenance | Track where a claim, retrieved record or artifact came from, its freshness and which result supports it. | Tools + scoped receipts/handles; GraphJin evidence `Slice`, general contract `Open` |
| 37 | Multimodal perception and action | Admit text, files, images, audio or UI observations and expose computer/browser actions only as governed capabilities. | Ax/provider and tool adapters; general path `Open` |
| 38 | Notifications and wakeups | Deliver progress, attention requests and terminal outcomes; accept authenticated external events without starting duplicate work. | Consumer channels + event adapter; narrow web/queue `Slice`, general subscriptions `Open` |

## Task path

```text
authenticated input/event
  → admit scope and budgets
  → assemble task context and eligible capabilities
  → AxAgent [optional plan → reason → tool/child/flow work]
  → governed dispatch [validate → policy → durable intent → sandbox → receipt]
  → update working state and continue, or wait durably for human/external work
  → verify output and artifacts
  → durable terminal result + data/control projections
```

This is an ownership diagram, not a requirement for every task to visit every
block. A simple answer should not pay for a planner, child agent, workflow graph
or sandbox process. A consequential task may need all of them.

## What to reuse and what to build

- **Reuse Ax Go** for provider adapters/routing, typed generation, the agent
  loop, child-agent composition, owned flows, context/runtime facilities and
  supported steering/telemetry. Verify behavior against the selected Go version,
  not TypeScript documentation alone. See [Ax agents](https://axllm.dev/go/subsystems/agent/),
  [flows](https://axllm.dev/go/subsystems/flow/) and
  [telemetry](https://axllm.dev/go/concepts/telemetry/).
- **Build once in the supervisor**: identity-bound admission, policy, exact
  operation receipts, recovery, budgets, lifecycle state updates, artifact and
  completion checks. These must work regardless of model, tool transport or
  consumer. An Ax state snapshot is not an external-effect journal.
- **Keep domain behavior at the edge**: OpenNeko capabilities, GraphJin's
  server-side agent, records, workflows, browser presentation and business
  verification belong in adapters or services with explicit contracts.

The [Claude Code series](https://y-agent.github.io/inside-claude-code/) informed
this taxonomy through its chapters on the [agent loop](https://y-agent.github.io/inside-claude-code/02-agent-loop-query-engine.html),
[SDK protocol](https://y-agent.github.io/inside-claude-code/15-agent-sdk-structured-io.html),
[delegation](https://y-agent.github.io/inside-claude-code/07-multi-agent-orchestration.html),
[plan mode](https://y-agent.github.io/inside-claude-code/19-plan-mode.html),
[context](https://y-agent.github.io/inside-claude-code/03-prompt-assembly.html),
[hooks](https://y-agent.github.io/inside-claude-code/11-hooks-lifecycle.html),
[tools](https://y-agent.github.io/inside-claude-code/05-tool-system.html),
[safety](https://y-agent.github.io/inside-claude-code/06-safety-sandbox.html),
[skills](https://y-agent.github.io/inside-claude-code/12-skills-system.html),
[extensions](https://y-agent.github.io/inside-claude-code/13-plugin-architecture.html)
and [remote runtime](https://y-agent.github.io/inside-claude-code/16-remote-runtime-bridge.html).
Its source is one product snapshot, so mechanisms here are design inputs rather
than claims about our implementation or mandatory copies of its internals.
