# OpenNeko Architecture

A map of the moving parts — services, databases, the agent runtime, and how they connect.

## Component map

OpenNeko is self-hosted. Everything below except the LLM providers runs inside the operator's own infrastructure.

```mermaid
flowchart LR
  subgraph SelfHosted["Operator infrastructure (self-hosted)"]
    direction LR
    subgraph Operator["Operator browser"]
      UI[Next.js UI]
    end

    subgraph NekoServices["OpenNeko services"]
      Web["web<br/>Next.js (apps/web)<br/>:3000"]
      Worker["worker<br/>Node (apps/worker)<br/>:4100 admin"]
      NekoGJ["neko-graphjin<br/>:8089<br/>(subscriptions, MCP off)"]
    end

    subgraph NekoData["OpenNeko app DB"]
      NekoDB[(neko-db<br/>Postgres + pgvector)]
    end

    subgraph DataSource["Data source (operator's existing system)"]
      DSGJ["data-source graphjin<br/>:8080<br/>(GraphQL + MCP, read-only)"]
      DSDB[(Operational Postgres<br/>CRM / billing / warehouse<br/>e.g. AdventureWorks sim)]
    end

    subgraph AgentRuntime["Agent runtime (spawned by worker)"]
      Hermes["hermes<br/>subprocess via ACP"]
      ClaudeAgent["claude-agent<br/>in-process<br/>@anthropic-ai/claude-agent-sdk"]
    end

    subgraph PluginRuntime["Plugin runtime (spawned by worker)"]
      PluginVm["plugin VM (×N)<br/>microsandbox microVM<br/>per installed plugin"]
    end
  end

  subgraph LLMProviders["LLM providers"]
    Anthropic[Anthropic]
    OpenAI[OpenAI / Google / others via Ax]
    Ollama["Ollama<br/>(local — same host or LAN)"]
  end

  subgraph PluginRemotes["Declared plugin remotes"]
    PluginApi["e.g. api.parallel.ai<br/>(allowlist from manifest)"]
  end

  UI <-->|HTTP| Web
  Web -->|pg-boss enqueue| NekoDB
  Web -->|Drizzle| NekoDB
  Worker -->|Drizzle + pg-boss| NekoDB
  Worker -->|GraphQL subscribe| NekoGJ
  NekoGJ -->|polled SELECTs| NekoDB

  Worker -->|spawn| Hermes
  Worker -->|in-proc call| ClaudeAgent
  Worker -->|spawn microVM + JSON-RPC over stdio| PluginVm

  Hermes -->|Ax → HTTPS| OpenAI
  Hermes -->|Ax → HTTPS| Anthropic
  Hermes -->|Ax → HTTP| Ollama
  ClaudeAgent -->|HTTPS| Anthropic

  Hermes -->|shell: graphjin cli| DSGJ
  ClaudeAgent -->|Bash: graphjin cli| DSGJ
  DSGJ -->|SQL| DSDB

  PluginVm -->|HTTPS, hosts limited to manifest| PluginApi
```

The only traffic that ever leaves the operator's perimeter is the prompt/response call to a hosted LLM provider (Anthropic / OpenAI / Google / etc.) and any host declared in an installed plugin's manifest. **With Ollama as the provider and no plugins installed, nothing leaves at all** — model inference runs on the operator's own host or LAN, and OpenNeko is end-to-end local. Plugin VMs are themselves operator infrastructure but are isolated from the worker process: each runs in a microsandbox microVM whose outbound network is constrained to the hosts the plugin's manifest declared at install time. All databases, queues, and agent processes are local in every configuration. Local dev runs the whole thing in Docker Compose: `compose.yml` brings up `neko-db`, `neko-graphjin`, `web`, `worker`; `compose.adventureworks.yml` adds the data-source graphjin + a simulated AdventureWorks operational DB.

## The two-database boundary

Both databases are operator-owned and on operator hardware; the boundary isn't a trust boundary, it's a *responsibility* boundary. OpenNeko owns the schema of one and treats the other as an opaque read-only system of record.

| | OpenNeko app DB (`neko-db`) | Data-source DB |
|---|---|---|
| Schema authored by | OpenNeko — `db/migrations/*.sql` | The operator (their CRM / billing / warehouse) |
| Schema modeled in code | Yes — `packages/db/src/schema.ts` (Drizzle) | No — discovered at runtime by GraphJin |
| Worker write path | Drizzle (typed) + `pg-boss` | None — worker never connects |
| Worker read path | Drizzle, plus `neko-graphjin` :8089 for subscriptions | Indirect, only via the agent |
| Agent access | Forbidden | `graphjin cli execute_graphql` only, read-only, no raw SQL/HTTP, no mutations, no subscriptions, blocklist applied |
| Web (Next.js) | Drizzle | None |

Two GraphJin instances exist *because they do different jobs*:

- **`neko-graphjin` :8089** — over the app DB. Subscriptions only (`subs_poll_duration: 5s`). MCP off. This is the worker's event bus: the only reason to use GraphQL against a DB we already query with Drizzle is to get *push-style deltas* on `workflow_output` without hand-rolling `LISTEN/NOTIFY` and diffs.
- **Data-source graphjin :8080** — over the operator's operational DB. Full read GraphQL + MCP, exposed to the LLM agent. Mutations and subscriptions denied at the tool gate; `password`/`token`/`secret`/`encrypted` columns blocklisted. The lockdown is there because the LLM is talking to a production system whose schema we don't model — not because the DB is "external".

## Agent runtime

The worker is the only process that spawns agents. It picks a backend per org from `agent_backend_config`:

```mermaid
flowchart LR
  Worker[worker process]

  subgraph Backends["AgentBackend interface (packages/llm/src/agent-backend.ts)"]
    H[hermes<br/>any provider via Ax]
    C[claude-agent<br/>Anthropic only]
  end

  subgraph HermesProc["hermes subprocess"]
    HermesBin[hermes binary<br/>spawned with ACP stdio]
    AxClient[Ax LLM client]
    ShellH["terminal tool"]
  end

  subgraph ClaudeProc["claude-agent (in-process)"]
    SDK["@anthropic-ai/claude-agent-sdk<br/>query()"]
    ClaudeBin[claude binary on PATH]
    ShellC["Bash tool"]
  end

  Worker -->|AgentEvent stream| Backends
  H -.implemented by.-> HermesProc
  C -.implemented by.-> ClaudeProc

  AxClient -->|HTTPS / HTTP| Provider[Provider<br/>Anthropic / OpenAI / Google / Ollama / etc.]
  SDK -->|HTTPS| Anthropic[Anthropic API]

  ShellH -->|graphjin cli| DSGJ[data-source graphjin :8080]
  ShellC -->|graphjin cli| DSGJ
```

Both backends expose the same `AgentEvent` stream (`message`, `tool_start/end`, `surface`, `artifact`, `output_emit`, `action_request_emit`, `done`). Shared code MUST consume that interface; never branch on `backend.id === "claude-agent"`. New backends drop in via capability declarations, not switch statements.

**Why two backends.** Hermes is a subprocess that works with any provider via Ax — operator choice of model, including local Ollama for a fully air-gapped setup. Claude Agent runs in-process via the SDK + a `claude` binary on PATH — locked to Anthropic but fewer moving parts and better tool fidelity. The shell tool is named accordingly (`terminal` for hermes, `Bash` for claude-agent).

**Tool gate around the data source.** The prompt and the tool gate together enforce: all data-source access is through `graphjin cli execute_graphql`; no `execute_code`, no Python, no raw HTTP. Discovery (`list_tables`, `describe_table`, etc.) is pre-served via knowledge pack files written to the agent workspace at boot.

## Plugin runtime

Plugins extend the action surface (and, in a future revision, the MCP tool surface). They are installed by the operator with `openneko plugin install <name>` against the curated [registry](https://open-neko.github.io/registry/) and listed in `openneko.plugins.json` at the repo root. Each plugin lives in npm under `@open-neko/plugin-*` (first-party) or anywhere on npm (third-party). **Every plugin runs inside a microsandbox microVM** — no exceptions, no "in-process for trusted" gradient. The rationale: AI harnesses compose tool calls unpredictably, operators are not expected to audit plugin source, and OSS has no support contract — so the platform owns blast-radius containment regardless of who shipped the plugin. On hosts where microsandbox cannot run, the plugin subsystem is disabled with a clear log line; the built-in `send_webhook` adapter remains as the unsandboxed extensibility path.

```mermaid
flowchart LR
  Worker[worker process]

  subgraph PluginLoader["plugin loader (apps/worker/src/plugins/)"]
    Reader["read openneko.plugins.json"]
    Boot["per plugin:<br/>copy bundled runner →<br/>start VM →<br/>call register()"]
  end

  subgraph PluginVm1["plugin VM (×N)<br/>microsandbox microVM"]
    Runner["/workspace/run.js<br/>(bundled by author)"]
    Handler["action handler<br/>e.g. web_search"]
  end

  Registry["openneko.plugins.json<br/>(pinned version + integrity hash +<br/>requires_network: [...])"]
  ExecutorRegistry["registerActionAdapter()<br/>packages/llm/src/workflows/action-executor.ts"]
  PolicyEngine["policy-engine.ts<br/>(operator approval flow)"]

  Worker --> PluginLoader
  PluginLoader -->|reads| Registry
  PluginLoader -->|stdio JSON-RPC<br/>method=register| PluginVm1
  PluginLoader -->|on each declared kind:<br/>registerActionAdapter| ExecutorRegistry
  ExecutorRegistry -->|action requests still go through| PolicyEngine
  PolicyEngine -->|approved → execute_action RPC| PluginVm1
  PluginVm1 -->|HTTPS to allowlisted hosts| Internet[Declared plugin remotes]
```

**Lifecycle.** One microVM per installed plugin, spawned at worker boot, stopped at SIGTERM. Park/resume is supported by `MicrosandboxRuntime` for cold-start mitigation but not used yet.

**RPC contract.** Worker invokes `node /workspace/run.js <method> <json-params>` inside the VM via `microsandbox.exec()`. One process per call — the runner reads the request, dispatches, prints a single `RpcResponse` JSON object on stdout, exits. Methods: `register` (called once at boot — plugin reports its declared action kinds), `execute_action` (per approved action request). RPC schema lives in `@open-neko/plugin-types`.

**Network policy from manifest.** Each plugin manifest entry declares `capabilities.network: [...]`. The loader translates this into a microsandbox network mode (empty list → `NetworkPolicy.none()`; non-empty → `NetworkPolicy.publicOnly()`) and applies it at VM creation. Per-host allowlist enforcement at the VM boundary is a v2 follow-up that depends on richer microsandbox `NetworkPolicy` modes; today the manifest declaration is the operator-visible contract surfaced on the registry Pages site.

**Adapter integration.** When the plugin's `register()` reports an action kind, the loader builds an `ActionAdapter` that proxies `execute_action` calls through the VM RPC, and hands it to the existing `registerActionAdapter()` from `packages/llm/src/workflows/action-executor.ts`. Approved action requests continue to flow through `packages/llm/src/workflows/policy-engine.ts` exactly as built-in adapters do — the policy engine never knows or cares whether the adapter lives in-process or behind a VM.

## Operating loop (OUDA)

Cron starts a chain; subscriptions propagate every link after. Outputs are non-mutating; action requests are the only thing that touches the world.

```mermaid
sequenceDiagram
  participant Cron as workflow_cron_sweep
  participant Queue as pg-boss (neko-db)
  participant Worker
  participant Agent as agent backend
  participant GJ as data-source graphjin :8080
  participant DB as neko-db
  participant SM as subscription-manager
  participant NGJ as neko-graphjin :8089

  Cron->>Queue: enqueue workflow_run_fire
  Queue->>Worker: deliver job
  Worker->>Agent: run workflow turn (prompt + tools)
  Agent->>GJ: graphjin cli execute_graphql (read-only)
  GJ-->>Agent: rows
  Agent-->>Worker: AgentEvent stream (message, tool_*, surface, output_emit, action_request_emit)
  Worker->>DB: write workflow_run, workflow_output, observation, action_request

  Note over SM,NGJ: parallel, always on
  SM->>NGJ: GraphQL subscription on workflow_output filter
  NGJ->>DB: polled SELECT (5s)
  NGJ-->>SM: delta
  SM->>Queue: enqueue downstream workflow_run_fire (if filter matches)

  Note over Worker,DB: action_execute path is separate
  Worker->>DB: read action_request + policy decision
  Worker->>Agent: action_execute (constrained tools)
  Agent-->>Worker: receipt
  Worker->>DB: write action receipt + observation
```

Worker job handlers in `apps/worker/src/jobs/`: `workflow-run-fire`, `action-execute`, `workflow-cron-sweep`, `workflow-output-ttl-sweep`, `business-profile-build`, `industry-insights-build`, `bootstrap-metrics-build`, `metric-refresh`, `work-run`.

## Memory and embeddings

Memory writes/reads are agent-driven (the agent calls explicit `memory_save` / `memory_search` tools). The embedding model is **in-process** — no external API.

- Model: `Xenova/all-MiniLM-L6-v2` (quantized, 384-dim) via `@huggingface/transformers` (`packages/llm/src/embedding.ts`).
- Storage: `work_memory.embedding` as `vector(384)` in `neko-db` (the pgvector image is used precisely for this).
- Retrieval: raw SQL with the `<=>` operator; Drizzle just declares the column type.
- Cache: model files baked into the image under `/app/.transformers-cache`; first cold load falls back to HF Hub.

## Process layout

| Process | Source | Talks to | Notes |
|---|---|---|---|
| `web` | `apps/web` (Next.js) | `neko-db` (Drizzle, pg-boss enqueue), `worker:4100` (admin) | Operator UI + API routes. Never reads the data-source DB. |
| `worker` | `apps/worker` (Node) | `neko-db` (Drizzle + pg-boss consumer), `neko-graphjin:8089` (subscriptions), agent backends (spawn/in-proc) | Owns all writes triggered by the loop. Hosts subscription manager. |
| `neko-graphjin` | `dosco/graphjin` + `db/graphjin/neko.yml` | `neko-db` | Subscriptions only. Internal CORS. MCP disabled. |
| data-source graphjin | `dosco/graphjin` + `db/graphjin/dev.yml` | Operational DB | Agent's read surface. GraphQL + MCP. Read-only via tool gate. |
| `neko-db` | `pgvector/pgvector:pg16` | — | App state, jobs, memory vectors. |
| Agent (hermes \| claude-agent) | spawned by `worker` | LLM provider, data-source graphjin via `graphjin cli` | Never opens a DB connection directly. |
| plugin VM (per installed plugin) | spawned by `worker` via `MicrosandboxRuntime` | Hosts declared in the plugin manifest's `capabilities.network` (e.g. `api.parallel.ai`) | One microsandbox microVM per plugin. Talks to the worker via JSON-RPC over stdio. Never opens a DB connection. Disabled on unsupported hosts (Windows, Linux without KVM, macOS x86_64). |

## Where to look next

- DB schema (Drizzle): `packages/db/src/schema.ts`, migrations in `db/migrations/`
- Agent backends and event stream: `packages/llm/src/agent-backend.ts`, `packages/llm/src/agent-backends/`
- Subscription manager: `packages/llm/src/workflows/subscription-manager.ts`
- Worker jobs: `apps/worker/src/jobs/`
- Plugin runtime + loader: `apps/worker/src/plugins/microsandbox-runtime.ts`, `apps/worker/src/plugins/load-plugins.ts`
- Plugin RPC schema: `@open-neko/plugin-types` ([source](https://github.com/open-neko/plugins/tree/main/packages/types))
- Plugin registry: [github.com/open-neko/registry](https://github.com/open-neko/registry) (Pages site at [open-neko.github.io/registry](https://open-neko.github.io/registry/))
- GraphJin configs: `db/graphjin/neko.yml` (app DB), `db/graphjin/dev.example.yml` (data source)
- Compose topology: `compose.yml`, `compose.adventureworks.yml`
