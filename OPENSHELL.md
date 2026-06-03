# OpenShell sandboxed-agent runtime (preview)

OpenNeko can run the **agent loop itself** — not just plugins — inside an
[NVIDIA OpenShell](https://github.com/NVIDIA/OpenShell) policy sandbox, with the
worker/web process as the only trusted control plane. This page covers the
threat model, the architecture, how to turn it on, and what's verified.

> **Status:** preview / opt-in. The default runtime is unchanged (plugins in
> microsandbox, agent in-process). On macOS the **host-process gateway** path is
> verified end-to-end via the web UI (hermes/gemini). The **containerised**
> gateway (for the one-command installer) is verified up to mTLS and is pending
> validation on the Linux target — see [Deployment](#deployment).

## Why

The least-trusted component historically ran with the most access: the
autonomous, prompt-injectable agent loop ran **in-process** on the worker/web
host, while only plugins were isolated. OpenShell flips that — the agent is
treated as untrusted code and runs in a default-deny sandbox:

- **Egress is default-deny**, allowed per `(host, binary)` — the agent can only
  reach the model endpoint (and whatever a plugin's manifest declares).
- **The model API key never enters the sandbox.** The box holds only an opaque
  `openshell:resolve:env:…` placeholder; the gateway's TLS-terminating egress
  proxy substitutes the real key on the wire. Verified: a grep of the sandbox
  env, filesystem, and config for the real key returns zero hits.
- **The control plane stays outside.** Secrets, the database, policy/approval,
  and channels live in the worker/web process; the agent reaches them only
  through a narrow, audited boundary.

## Architecture

```
 user ─▶ web/worker (CONTROL PLANE: DB, secrets, policy)
            │  runChatTurn: prologue (build prompt) ─┐
            │                                         ▼
            │                              ┌───────────────────────────┐
            │   launch + stream  ─────────▶│  OpenShell sandbox         │
            │   (events over stdout/SSE)   │   entry.ts → runAgentBackend│
            │                              │   → hermes / claude         │
            │                              └────────────┬──────────────┘
            │                                  egress (default-deny)
            │                                           ▼
            │                              ┌───────────────────────────┐
            │                              │ gateway egress proxy:      │
            │                              │  injects real key on wire  │──▶ model API
            │  ◀──────────────────────────┤  (placeholder never leaves) │
            ▼  epilogue (parse fences, persist)
```

`runChatTurn` splits into **prologue** (host: build the prompt from the DB),
**runCore** (the agent loop — in-process *or* in a sandbox), and **epilogue**
(host: parse action/workflow fences, persist, scrub). Only `runCore` moves into
the box; the trusted halves stay on the host. Both the worker (channel messages)
and the web chat route launch the sandbox through the same shared launcher
(`@neko/llm/work` `makeSandboxRunCore`).

**Backends.** Hermes streams its output and emits action/workflow fences parsed
host-side, so its sandbox needs only the model call. Claude uses MCP tools
mid-turn, so it additionally needs the host-side broker (see Status).

## Enabling it (developer / advanced)

Prerequisites: an OpenShell gateway reachable by the worker/web, the agent image
(`openneko/agent`), and the `openshell` CLI on PATH.

Set these on the **worker** (channel runs) and/or the **web** process
(interactive chat) — both honour the same flags:

| Env var | Meaning |
|---|---|
| `OPENNEKO_AGENT_RUNTIME=openshell` | Run the agent loop in a sandbox (default `inprocess`) |
| `OPENNEKO_AGENT_IMAGE` | The agent image (Dockerfile `agent` stage) |
| `OPENNEKO_AGENT_MODEL_PROVIDER` | Gateway-side provider name (auto-synced; see below) |
| `OPENNEKO_AGENT_MODEL_HOST` | Comma-separated egress hosts (e.g. `generativelanguage.googleapis.com,models.dev`) |
| `OPENNEKO_AGENT_MODEL_BINARY` | The backend's connecting binary (egress is per-binary) |
| `OPENNEKO_AGENT_MODEL_KEY_ENV` | The env var the backend reads (e.g. `GEMINI_API_KEY`) |
| `OPENSHELL_GATEWAY` / `OPENSHELL_GATEWAY_ENDPOINT` | Gateway selection (mTLS name, or endpoint) |

**Provider auto-sync.** On startup the worker turns your configured model key
(`/settings`) into a gateway-side OpenShell provider automatically — you do not
run `openshell provider create` by hand. The proxy injects that credential on
egress; the box only ever sees the placeholder.

**Connecting binary (gotcha).** Egress is matched on the *resolved* executable
path. For hermes/gemini that's the uv-managed Python
(`…/uv/python/cpython-*/bin/python3.11`), not the `hermes` launcher; for claude
it's the resolved `claude.exe` (a native binary), not the `/usr/local/bin/claude`
symlink. `OPENNEKO_AGENT_MODEL_BINARY` must be the resolved path.

## Plugins

Plugins can run in OpenShell sandboxes instead of microsandbox with
`OPENNEKO_PLUGIN_RUNTIME=openshell` (default `microsandbox`). Both sit behind one
`PluginRuntime` interface, so it's a single flag — see
[PLUGINS.md](PLUGINS.md).

## Deployment

- **Host-process gateway** (brew on macOS / binary + systemd on Linux) — the
  verified path; the agent-in-sandbox web-UI e2e runs on this.
- **Containerised gateway** (`compose.openshell.yml`) — for the one-command
  installer. Requires the Docker socket, mTLS PKI, and matched host:container
  bind paths; verified up to mTLS, **pending end-to-end validation on Linux**
  (macOS/OrbStack has host-path + port-advertising quirks). Validate on the
  Linux target before relying on it.

## Verifying key isolation

```sh
# Inside a running agent sandbox — the real key must be ABSENT:
env | grep -c "<your-key-prefix>"          # → 0
grep -rl "<your-key-prefix>" /sandbox /tmp # → (nothing)
echo "$api_key"                            # → openshell:resolve:env:v…_api_key
```

A 200/real response from the model with the box holding only the placeholder
confirms the proxy injected the key on the wire — it never entered the sandbox.
