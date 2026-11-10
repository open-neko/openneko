# Agent runtime

OpenNeko treats the agent loop as untrusted code: a prompt can steer it. Three
pieces run it, and each holds only what its job needs:

- the **runner**, a short-lived Node process that web or the worker starts for
  one run;
- **`openneko serve`**, which runs each agent turn: the Ax harness, the model
  calls and the agent's file tools;
- the optional **sandbox service** ([recipes/sandbox](recipes/sandbox/README.md)),
  which runs terminal commands, script skills and workflow script steps.

Web and the worker are the trusted control plane. The database, secrets,
policy and approvals live there. The agent reaches them only through the
per-run broker.

## How a run flows

```
 user ─▶ web / worker  (control plane: DB, secrets, policy, broker)
            │ stage the run's workspace in the shared runs folder
            │ start the runner: node /opt/openneko-agent/entry.js
            ▼
        runner  ── one turn per connection ──▶  openneko serve
        (child process,                          (openneko-serve container,
         broker token, model key                  model key only for the turn)
         for this run)                                 │ terminal commands
            ▲                                          ▼ (only with a sandbox URL)
            │ MCP tools over the serve socket     sandbox service
            └──────── broker (loopback) ◀──       (own network, per-run user,
                                                   no secrets)
```

1. Web (chat) or the worker (channels, workflows, jobs) stages the run's
   workspace, skills and job file in a new folder under
   `OPENNEKO_AGENT_RUNS_DIR` (`/var/lib/openneko/agent-runs` in the images).
2. It starts the runner, `node $OPENNEKO_AGENT_ENTRY` (default
   `/opt/openneko-agent/entry.js`), as a child process in its own process
   group. The runner gets the broker URL on loopback, a token bound to
   `{run, org, thread}`, the serve socket and the org's model key. The
   launcher loads the key again for each run.
3. The runner sends each turn to `openneko serve` over the Unix socket
   `OPENNEKO_SERVE_SOCKET`. The turn carries the model key. Serve uses the key
   for the model calls of that turn and keeps it nowhere else. The agent's MCP
   tools (GraphJin, memory, files, actions) go back over the same socket to
   the runner, which calls the broker.
4. When `OPENNEKO_SANDBOX_URL` is set, serve sends terminal commands to the
   sandbox service. Without it, the agent has no terminal and the script
   skills are off.
5. When the run ends, the launcher kills the runner's process group, copies
   artifacts and skill edits back, and deletes the run folder. The next start
   deletes folders that a restart left behind.

`runChatTurn` keeps its split. The **prologue** (build the prompt) and the
**epilogue** (parse action and workflow fences, persist, scrub) run in the
trusted host. Only the agent loop runs in the runner and serve.

## What each piece holds

| Piece | Runs as | Holds | Network |
|---|---|---|---|
| Runner | the web or worker user (`neko`, 1001) | the run's broker token and the org's model key, for this run only | that of web or the worker |
| `openneko serve` | `neko`, 1001 | a turn's model key while the turn runs; the files service; the `neko_files` database login and the derived token key | the stack's network, for the model APIs |
| Sandbox service | root, to give each run its own user | **no secrets**; a command gets only `PATH`, `HOME`, `TMPDIR`, `LANG`, `SKILL_DIR`, `RUN_DIR`, `ARTIFACT_DIR` and `PYTHONPATH` | only its own network |

Model keys exist only in the runner and in serve. Serve drops every
`OPENNEKO_SANDBOX_*` value that a turn sends, and the sandbox request carries
no model key, so the terminal cannot read one.

## Two zones

The stack has two zones. The control zone is web and the worker. It holds
the secret key, the `neko` database login and the provider keys. The agent
zone is serve and the runner's turns. Serve holds only these:

- the `neko_files` database login. The role can read and write the three
  `file_content_*` tables and nothing else (migration 0127).
- the token key, `HMAC(secret key, "openneko-serve:v1")`. It checks the
  tokens that web and the worker sign. It cannot decrypt a stored value.
- a turn's model key, while the turn runs.

The one-shot `openneko-serve-init` step writes the first two into the
`openneko-serve-credentials` volume before serve starts. It runs on every
start, so a new secret key reaches serve on the next start. Serve does not
mount the config volume.

Containers and credentials separate the zones. The network does not: serve
shares the stack network with the database, web and the worker. The
database grants and the broker's per-run tokens stop serve from acting
outside its zone.

Agent runs read customer data only through the direct GraphJin tools. No run
hands a question to GraphJin's own agent, so GraphJin gets no provider key.
Migration 0128 removed the setting that did that.

## The sandbox service

The sandbox gives each run its own user and group from
`OPENNEKO_SANDBOX_BASE_UID` (default 20000). It runs a command only when the
workspace is under `OPENNEKO_SANDBOX_WORKSPACES`. POSIX ACLs give the run's
group its own staged folder and nothing else, so one run cannot read another.
Ending a run kills every process its user started and removes its home.

In the Compose overlay the sandbox container:

- sits **only** on its own network. Commands can reach the internet, but not
  `neko-db`, GraphJin, the broker or another service of the stack;
- drops every capability except `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `KILL`,
  `SETGID` and `SETUID`, and sets `no-new-privileges`;
- mounts only the sockets folder, the runs folder and the sandbox homes. It
  mounts no configuration, no secret and no Docker socket.

The OpenNeko product does not limit the internet egress of terminal commands.
Put an egress proxy or firewall on the sandbox network if you need that.

## Shared folders

- `openneko-agent-runs` → `/var/lib/openneko/agent-runs`, owned by
  `1001:1001`. Web, the worker, serve and the sandbox share it.
- `openneko-run` → `/run/openneko`, `1001:1001`, mode `0770`. It holds
  `serve.sock` and, with the overlay, `sandbox.sock`.

## Configuration

Compose sets all of these. They matter only for custom deployments.

| Variable | Where | Meaning |
|---|---|---|
| `OPENNEKO_SERVE_SOCKET` | web, worker, serve | the agent socket of serve (`/run/openneko/serve.sock`) |
| `OPENNEKO_SERVE_MAX_TURNS` | serve | turns that run at once (default 32) |
| `OPENNEKO_AGENT_RUNS_DIR` | web, worker | where runs are staged |
| `OPENNEKO_AGENT_ENTRY` | web, worker | the runner (`/opt/openneko-agent/entry.js`; a `.ts` path runs with tsx) |
| `OPENNEKO_SANDBOX_URL` | web, worker, serve | the sandbox service (`unix:///path`, `http://` or `https://`); empty turns the terminal off |
| `OPENNEKO_SANDBOX_KEY` | web, worker, serve, sandbox | the bearer key of the sandbox service |

Web and the worker check that serve answers on its socket before setup
completes and before onboarding work starts. When serve does not answer, they
report that the agent runtime (openneko serve) is unavailable.

## Development

`pnpm dev` runs `openneko serve` on the host next to web and the worker
(`scripts/dev-agent.sh`). To try the terminal, run the sandbox recipe and set
`OPENNEKO_SANDBOX_URL`.
