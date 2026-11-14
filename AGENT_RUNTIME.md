# Agent runtime

OpenNeko treats the agent loop as untrusted code: a prompt can steer it. Three
pieces run it, and each holds only what its job needs:

- **`openneko web`**, the trusted control plane. It stages each run, serves
  the agent's MCP tools and holds the database, secrets, policy and
  approvals. It runs every turn: chat, channel messages, scheduled,
  subscription, watcher, manual and API workflows, and background jobs;
- **`openneko serve`**, which runs each agent turn and every other model call:
  the Ax harness, the inference and embedding gateways and the agent's file
  tools;
- the optional **sandbox service** ([recipes/sandbox](recipes/sandbox/README.md)),
  which runs terminal commands, script skills and workflow script steps.

The agent reaches the database, secrets and policy only through the MCP tools
that `openneko web` serves for the run.

## How a run flows

```
 user ─▶ openneko web  (control plane: DB, secrets, policy, MCP tools)
            │ stage the run's workspace in the shared runs folder
            │ send each turn over the serve socket
            ▼
        openneko serve                         sandbox service
        (model key only for the turn) ──────▶  (own network, per-run user,
            │ terminal commands                 no secrets)
            │ (only with a sandbox URL)
            └── MCP tool calls back over the same socket to openneko web
```

1. `openneko web` stages the run's workspace, skills and job file in a new
   folder under `OPENNEKO_AGENT_RUNS_DIR` (`/var/lib/openneko/agent-runs` in
   the images).
2. It sends each turn to `openneko serve` over the Unix socket
   `OPENNEKO_SERVE_SOCKET`. The turn carries the org's model key. Serve uses
   the key for the model calls of that turn and keeps it nowhere else. The
   agent's MCP tools (GraphJin, memory, files, actions) go back over the same
   socket to `openneko web`, which checks the run's identity and policy.
3. When `OPENNEKO_SANDBOX_URL` is set, serve sends terminal commands to the
   sandbox service. Without it, the agent has no terminal and the script
   skills are off. An admin can also turn the terminal off for one
   organization at Admin, Settings, Agent. The run then gets no sandbox URL
   or key, the script skills are not staged, and the prompt and skill search
   leave them out.
4. When the run ends, `openneko web` copies artifacts and skill edits back
   and deletes the run folder. The next start deletes folders that a restart
   left behind.

`openneko web` builds the prompt before the turn and parses the action and
workflow fences, persists and scrubs after it. Only the agent loop runs in
serve.

## What each piece holds

| Piece | Runs as | Holds | Network |
|---|---|---|---|
| `openneko serve` | `neko`, 1001 | a turn's or a host call's model key while it runs; the files service; the `neko_files_<database>` login and the derived token key; the customer GraphJin and its config | the stack's network, for the model APIs and the customer's data sources |
| Sandbox service | root, to give each run its own user | **no secrets**; a command gets only `PATH`, `HOME`, `TMPDIR`, `LANG`, `SKILL_DIR`, `RUN_DIR`, `ARTIFACT_DIR` and `PYTHONPATH` | only its own network |

Model keys exist only in `openneko web` while it runs a turn, and in serve. Serve drops every
`OPENNEKO_SANDBOX_*` value that a turn sends, and the sandbox request carries
no model key, so the terminal cannot read one.

## The inference gateway

Every model call goes through one Go package, `apps/ax-harness/inference`.
Agent turns use it, and so do the host's own calls: question
classification, the briefing summary, transcript compaction, bot names and
job descriptions, skill lessons, the research mission and the provider test
in Admin, Settings.

A host call is an Ax signature program. Web or the worker resolves the
org's model route and loads its key, then sends one `generate` frame over
the serve socket. The key travels in its own field, as it does for a turn.
Serve runs the program, returns the output fields and the token counts, and
keeps the key for that call only. Error text never carries the key. Where no
serve socket exists, as in a test, the host runs `ax-harness generate` for
the call.

The gateway takes every Ax provider and any OpenAI-compatible URL. Ollama,
vLLM, LM Studio and other local servers use the OpenAI-compatible route with
no key. Web and the worker hold no provider client of their own.

The industry research step still calls Perplexity directly from the worker,
because the gateway does not yet return Perplexity's citations.

### Models and task routes

The model in Admin, Settings, Model provider is the default model. At Admin,
Settings, Models, an admin adds more models (`llm_model`, one row per model,
keys sealed like the provider key) and gives each task a route
(`llm_task_route`). The tasks are agent turns, the question classifier,
compaction, bot instructions, bot names, skill lessons and the research
mission. A task with no route follows the "default" route. The default route
always uses the default model and may name a fallback. A disabled model counts
as unset, so its tasks use the default model.

`openneko web` answers the model routes (`/api/v1/admin/models/*` and
`/api/v1/admin/model-routes`). The provider list, the default models, the
form fields and the provider checks are in `apps/openneko/internal/llm`.
`packages/llm` keeps a copy until the host model code moves to Go. Both
copies must match `packages/llm/test/fixtures/model-catalog.json`; a Go test
and a TypeScript test check this.

`openneko web` also makes host model calls in Go. It answers the provider
test (`/api/v1/settings/provider/test`), the external service checks
(`/api/v1/admin/services`) and the onboarding submit
(`/api/v1/onboarding/submit`). A Go host call reads the task route and its
fallback, loads each key (a Vertex key is a token from Google Application
Default Credentials), sends the `generate` frame to serve's socket and writes
the `spend_ledger` row. With no `OPENNEKO_SERVE_SOCKET`, the call runs inside
`openneko web`. The worker keeps the TypeScript host calls until its jobs
move to Go. The Go and TypeScript code must give the same Ax route for each
provider and the same price for each case in
`packages/llm/test/fixtures/host-calls.json`; a Go test and a TypeScript
test check this.

A route may name a fallback model. The gateway sends a call to the fallback
only when the first model fails with an error that Ax classifies as
retryable, such as a 503 or a timeout, after the first model's own retries.
A refused key or a bad request does not go to the fallback. For a host call,
the frame carries the fallback route and its key in `fallback_key`. For a
turn, `openneko web` sends the fallback key as `FALLBACK_MODEL_API_KEY`, and the
harness reads a two-route profile in `HARNESS_MODEL_ROUTES`. The fallback
route never runs a tool call again. A streamed answer goes to the fallback
only when the first model fails before it sends any text. After the first
text, the error stands, because the user may already see part of the answer.

Each model may carry a price in USD per million tokens. That price wins over
the model price list. Every host call writes a `spend_ledger` row with its
task, provider and model. A host call with no price costs nothing in the
ledger and raises no alert; a turn with no price is still charged the
per-run cap. Admin, Settings, Spend shows the last 30 days by task and
model. Each change to a model or a route goes in the audit chain.

### The embedding gateway

Every embedding call goes through one Go package,
`apps/ax-harness/embedding`. It calls an OpenAI-compatible `/embeddings`
route at `NEKO_EMBEDDING_URL`. Web and the worker read the URL, the model
and the dimensions, and decide whether embedding is on. They send one
`embed` frame over the serve socket with up to 64 texts and the key in
`model_key`. Serve returns one vector per text, in order. It refuses a
redirect, a URL with credentials or a query, and a vector of the wrong
size, and keeps the key out of error text. Where no serve socket exists,
the host runs `ax-harness embed`, with the key in
`HARNESS_EMBEDDING_API_KEY`. When embedding is off, nothing calls out and
search uses Postgres full-text search.

### The data access interface

The agent's data tools (`neko_graphjin`: the tool list and each tool
call) and the broker's read-query route reach the customer's data through
one interface, `packages/llm/src/data-access`. The broker calls it with
the run's org and run id. The interface checks the run's grants first: a
run whose user holds no data source gets an error, a saved query the user
does not hold returns a refusal, and the read query accepts query
operations only. Then it passes the call to the data backend.

The backend today is GraphJin (`data-access/graphjin.ts`). It finds the
org's data source, signs the run's GraphJin token with the user's group
roles, refuses the internal GraphJin, applies the config's write guard
and calls GraphJin's MCP or GraphQL route. Another backend implements
`DataBackend` (`query`, `listTools`, `callTool`); the grant checks stay
the same.

## Two zones

The stack has two zones. The control zone is web and the worker. It holds
the secret key, the `neko` database login and the provider keys. The agent
zone is serve and its turns. Serve holds only these:

- the `neko_files_<database>` login. The role can read and write the three
  `file_content_*` tables and nothing else. `openneko serve-init` creates it
  and grants its rights at each start.
- the `neko_subscriptions_<database>` login. The role can read
  `workflow_output` and nothing else, and comes from the same step.
- the token key, `HMAC(secret key, "openneko-serve:v1")`. It checks the
  tokens that web and the worker sign. It cannot decrypt a stored value.
- a turn's or a host call's model key, while it runs.

The `openneko-serve-init` start step writes the first three into the
`openneko-serve-credentials` volume before serve starts. It runs on every
start, so a new secret key reaches serve on the next start. Serve does not
mount the config volume.

Containers and credentials separate the zones. The network does not: serve
shares the stack network with the database, web and the worker. The
database grants and the per-run tokens stop serve from acting
outside its zone.

Agent runs read customer data only through the direct GraphJin tools. No run
hands a question to GraphJin's own agent, so GraphJin gets no provider key.
Migration 0128 removed the setting that did that. Admin chat plans GraphJin
config changes with OpenNeko's own model: the `graphjin-config` skill reads
the redacted config through `describe_source_config`, and the seed configs
set `agent.enabled: false`. The model fills a typed proposal
(`request_source_config_change`) and never writes YAML. After an admin
approves, the worker applies the change to the running GraphJin and writes it
to `OPENNEKO_GRAPHJIN_CONFIG`: sources, access, table lists and roles.

Before a proposal reaches an admin, the control plane checks it three times:
the answers (an empty login, a password name that is not saved, a port out of
range, a source or table that does not exist), then GraphJin's live preview,
then the config file. For the file check, the control plane writes the config
as it would be after approval to a private folder beside the config
(`.openneko-check/<id>/`, mode 0700) and asks serve to load it with the
pinned GraphJin loader. Serve listens on the Unix socket
`OPENNEKO_CONFIG_CHECK_SOCKET` (mode 0660, the same rule as the agent
socket). The request names the candidate by id only, serve reads only that
folder, and the reply holds errors only. The control plane deletes the
candidate after the check. Each problem names the answer at fault, and the
source form shows it beside that field. API and local-file sources skip the
file check, because their files exist only after approval.

An admin uploads the files of a local-file source in the source form
(`POST /api/v1/settings/file-sources`; the Go web front answers it). Go
writes them to a folder per org and source under `files/` beside the
GraphJin config, or under `OPENNEKO_GRAPHJIN_FILES_LOCAL_DIR`. It refuses
new files for a source that GraphJin already serves or that an admin
approved. The upload and the approval worker take the same lock file.

An admin adds an API source from an OpenAPI 3 document in the source form
(`GET` and `POST /api/v1/settings/openapi-specs`; the Go web front answers
them). The admin uploads a YAML or JSON file of up to 2 MB, or gives an
HTTPS URL. Go fetches a URL only when every address of its host is public,
connects to the address it checked, and follows at most three redirects.
It refuses a document with an external `$ref`, sets the one base URL in
`servers`, counts the read and mutating operations, and stores the
document as YAML with its SHA-256 checksum. The approval worker checks
that checksum before it writes the file for GraphJin.

OpenNeko says what a config does in plain sentences
(`packages/llm/src/graphjin/config-describe.ts`): for each source, its kind,
host and database, whether GraphJin blocks changes, who may read, change and
delete, and which tables are hidden, admin-only or open. `describe_source_config`
returns them as `summary`, and Admin settings, GraphJin config shows them.
A valid preview also carries `summary` with the touched sources and roles
before and after the change, and their redacted YAML. The approval card shows
the sentences side by side and keeps the YAML diff behind a disclosure.

Admin settings, GraphJin config also shows the config file as read-only YAML
(`packages/llm/src/graphjin/config-file-view.ts`). The page names the file's
path on the server and GraphJin's state from `.openneko-graphjin-state`:
running, or restarting with the reason. The same redaction code as
`describe_source_config` hides the secrets, and the view drops comments. The
page has no edit control and no route behind it. Admins edit the file on the
server and run `openneko graphjin check`.

A database source needs a login with a password. OpenNeko refuses a
proposal with no saved password, or with a saved password that is empty, at
the answer check and again at apply. The password goes to GraphJin only. The
control plane and the approval worker replace it with `[REDACTED]` in every
error text, so GraphJin's errors cannot carry it to the model, a log line or
the thread.

The source check (`packages/llm/src/graphjin/source-check.ts`) tests one
PostgreSQL, MySQL or MariaDB source in order: the host resolves, the port
answers, TLS, the login, the rights to read the schema, and read-only rights.
It stops at the first failed step. Each failure names the answer at fault and,
where SQL fixes it, gives the SQL for the database administrator. A passed
check lists the tables the login can read, with the planner's row estimates.
The check runs in the control zone, because it needs the stored password.
Three callers use it: the `check_source` tool (through the broker, admin runs
only), the "Check connection" part of the source form
(`POST /api/v1/settings/source-check`, admins only; the Go web front answers it with a Go copy of the check in `apps/openneko/internal/sourcecheck`), and `openneko source check`
(the worker's `POST /admin/sources/check`). The form shows the tables with all
selected; the tables the admin clears become `blockedTables` in the proposal.
TLS: the check reports whether the server offers TLS and whether its
certificate verifies against the system's trusted certificates. It then signs
in with TLS when the server offers it, without certificate checks, as pgx does
by default for GraphJin.

For a system with a pack, `present_pack_setup` shows the pack's setup card
in chat. The card is the same component as Admin settings, Packs
(`apps/web/src/components/admin/PackSetup.tsx`) and calls the same admin pack
routes: install, review, then install or configure with the review hash. The
pack's own `graphjin/sources.yaml` is the ready config, so the admin enters
only the inputs and secrets that the pack declares. The secrets go from the
card to the pack API. The model gets the pack ID and, after apply, one
message that names the pack and its status. A pack source does not file a
`source_config_admin` request: the admin's approval in the card is the
approval, as it is on the settings page.

The org setting "Ask-based config" (Admin settings, Data access rules) turns
these tools on. It is on for an organization made after migration 0130.
Migration 0130 saves "off" for each organization that had no saved value, so
an upgrade changes nothing. Every change still needs an admin's approval.

## Start steps

`openneko start` runs these steps in order, each in its own container with
`docker compose run --rm`, and stops at the first failure:

1. An external database only: `database-check` runs `openneko database
   check` (see INSTALL.md, "OpenNeko's database").
2. `storage-reconcile` reports the collation state of an external database
   and changes nothing. With the bundled database, it rebuilds the indexes
   after a libc or collation change.
3. `neko-migrate` applies the migrations (`--skip-migrate` turns it off).
   It holds a Postgres advisory lock while it runs. A second migrator
   waits for the lock and names the process that holds it every 15 seconds.
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

- It connects as `neko_subscriptions_<database>`. Serve reads the role's URL from
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
`OPENNEKO_SANDBOX_BASE_UID` (default 20000), and its own folder, mode 0700,
under `OPENNEKO_SANDBOX_WORKSPACES`. One run cannot read another. The
sandbox shares no folder with the stack. Each command carries the run's
files that changed since the last command, and the answer carries the files
that the command changed; serve, or `openneko web` for a workflow script step,
writes them into the staged run. The run's `.openneko` folder, which holds
the job file, never goes to the sandbox, and nothing the sandbox returns may
land in it. When a command ends, the sandbox kills every process of the
run's user. Ending a run removes its folder and its home. The protocol is in
`recipes/sandbox/README.md`.

A skill script gets its data as files. When it asks for rows, it stops with
its request; serve, or `openneko web` for a script step, runs the query through
the run's GraphJin tools and runs the script again with the rows. The
sandbox holds no key, token or socket for this. A script can also declare
its queries in a `<script>.queries.json` file beside it; OpenNeko runs them
before the script starts and passes the rows in as files. The steps, the
file format and the limits are in `recipes/sandbox/README.md`, "Script
data".

In the Compose overlay the sandbox container:

- sits **only** on its own network. Commands can reach the internet, but not
  GraphJin, the broker or another service of the stack. In prod mode
  OpenNeko's database is a server on your network, so firewall the sandbox
  network from it;
- drops every capability except `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `KILL`,
  `SETGID` and `SETUID`, and sets `no-new-privileges`;
- mounts only the sockets folder and its own volumes for run folders and
  homes. It mounts no configuration, no secret, no Docker socket and no
  folder of the stack.

The OpenNeko product does not limit the internet egress of terminal commands.
Put an egress proxy or firewall on the sandbox network if you need that.

## Shared folders

- `openneko-agent-runs` → `/var/lib/openneko/agent-runs`, owned by
  `1001:1001`. Web, the worker and serve share it. The sandbox does not
  mount it.
- `openneko-run` → `/run/openneko`, `1001:1001`, mode `0770`. It holds
  `serve.sock` and, with the overlay, `sandbox.sock`.

## Configuration

Compose sets all of these. They matter only for custom deployments.

| Variable | Where | Meaning |
|---|---|---|
| `OPENNEKO_SERVE_SOCKET` | web, worker, serve | the agent socket of serve (`/run/openneko/serve.sock`) |
| `OPENNEKO_SERVE_MAX_TURNS` | serve | turns that run at once (default 32) |
| `OPENNEKO_AGENT_RUNS_DIR` | web, worker | where runs are staged |
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
