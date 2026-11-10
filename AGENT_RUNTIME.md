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
| `openneko serve` | `neko`, 1001 | a turn's model key while the turn runs; the files service; the `neko_files` database login and the derived token key; the customer GraphJin and its config | the stack's network, for the model APIs and the customer's data sources |
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
- the `neko_subscriptions` database login. The role can read
  `workflow_output` and nothing else (migration 0129).
- the token key, `HMAC(secret key, "openneko-serve:v1")`. It checks the
  tokens that web and the worker sign. It cannot decrypt a stored value.
- a turn's model key, while the turn runs.

The `openneko-serve-init` start step writes the first three into the
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

## Start steps

`openneko start` runs these steps in order, each in its own container with
`docker compose run --rm`, and stops at the first failure:

1. An external database only: `database-check` runs `openneko database
   check` (see INSTALL.md, "OpenNeko's database").
2. `storage-reconcile` checks the database storage contract.
3. `neko-migrate` applies the migrations (`--skip-migrate` turns it off).
4. `openneko-config-init` gives the config volume to user 1001.
5. Demo mode only: `adventureworks-init` loads the sample data and
   `neko-adventureworks-seed` creates the demo organization.
6. `openneko-serve-init` writes serve's credentials and the GraphJin config.

Then `docker compose up` starts the services. `openneko upgrade` and
`openneko restore` run the same steps. The steps are in the compose profile
`steps`, so `up` does not start them and no running service waits for them.
From a source checkout, `docker compose run --rm openneko-serve-init` runs
steps 2, 3, 4 and 6 in order.

In prod mode, steps 1, 2, 3 and 6, web and the worker read OpenNeko's database
URL from the Compose secret `database-url`. Serve does not get it. When the
URL names TLS files, start writes `database-tls.yml`, which gives the same
services and serve a copy of each file as a Compose secret. Dev, demo
and a legacy prod install load `bundled-db.yml`, which adds `neko-db` and
points the same services at it.

## The customer GraphJin

Serve runs the customer GraphJin from the pinned GraphJin module (v3.21.18)
as a child process of the same executable: `openneko graphjin serve --child`.
A GraphJin crash ends only the child. Serve listens on port 8080 and answers as `graphjin` on the
stack network, so stored endpoints such as `http://graphjin:8080` keep
working. Its config, `agentic.yml`, holds the customer's source logins and the
per-org JWT secret. It is in the agent zone, as it was when GraphJin had its
own container.

- `openneko-serve-init` creates `agentic.yml` from the seed in the serve image
  when the file is missing, with a new keystore key. It then finds or creates
  the organization row and writes the per-org JWT secret, the write policy and
  the `system` block into the file. GraphJin reads auth keys only when it
  starts, so `openneko start` runs this step before it starts serve, web
  and the worker.
- When the worker changes the config, it writes a new token to
  `.openneko-graphjin-restart`. Serve then starts a new child with the file
  and writes the token to `.openneko-graphjin-restart-ack` when the child
  answers or exits. Serve answers `.openneko-graphjin-supervisor-ping` the
  same way. These are the files the old supervisor script used.
- Serve passes each request on port 8080 to the child through a private
  socket. When the child exits, serve starts it again after 1 s, then 2 s,
  up to 30 s. It starts the child at once when the config file changes or a
  restart token arrives. While the child is down, file search and agent
  turns keep working, and port 8080 answers HTTP 503 with "GraphJin is
  restarting" and the child's last line of output.
- Serve writes the child's state to `.openneko-graphjin-state` beside the
  config. `openneko graphjin status` prints it, and `openneko status` shows
  it as the `graphjin` line.
- The child gets the config path, the socket and a short list of variables:
  `GJ_*`, `GO_ENV`, `PATH`, `HOME`, `TZ`, the locale, the CA file paths and
  the Go runtime settings. It gets no serve credentials path, no agent
  socket and no sandbox key. It runs as the same user in the same
  container, so it can still read files that serve can read. Those files
  hold agent-zone secrets only, so this crosses no zone line.

## The subscriptions GraphJin

Serve also runs the GraphJin that serves the worker's workflow output
subscriptions, on port 8089. It is a second child process of the same kind,
with the same restarts, 503 answer and state file.

- It connects as `neko_subscriptions`. Serve reads the role's URL from
  `subscriptions.url` in the credentials folder and writes the GraphJin config
  into a private folder (`OPENNEKO_SUBSCRIPTIONS_DIR`, default
  `$TMPDIR/openneko-subscriptions`, mode 0700). When the URL changes, serve
  restarts the child. A URL for another role is refused, and the running
  child stays.
- The child gets no `GJ_*` variables and no `GO_ENV`, because those are for
  the customer GraphJin.
- `openneko graphjin status --subscriptions` prints its state, and
  `openneko status` shows it as the `subscriptions` line.
- This replaces the `neko-graphjin` container, which logged in as `neko`.
  `openneko start` and `upgrade` remove the old container.

## The sandbox service

The sandbox gives each run its own user and group from
`OPENNEKO_SANDBOX_BASE_UID` (default 20000). It runs a command only when the
workspace is under `OPENNEKO_SANDBOX_WORKSPACES`. POSIX ACLs give the run's
group its own staged folder and nothing else, so one run cannot read another.
Ending a run kills every process its user started and removes its home.

In the Compose overlay the sandbox container:

- sits **only** on its own network. Commands can reach the internet, but not
  GraphJin, the broker or another service of the stack. In prod mode
  OpenNeko's database is a server on your network, so firewall the sandbox
  network from it;
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
| `OPENNEKO_GRAPHJIN_CONFIG` | web, worker, serve, serve-init | the customer GraphJin config (`/config/graphjin/agentic.yml`); empty runs no GraphJin in serve |
| `OPENNEKO_GRAPHJIN_ADDR` | serve | where the customer GraphJin listens (default `127.0.0.1:8080`; Compose sets `0.0.0.0:8080`) |
| `OPENNEKO_GRAPHJIN_SEED` | serve-init | the template for a new config; the serve image sets it |
| `OPENNEKO_SUBSCRIPTIONS_ADDR` | serve | where the subscriptions GraphJin listens (default `127.0.0.1:8089`; Compose sets `0.0.0.0:8089`); empty runs none |
| `OPENNEKO_SUBSCRIPTIONS_DIR` | serve | the private folder for its config and state |
| `OPENNEKO_GRAPHJIN_URL` | web, worker | the subscriptions GraphJin (`http://openneko-serve:8089`) |

Web and the worker check that serve answers on its socket before setup
completes and before onboarding work starts. When serve does not answer, they
report that the agent runtime (openneko serve) is unavailable.

## Development

`pnpm dev` runs `openneko serve` on the host next to web and the worker
(`scripts/dev-agent.sh`). That serve runs no GraphJin. The customer GraphJin
runs in the `openneko-serve` container that `pnpm dev:up` starts, on
`localhost:8080`. To try the terminal, run the sandbox recipe and set
`OPENNEKO_SANDBOX_URL`.
