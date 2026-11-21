# Agent runtime

OpenNeko treats the agent loop as untrusted code: a prompt can steer it. Three
pieces run it, and each holds only what its job needs:

- **`openneko web`**, the Go server in the control plane that stages each run's
  workspace, serves the agent's MCP tools in-process, and runs chat turns,
  channel turns, manual and scheduled workflows, metric refreshes, and setup
  builds;
- **`openneko serve`**, which runs the Ax harness for each agent turn and every
  other model call, the inference and embedding gateways, and the agent's file
  tools (when `OPENNEKO_SERVE_SOCKET` is not set, `openneko web` runs the Ax
  harness in-process);
- the optional **sandbox service** ([recipes/sandbox](recipes/sandbox/README.md)),
  which runs terminal commands, script skills and workflow script steps.

`openneko web` is the trusted control plane. The database, secrets, policy and
approvals live in the control plane. The agent reaches them only through the
per-run tool bridge.

## How a run flows

```
 user / job ─▶ openneko web  (control plane: DB, secrets, policy, tool handlers)
                  │ stage the run's workspace in OPENNEKO_AGENT_RUNS_DIR
                  ▼
              openneko web ── one turn per connection ──▶ openneko serve
              (in-process MCP tool handlers;              (openneko-serve container
               the key door holds the model keys)          or native serve process;
                  ▲    ▲                                   a door token, no model key)
                  │    └── model calls over the door socket ────┤
                  │ MCP tools over the serve socket             │ terminal commands
                  └─────────────────────────────────────────────┤ (only with a sandbox URL)
                                                                ▼
                                                         sandbox service
                                                         (closed network, per-run user,
                                                          no secrets)
                                                                │ listed hosts only
                                                                ▼
                                                         egress proxy
```

`openneko web` runs chat questions, channel messages, manual and scheduled
workflows, metric refreshes, and setup builds in Go:

1. `openneko web` stages the run's workspace and skills in a new folder under
   `OPENNEKO_AGENT_RUNS_DIR` (`/var/lib/openneko/agent-runs` in the images).
   It loads only the team skills that the person's groups hold and that
   person's own skills.
2. `openneko web` resolves the org's model route and fallback route, gives
   their API keys to the [key door](#the-key-door) for this turn, and sends
   the turn to `openneko serve` over the Unix socket `OPENNEKO_SERVE_SOCKET`
   (or runs the Ax harness in-process when no socket is set). The turn
   carries a door token and door URLs in place of the keys and the provider
   URLs. Serve sends each model call of the turn through the door. The
   agent's MCP tools (GraphJin, memory, files,
   skills, cards, user input, actions) go back over the same socket to
   `openneko web`'s in-process tool bridge.
3. For a workflow with script steps, `openneko web` runs the script steps
   before the turn against the sandbox service, pre-fetching any declared
   `<script>.queries.json` files and serving `neko_data` / `neko-query`
   requests through the run's read-only GraphJin tools.
4. Serve reads the sandbox service from its own `OPENNEKO_SANDBOX_URL` and
   `OPENNEKO_SANDBOX_KEY` (`apps/openneko/internal/cli/serve.go`). The agent
   gets a terminal only when `openneko web` sends the turn
   `OPENNEKO_HARNESS_SHELL=1` and serve has a sandbox URL
   (`apps/ax-harness/harness/harness.go`). Web sends it when its own
   `OPENNEKO_SANDBOX_URL` is set and the organization's terminal is on
   (`orgTerminal` in `apps/openneko/internal/webfront/turncontext.go`). An
   admin turns the terminal off for one organization at Admin, Settings,
   Agent. Without the terminal, `openneko web` does not stage the script
   skills, and the prompt and skill search leave them out. Web also sends
   the organization's egress list (`OPENNEKO_HARNESS_EGRESS_HOSTS`), and
   each command to the sandbox carries it.
5. When the turn ends, `openneko web` copies artifacts and skill edits back
   (saving a member's edit to a team skill as a private copy for that member)
   and deletes the run folder. The next start deletes folders that a restart
   left behind.

`openneko web` builds the prompt before the turn, and parses the action and
workflow fences, persists and scrubs after it. Only the agent loop runs in the
harness.

## What each piece holds

| Piece | Runs as | Holds | Network |
|---|---|---|---|
| `openneko web` | `neko`, 1001 (or `openneko` on a server install) | the `neko` database login, `secrets.json`, the in-process tool handlers and the key door | the stack's network, for the model APIs |
| `openneko serve` | `neko`, 1001 (or `openneko` on a server install) | a door token for each call that runs, never a model key; the files service; the `neko_files_<database>` login and the derived token key; the customer GraphJin and its config | the stack's network, for the customer's data sources |
| Sandbox service | root, to give each run its own user | **no secrets**; a command gets only `PATH`, `HOME`, `TMPDIR`, `LANG`, `NODE_PATH`, `SKILL_DIR`, `RUN_DIR`, `ARTIFACT_DIR` and `PYTHONPATH` (`recipes/sandbox/sandbox.go`) | only its own internal network, with no route out; it shares one volume with web and serve, `openneko-sandbox-socket`, which holds only its socket |
| Egress proxy | `openneko-sandbox egress`, no capabilities, read-only root | **no secrets**; it asks the sandbox for a run's list over a root-only socket | the sandbox network and a network with a route out |

Model keys stay in `openneko web`. Serve gets a door token that is valid
for one call only. Serve drops every `OPENNEKO_SANDBOX_*` value that a turn
sends, and the sandbox request carries no model key and no door token.

## The key door

`openneko web` runs the key door (`apps/openneko/internal/llm/keydoor.go`).
The door keeps every model key, fallback key and embedding key in the
control zone. For each turn, host call and embedding call, web does this:

1. It makes a random token for that call. The token is valid for one org,
   for the routes of that call (`primary`, `fallback` or `embed`), for a
   number of calls, and until the call ends or its deadline passes.
2. It sends serve the token in place of each key, and
   `http://openneko-door/r/<route>` in place of each provider URL.
3. When the call ends, it revokes the token. A call that is still
   streaming stops at once.

Serve and the Ax harness send the model calls to the door over a Unix
socket, `keydoor.sock`. The door checks the token in constant time. It
forwards a call only to the stored upstream of the token's route, so it is
not an open proxy. It removes the key headers that serve sent and puts the
real key where the provider wants it:

| Provider | Key on the wire |
|---|---|
| OpenAI, OpenAI-compatible URL, and the other Ax providers | `Authorization: Bearer <key>` |
| Anthropic | `x-api-key` |
| Google Gemini | `x-goog-api-key` |
| Azure OpenAI | `api-key`, on `https://<resource>.openai.azure.com/openai/deployments/<deployment>` with the route's `api-version` |
| Vertex AI | `Authorization: Bearer <token>`; web gets the token from Google Application Default Credentials when the call starts |
| OpenAI-compatible URL with no key | no key header |

The door forwards model calls only. Each route allows the paths of its
provider's chat, streamed chat or embedding call, and refuses every other
path with 403:

| Route | Paths | Query |
|---|---|---|
| Anthropic | `/v1/messages` | none |
| Google Gemini | `/models/<model>:generateContent`, `/models/<model>:streamGenerateContent` | `alt=sse` |
| Azure OpenAI | `/chat/completions` | `api-version`, which the door sets to the route's version |
| Every other chat route | `/chat/completions` | none |
| Embedding | `/embeddings` | none |

A query parameter that is not in the table gets 403. Each token also has a
call cap, and the door answers 429 after it:

- A turn: the turn's model-call ceiling plus 4, for the resamples and the
  fallback of the last call. Web sends the ceiling to serve as
  `max_model_calls`: the actor steps times one more than the delegated
  child steps, plus 2, at most 2000 (1277 for a chat turn with the
  defaults). A workflow API run uses its own `maxModelCalls` when that
  number is lower.
- A host call: 5 per route, the first try and four retries.
- An embedding call: 1.

The door counts the calls of each token. When the call ends, web revokes
the token and compares the count with the calls that serve reported:
`usage.requests` of the turn's result, the requests of a host call, and one
for an embedding call. When serve exits, fails or drops the connection with
no result, web uses the number of `model.request.started` events that serve
sent, and allows one call that serve started but did not send to the door.
Serve's count may also hold the calls that the door refused for load. A
difference outside these bounds writes a log line and, for a turn or a host call, an
`agent:model_calls_mismatch` event in the org's audit chain, with the
purpose and both counts. An embedding call belongs to no org, so it gets
the log line only.

The door limits time and load. Serve must send the whole request body
within 30 seconds, else the door answers 408. A body over 32 MiB gets 413:
the largest honest body is a prompt that fills a 2M-token context window,
or 64 embedding texts of 32,000 characters each. The door reads the body
before the call takes a slot. One token has at most 4 calls open at once
and gets 429 for one more. The door forwards a number of calls at once
that follows the "Concurrent jobs" setting: 4 for each agent turn that may
run at once, plus 32 for host and embedding calls, and at least 64. A call
over that number waits up to 2 minutes for a free slot, then gets 503. The
provider must start its answer within 10 minutes, else the door answers
502. A call ends at the token's deadline. The door refuses a protocol
upgrade and drops the provider's 1xx answers.

The door streams each answer back as it arrives. It asks the provider for
an uncompressed answer (`Accept-Encoding: identity`) and does not forward
serve's value. It decodes a gzip or deflate answer and refuses any other
encoding with HTTP 502, because it cannot scrub it. It replaces the key
with `[redacted]` in every body, 2xx and streamed included, in each
response header and trailer, and in the raw, JSON-escaped, URL-encoded
and base64 forms. A key split across two chunks is replaced too: the door
holds back a chunk's tail only while it can start a form of the key. Its
log lines carry the route and the status only. Spend rows, audit rows, traces, retries and fallback rules
work as before.

The socket has mode 0660, so only the accounts of web and serve can
connect, and each call needs a valid token as well. Its path is
`OPENNEKO_KEY_DOOR_SOCKET`, else `keydoor.sock` beside `OPENNEKO_SERVE_SOCKET`:

- Docker: `/run/openneko/keydoor.sock`, in the `openneko-run` volume. Only
  web and serve mount that volume. Web and serve run as the same account
  (`neko`, 1001), so the token separates them from other callers there. The
  sandbox does not mount the volume.
- Server install: `/var/lib/openneko/shared/door`, owner `openneko`, mode
  0700. Both units set `OPENNEKO_KEY_DOOR_SOCKET`.
- Native install for one user: `~/.local/share/openneko/door`; on Windows
  `%LOCALAPPDATA%\OpenNeko\door`. Windows has no file modes. OpenNeko gives
  the data folder a protected access list that allows only the user and
  SYSTEM, and each socket in it takes that list.

In production (`OPENNEKO_ENV` unset or `production`), serve refuses a frame
that holds a model key in place of a door token. It also refuses a frame
whose environment holds a value in the form of a provider key or cloud
credential, or a variable from the list below. At start, serve removes
these variables from its environment and logs a warning that names them:
every `*_API_KEY`, `HF_TOKEN`, `VERTEX_ACCESS_TOKEN`,
`GOOGLE_VERTEX_ACCESS_TOKEN`, `GOOGLE_APPLICATION_CREDENTIALS`,
`AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AZURE_OPENAI_KEY`,
`NEKO_SECRET_KEY`, and each `DATABASE_URL`, `*_DATABASE_URL` and
`*_DATABASE_URL_FILE` outside `GJ_*`. `openneko release-check
--serve-socket` connects to `OPENNEKO_SERVE_SOCKET` and fails when serve
started with such a variable or runs in development mode.

The native install for one user (`openneko start` with no server install)
starts serve with a filtered copy of the shell environment. The copy holds
no variable whose name holds `KEY`, `TOKEN`, `SECRET`, `PASS`,
`CREDENTIAL`, `DATABASE_URL`, `DSN` or `AUTH`, except GraphJin's `GJ_*`
variables. Serve reads its own `OPENNEKO_SANDBOX_KEY` and its bucket login
(`OPENNEKO_FILES_BUCKET_KEY_ID`, `OPENNEKO_FILES_BUCKET_SECRET`) as one
line on its stdin at each start, and passes the bucket login to its network
part the same way. Web gets the same filtered copy plus the secret-named
variables that it reads (`webReads` in `apps/openneko/internal/native/native.go`;
`TestWebReadsIsComplete` fails when the code reads a new one). In Docker, web
and serve read the sandbox key and the embedding key from compose secret
files.

On every native install, the server install too, web and serve run as one
OS account, so the OS does not separate them by account. Serve confines
itself so that it cannot read web's files (see "Serve confines itself
(Linux and macOS)"), and each agent turn runs in a confined runner whose
environment holds only `PATH` and `TMPDIR` (test
`TestRunnerGetsNoControlZoneSecret`). Docker gives each zone its own
container.

With no `OPENNEKO_SERVE_SOCKET`, `openneko web` refuses agent turns, and
the `ax-harness` binary refuses a turn. One path still carries a key toward
the agent loop: `openneko serve` with `OPENNEKO_ENV=development` takes a
real key in a frame. A native install has no separation of web and serve by
OS account. Use these two for development and demos only.

## The inference gateway

Every model call goes through one Go package, `apps/ax-harness/inference`.
Agent turns use it, and so do the host's own calls: question
classification, the briefing summary, transcript compaction, bot names and
job descriptions, skill lessons, the research mission and the provider test
in Admin, Settings.

A host call is an Ax signature program. `openneko web` resolves the
org's model route, gives its key to the key door, then sends one
`generate` frame over the serve socket. The frame carries a door token in
place of the key, as a turn does. Serve runs the program through the door
and returns the output fields and the token counts. Error text never
carries the key. With no
serve socket, `openneko web` runs the call in-process.

The gateway takes every Ax provider and any OpenAI-compatible URL. Ollama,
vLLM, LM Studio and other local servers use the OpenAI-compatible route with
no key. `openneko web` holds no provider client of its own.

The industry research step (`industry_insights_build`) runs in `openneko web`
and calls Perplexity directly (`apps/openneko/internal/webfront/industrybuild.go`),
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
`packages/llm` keeps a copy for shared fixtures and tests. Both
copies must match `packages/llm/test/fixtures/model-catalog.json`; a Go test
and a TypeScript test check this.

`openneko web` makes all host model calls in Go: question classification,
compaction, bot names and instructions, skill lessons, the research mission,
the provider test (`/api/v1/settings/provider/test`), the external service
checks (`/api/v1/admin/services`) and the onboarding submit
(`/api/v1/onboarding/submit`). A Go host call reads the task route and its
fallback, loads each key into the key door (a Vertex key is a token from
Google Application Default Credentials), sends the `generate` frame to
serve's socket and writes
the `spend_ledger` row. With no `OPENNEKO_SERVE_SOCKET`, the call runs inside
`openneko web`. The Go and TypeScript code must give the same Ax route for each
provider and the same price for each case in
`packages/llm/test/fixtures/host-calls.json`; a Go test and a TypeScript
test check this.

A route may name a fallback model. The gateway sends a call to the fallback
only when the first model fails with an error that Ax classifies as
retryable, such as a 503 or a timeout, after the first model's own retries.
A refused key or a bad request does not go to the fallback. For a host call,
the frame carries the fallback route, and `fallback_key` holds the door
token. For a turn, `FALLBACK_MODEL_API_KEY` holds the door token, and the
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
route at `NEKO_EMBEDDING_URL`. `openneko web` reads the URL, the model
and the dimensions, and decides whether embedding is on. It sends one
`embed` frame over the serve socket with up to 64 texts and a door token
in `model_key`. Serve calls the service through the key door. Serve returns one vector per text, in order. It refuses a
redirect, a URL with credentials or a query, and a vector of the wrong
size, and keeps the key out of error text. With no serve socket,
`openneko web` runs the call in-process. When embedding is off, nothing calls out and
search uses Postgres full-text search.

### The data access interface

The agent's data tools (`neko_graphjin`: the tool list and each tool
call) and the read-query handler reach the customer's data through
`apps/openneko/internal/webfront/turndata.go` (matching `packages/llm/src/data-access`).
`openneko web` checks the run's grants first: a
run whose user holds no data source gets an error, a saved query the user
does not hold returns a refusal, and the read query accepts query
operations only. Then it passes the call to GraphJin: it finds the
org's data source, signs the run's GraphJin token with the user's group
roles, refuses the internal GraphJin, applies the config's write guard
and calls GraphJin's MCP or GraphQL route.

## Two zones

The stack has two zones. The control zone is `openneko web`. It holds
the secret key, the `neko` database login, the provider keys and the key
door. The agent zone is serve and its turns. Serve holds only these:

- the `neko_files_<database>` login. The role can read and write the three
  `file_content_*` tables and nothing else. `openneko serve-init` creates it
  and grants its rights at each start.
- the `neko_subscriptions_<database>` login. The role can read
  `workflow_output` and nothing else, and comes from the same step.
- the token key, `HMAC(secret key, "openneko-serve:v1")`. It checks the
  tokens that `openneko web` signs. It cannot decrypt a stored value.
- a door token for each turn, host call or embedding call, while it runs.
  Serve holds no model key.

The `openneko-serve-init` start step writes the first three into the
`openneko-serve-credentials` volume before serve starts. It runs on every
start, so a new secret key reaches serve on the next start. Serve does not
mount the config volume.

Containers and credentials separate the zones. The network does not: serve
shares the stack network with the database and `openneko web`. The
database grants and the broker's per-run tokens stop serve from acting
outside its zone.

## Serve confines itself (Linux and macOS)

`openneko serve` runs as two processes. The turn part listens on the agent
socket and starts an agent runner for each turn. The network part is a
child, `openneko serve-network`, that runs the files service, the customer
GraphJin, the subscriptions GraphJin and the config check. It holds the
network and the database logins of serve.

The turn part reads the serve credentials folder at start. It gives the
network part the derived token key, the files role database URL and the
subscriptions URL on its stdin, then forgets the key. The network part gets
no credentials path, no sandbox key and no variable that can hold a model
key, a database URL or the secret key. It confines itself before it opens
its database pool or listens, and it tells the turn part its state in one
line. When the network part stops, serve stops, and the supervisor starts
both again. When serve stops, it closes the network part's stdin, and the
network part stops.

In production mode, serve refuses to start without `--credentials`, so no
part of serve holds the secret key or the main database login. After
`openneko serve-init` writes new logins, restart serve.

The network part keeps only these paths:

- read and run: the `openneko` program, the DuckDB program, and the loader
  and library folders that DuckDB needs.
- read: the TLS certificate folders, `/etc/resolv.conf`, `/etc/hosts` and
  the other name files, the time zone files, `/proc`, `/sys/fs/cgroup`,
  `~/.postgresql` and the certificate files named in the database URL.
- read and write: the files folder, the file index, the subscriptions
  folder, the GraphJin config folder, its own temp folder and `/dev/null`.
- listen: the config check socket and the sockets of its two GraphJin
  children.

The turn part keeps the `openneko` program, the same system files, the
agent runs folder, its own temp folder, its own socket, the key door socket and
the sandbox socket. On macOS the turn part is not confined: a process under
a Seatbelt profile cannot start a runner that applies its own profile. Each
runner there confines itself (see "The runner confines itself (macOS)").

Neither part can read the config folder (the secret key, `config.json` with
the database login, `secrets.json`), the serve credentials folder, the web
admin socket token, the rest of the home folder or the other part's
folders. Neither can run a file that it wrote, or read the memory or the
environment of a process outside its domain. At start, serve makes a
private folder `openneko-serve-*` (mode 0700) in its `TMPDIR` (`<data>/tmp`
on a native install). It holds the sockets of the GraphJin children and one
temp folder for each part; each part sets `TMPDIR` to its own and gets no
right on the rest of `TMPDIR`.

### Linux

Both parts use Landlock. The confinement holds for good, for every thread
and every child (GraphJin, DuckDB, the agent runners). It needs no root, no
admin right and no cgo. Landlock domains stack, so each agent runner adds
its own confinement to the turn part's.

Each Landlock ABI adds rights. Serve uses the newest ABI that the kernel
has:

| ABI | Linux | Adds |
|---|---|---|
| 1 | 5.13 | read, write, run, make and remove files and folders; no trace of a process outside the domain |
| 2 | 5.19 | rename and link between folders |
| 3 | 6.2 | truncate |
| 4 | 6.7 | TCP bind and connect (serve does not use it yet) |
| 5 | 6.10 | ioctl on devices |
| 6 | 6.12 | no signal to, and no abstract socket of, a process outside the domain |
| 7 | 6.15 | audit logs |
| 8 | 7.0 | one call confines every thread |
| 9 | 7.1 | connect to named Unix sockets |

Serve uses any ABI from 1. Below ABI 9, Landlock cannot refuse a connect
to a named socket, so the web admin socket also needs a token that `openneko
web` writes beside it (`web-admin.sock.token`, mode 0600). Serve cannot read
that file. From ABI 9, the turn part may connect only to the key door socket
and the sandbox socket, and the network part only to its own sockets.

The config check socket has a folder of its own (`check/check.sock` in the
data folder, `/run/openneko/check/check.sock` in Docker), so the network
part cannot make, replace or remove a file beside the agent socket, the
key door or the sandbox socket. Serve refuses to start when
`OPENNEKO_CONFIG_CHECK_SOCKET` names a folder that holds, or lies above,
one of those sockets or the web admin socket. Below ABI 9 the network part
can still connect to a named socket, so:

- the agent socket reads the peer process of each connection and closes a
  connection from serve itself or any process under it: the network part,
  its children (GraphJin, DuckDB) and the runners. Serve is the child
  subreaper of its processes, so an orphan of the network part (a double
  fork) gets serve as its parent and stays in the tree. A chain of more
  than 64 parents counts as serve's. Serve also refuses a peer that exited
  before the check ended (`SO_PEERPIDFD`, or `pidfd_open` on a kernel
  before 6.5), because its pid can name another process by then. A process
  in another pid namespace, as web in its own container, passes;
- the key door takes only a one-call token of a turn, and the sandbox takes
  only its key or a run's token. The network part holds neither.

On a Linux kernel with no Landlock, both parts start unconfined and say so,
so that data reads, the web app and workflows without an agent step still
work. Serve then refuses every agent turn (see the next section). A kernel
that has Landlock and fails to apply it stops serve in every mode.

Serve's network does not change: Landlock here governs files only.

### macOS

The network part applies a Seatbelt profile to itself with `sandbox_init`,
as the agent runner does. The profile refuses by default. It keeps the
paths above, TCP and UDP to any host, listening on any local address, its
own Unix sockets, the name service socket, and the Mach services that name
lookup and TLS certificate checks use (`com.apple.dnssd.service`,
`com.apple.trustd`, `com.apple.SystemConfiguration.DNSConfiguration` and a
few logging and directory services). It can fork, run the `openneko` and
DuckDB programs, and signal its own children. It binds a Unix socket only
at the real path of each of its own sockets (`/tmp` resolves to
`/private/tmp`). It has the two gaps of the runner: it can read the
metadata of a path that it names, and the arguments, environment and
process record of every process of the same account.

When macOS has no `sandbox_init`, the network part starts unconfined and
says so, as on a Linux kernel with no Landlock. When macOS refuses the
profile, serve stops in every mode. The proof tests
(`TestConfinedServeNetworkPart`, `TestServeNetworkPartFailsClosed`) run on
macOS 15 on every push.

### On every system

No mode, setting, flag or variable turns the confinement off, also in
development mode. `openneko release-check --serve-socket` prints `serve_confinement`
(the turn part), `serve_network_confinement` and `runner_confinement`, and
fails when a part that the system can confine runs unconfined in production
mode. The status page shows the state under "Agent confinement". On
Windows the agent runner is confined (see "The runner is confined
(Windows)"), and the network part of serve is not.

Neither OS can limit the network part to the hosts that the install's
configuration names without root or admin rights. A root-installed systemd
unit can add `IPAddressAllow=`.

## The agent runner

On Linux and macOS, `openneko serve` runs each agent turn in a new
process, `openneko agent-runner`. Serve checks the turn's frame, then
starts the runner with two connected sockets and nothing else: the turn's
link and a broker link. The runner gets no variable of serve's except
`PATH` and `TMPDIR`. It inherits no other descriptor.

The runner opens no connection itself. For each connection to the key door
or the sandbox, it asks serve over the broker link. Serve dials, then
passes the connected socket to the runner (`SCM_RIGHTS`). The broker passes
these two connections only. The runner holds no sandbox key. For the
sandbox, serve opens the turn's run with its key and the org, run label and
egress list of the turn's start frame, and passes a socket to its own gate
for that run. The gate sends on only the exec and the end of that run,
with the run's token. The sandbox refuses an egress list, org or label on
a request with a run token (`apps/ax-harness/sandbox/gate.go`; tests
`TestARunnerCannotChooseTheSandboxEgress`, `TestARunTokenCannotChooseTheEgress`).
In the runner:

- a model or embedding URL that is not a door URL fails the call;
- a sandbox without a passed connection fails the terminal;
- `web_fetch` calls `openneko web` over the tool bridge. Web holds the
  host list of the run, makes the fetch and logs each URL with the
  organization and the run. Web connects only to a public address: it
  refuses private, loopback, link-local, shared (100.64.0.0/10) and NAT64
  (64:ff9b::/96, 64:ff9b:1::/48) addresses. Behind a proxy, web resolves
  the host and refuses it when any of its addresses is not public. The
  proxy resolves the name again, so a name that changes its answer between
  the two lookups is not covered.

Serve writes each line that a runner sends to stderr into its own log as
`agent runner <n>: <line>`, with control characters removed, lines cut at
4 KiB and at most 256 KiB for each turn.

Serve starts each runner in its own process group. On Linux the runner
also gets `SIGKILL` when serve dies. Serve kills the whole group when web
hangs up, when the runner sends its exit frame, and at the turn's deadline:
the spec's `timeout_ms` (at most 30 minutes) and one minute more. Serve
sends the exit frame to web only after it killed the group, and sends one
with code 2 when the runner stopped without one. Web copies the artifacts
and the skill edits back only after that exit frame, and opens each file
with no link followed, checks that the open file is the regular file it
saw, and skips a file with a second name. The broker answers at most 1,024
requests per turn, then closes.
`openneko serve` says on start that it uses the runner, and its status
frame reports `"runner":"process"`.

### The runner confines itself (Linux)

On Linux, the runner reads its setup frame and the turn's start frame, then
limits and confines itself for good, before any agent code runs:

- Limits, each set as both soft and hard limit, so the runner cannot raise
  them: CPU time up to the turn's deadline (`timeout_ms`, at most 30
  minutes, plus one minute); 8 GiB of address space; a file of at most
  128 MiB, the most that one sandbox command may return; 1024 open files;
  256 threads, which the Go runtime enforces (`debug.SetMaxThreads`); and
  `RLIMIT_NPROC` at the account's task count at the start of the turn plus
  256 for each turn that serve runs at once (`OPENNEKO_SERVE_MAX_TURNS`).
  The kernel counts `RLIMIT_NPROC` over every task of the account, also
  the tasks that `/proc` in a container does not show (other containers
  with the same uid, or the host). The runner reads that count from the
  kernel: it lowers its soft limit step by step and tries a fork, which
  fails when the count has reached the limit. A thread flood stops in the
  runner and the account keeps room for web and serve. The Go runtime
  holds each runner to 256 threads, so a flood of Go threads leaves room
  for every runner beside it. Native code is not bound by the Go cap: it
  can take the room of the runners beside it, and their thread starts
  then fail. A runner that cannot start a
  thread ends only its own turn, with the line "the agent runner could not
  start another thread". The kernel does not apply `RLIMIT_NPROC` to root:
  run OpenNeko as another user, or the status page names the gap.
- Landlock keeps read and write on the folders of its turn only (the
  workspace, the uploads, the skills, the run folder and the artifacts, all
  in the turn's staged folder) and read on the time zone files. It keeps no
  right to run a program. From ABI 4 it also refuses every TCP bind and
  connect; from ABI 6 it refuses signals to, and abstract sockets of, a
  process outside its domain.
- A seccomp filter, on every thread with `no_new_privs`, refuses with
  `EPERM`:
  - sockets: `socket`, `socketpair`, `connect`, `bind`, `listen`,
    `sendmsg`, `sendmmsg`, and `sendto` with a destination address;
  - new processes and groups: `fork`, `vfork`, `clone` without
    `CLONE_THREAD` or with a namespace flag, `unshare`, `setns`,
    `setpgid`, `setsid`, and `prctl` `PR_SET_PDEATHSIG`;
  - the reach into other processes: `ptrace`, `process_vm_readv`,
    `process_vm_writev`, `kcmp`, `pidfd_getfd`, `process_madvise`,
    `get_robust_list`, `migrate_pages`, `move_pages`;
  - signals: `kill`, `tkill`, `pidfd_send_signal`, `rt_sigqueueinfo`,
    `rt_tgsigqueueinfo`; `fcntl` `F_SETOWN`, `F_SETOWN_EX`, `F_SETSIG`,
    `F_SETLEASE`, `F_NOTIFY` and `F_SETFL` with `O_ASYNC`; and the ioctls
    `FIOSETOWN`, `SIOCSPGRP` and `FIOASYNC`;
  - shared memory and queues: `shmget`, `shmat`, `shmctl`, the `msg*` and
    `sem*` calls and the `mq_*` calls;
  - scheduling and limits: `sched_setscheduler`, `sched_setparam`,
    `sched_setattr`, `ioprio_set`, `setrlimit`; `prlimit64` on another
    process or with a new limit, `sched_setaffinity` on another process,
    and `setpriority` on anything but itself;
  - file changes that Landlock does not check: `truncate` by path,
    `chmod`, `fchmodat`, `fchmodat2`, the `chown` family, `utime`,
    `utimes`, `futimesat`, `utimensat` on a path, the `setxattr` and
    `removexattr` families, `file_setattr`;
  - path watches: `inotify_init`, `inotify_init1`, `inotify_add_watch`,
    `fanotify_init`, `fanotify_mark`;
  - kernel surface: the `io_uring` calls, `bpf`, `perf_event_open`,
    `userfaultfd` and the key calls; the `TIOCSTI` and `TIOCLINUX` ioctls.

  It allows `tgkill` and `pidfd_open` only for the runner's own process:
  the Go runtime signals its own threads. `fchmod` on a descriptor stays:
  the runner opens files only in its folders, and the sandbox sync sets the
  mode of a file it wrote that way. `clone3` and any call newer than the
  build gets `ENOSYS`, as on a kernel that lacks it: a filter cannot read
  the flags of `clone3`. A call from another architecture ends the process.

  The filter is a list of refused calls, not a list of allowed ones. A
  list of allowed calls would have to hold every call of the Go runtime and
  the standard library on every path, also the error paths that one turn's
  trace does not show; a missing call breaks a turn in production. The
  refused list closes each class of reach outside the turn, and the
  `ENOSYS` rule closes calls that a later kernel adds.

So the runner holds the two sockets that serve passed and the connections
that the broker passes, and can open no other. The broker passes at most
1024 connections a minute to a turn and ends a turn that asks for more. It starts threads only, no
process. It reaches no file of another turn, the config folder or the home
folder, and cannot read, trace, signal or reschedule serve or web, or change
the mode, owner, times or attributes of a file outside its folders.

The seccomp filter closes the gaps of the low Landlock ABIs: truncate (ABI
3), signals and abstract sockets (ABI 6) and connects to named sockets (ABI
9). The runner can read the metadata (size, times, extended attributes) of a
path that it names on every ABI. Two gaps stay below ABI 5: a rename or link
between folders fails (ABI 2), and the runner can send an ioctl to a device
it opened (ABI 5). The lowest kernel for agent turns is Linux 5.13 (ABI 1)
with `landlock` in the active security modules.

Fail closed: when Landlock or seccomp is missing, serve refuses every agent
turn in every mode. Each refused turn, the start of serve, the status
page and `openneko doctor` show one line, for example "agent confinement:
off. Linux 4.18.0 has no Landlock: use kernel 5.13 or later with landlock in
the lsm= boot list, so agent turns are off (INSTALL.md, "Agent
confinement")". The line tells a kernel with no
Landlock apart from a kernel that has it off (`EOPNOTSUPP`) and from a
container engine that blocks the calls. No setting, flag or variable turns
agent turns on without the confinement, in any mode. `openneko release-check
--serve-socket` prints `runner_confinement=landlock+seccomp ABI <n>` and
fails when a turn would run unconfined. A runner that cannot confine itself
ends the turn with an error and runs no agent code.

Proof tests (`apps/openneko/internal/cli/runner_confine_linux_test.go`):
`TestConfinedRunner` runs the checks of the CONF0 probe, and the truncate,
signal, pidfd, scheduling, metadata, watch and datagram checks, inside a
real runner that serve starts, in a one-account layout. It runs them unconfined as a control, confined at the
kernel's ABI and with the ABI capped at 1, and with Landlock or seccomp
left out, which must fail named checks. It also checks each limit, and
with the limits left out each limit check must fail.
`TestRunnerThreadFloodStaysInTheRunner` floods threads in a runner: with
the limits left out the runner starts 400 threads; with them it stops, the
turn ends with the thread line, and the account still starts threads.
`TestAThreadFloodEndsOnlyItsOwnTurn` runs three runners at once: one waits,
one holds 200 threads and one floods; the flood ends only its own turn, and
the first runner then starts 64 threads. Both run on Linux and macOS.
`TestAgentSocketRefusesServesOwnTree` (`serve_peer_linux_test.go`) starts
a serve stand-in whose network part runs one peer as a child and one as a
double-forked orphan: both are refused and web is answered; with no
subreaper the orphan gets in. Web reads a file that a runner wrote
only up to its limit (an artifact 128 MiB, a skill file 16 MiB) and
refuses a sparse file with one line (`TestRunnerFilesAreReadWithALimit`). `TestConfinedServeDoesItsWork`
(`internal/webfront`) runs a whole turn with a fake model through the door
descriptor in a confined runner.

### The runner confines itself (macOS)

On macOS, the runner reads its setup and start frames, then applies a
Seatbelt profile to itself with `sandbox_init` (found at run time in
libSystem, with no cgo). The profile is built for each turn from the same
`confine.Spec` as on Linux. It refuses by default and allows only:

- read and write on the folders of its turn, and read on the time zone
  files;
- read of `/`, `/usr/lib`, `/System/Library`, the dyld cache and cryptexes,
  `/dev/null`, `/dev/urandom` and `/dev/random`;
- the metadata (size, times) of any path;
- sysctl reads;
- signals and process information for itself only.

macOS has no kernel limit on the threads of one process, so the runner
caps its threads at 256 in the Go runtime, as on Linux. It also sets the
CPU time, file size and open file limits; macOS does not enforce an
address space limit.

It allows no network rule, no fork, no program to run and no Mach service.
So the runner holds the two sockets that serve passed and the connections
that the broker passes, and can open no other. The broker passes at most
1024 connections a minute to a turn and ends a turn that asks for more. It cannot read another
turn's files, the config folder or the home folder, and cannot signal serve
or web. macOS matches rules on the resolved path,
so each path goes in the profile both as named and resolved (`/var` and
`/tmp` are links into `/private`). A path with a quote, a backslash or a
control character is refused.

Two gaps: the runner can read the metadata of a path that it names, and
the arguments, environment and process record of every process of the same
account (`kern.procargs2`, `kern.proc.pid`). On macOS 15.8 Seatbelt does not
check a sysctl read by number: a profile that refuses every sysctl but a
named list still lets the runner read them (proved on the `macos-15`
runner). So no OpenNeko process keeps a secret in its arguments or
environment: the supervisor, web, serve, the network part and the GraphJin
child get theirs on stdin or in a start line (INSTALL.md, "Agent
confinement (macOS)"). `TestNoOpenNekoProcessShowsAPlantedSecret` plants
secrets in the shell of `openneko start` and the database URL in its
arguments, runs the hand-over chain, and reads the arguments and
environment of each process with no confinement (`kern.procargs2` on
macOS, `/proc` on Linux).

Fail closed: with no `sandbox_init`, or a profile that macOS refuses,
serve refuses every agent turn in every mode with the same lines as
on Linux (start, status page, doctor, each refused turn, and
`release-check --serve-socket`, which prints `runner_confinement=seatbelt`).

Proof tests, run on the `macos-15` GitHub runner with `CI` set, so they
fail when they cannot run: `TestConfinedRunner`
(`internal/cli/runner_confine_darwin_test.go`) runs the 17 checks of the
CONF0 macOS probe and more (truncate by path, a file it wrote, and the
arguments, environment and record of a web stand-in that holds a planted
secret in each) in a real runner. The test fails when a check of the
stand-in is open and the runner's gaps do not name it, and when macOS
refuses all of them and the gap is still listed. It runs them unconfined as a
control, confined, and then with one rule added at a time; each added rule
must open the checks that it covers, and each check must be covered.
`TestRunnerRefusesATurnWhenItCannotConfine` covers no workspace, no
`sandbox_init` and a refused profile. `TestConfinedRunnerTurn`
(`internal/webfront`) runs a whole turn of the release binary's runner with
a fake model through the door descriptor.

### The runner is confined (Windows)

Windows has no call with which a process confines itself for good, and it
passes no socket to a child. So on Windows serve starts each runner
confined, and the runner reaches serve on two sockets:

- Serve makes a new app container profile for each turn
  (`OpenNeko.Runner.<serve pid>.<random>`), with no capabilities, and
  starts the runner in it. A profile per turn gives each runner its own
  SID: two runners at once cannot open each other, and no runner finds what
  an earlier turn left in the container's own folders. Serve deletes the
  profile when the turn ends. At start it deletes the profiles whose serve
  process has ended.
- The container SID gets read, write and delete on the turn's folders,
  with no `WRITE_DAC` and no `WRITE_OWNER`, so the runner cannot open a
  file that it made to other app containers. It gets read and execute on
  the program file, and connect on the two sockets. Serve takes these
  grants back when the turn ends. Each turn's folder must lie inside the
  agent runs folder. Before the grant, serve removes each junction and
  symbolic link in the turn's folders, so the grant does not follow a link
  that an earlier runner made.
- Serve listens on `link.sock` and `broker.sock` in a new folder that only
  the account, SYSTEM and the container SID can open. It takes a
  connection only from the runner's process, which it checks with
  `SIO_AF_UNIX_GETPEERPID`, and closes the link socket after the first. For
  each connection to the key door or the sandbox, the runner connects to
  the broker socket and names the target. Serve answers `+` and carries the
  bytes both ways, or answers `-` and the reason. The 1,024 requests a
  minute of the broker hold here too.
- A job object holds the runner: one process, killed when serve's handle
  closes, 8 GiB of committed memory, the turn's CPU time, and no
  clipboard, desktop, global atoms or handles of other jobs.
- The runner's environment holds `PATH`, `TMPDIR`, `SYSTEMROOT` and the
  folder of its sockets.

Fail closed: the runner checks its own token before the turn. When the
token is not an app container, or holds a capability, the runner refuses
the turn. A serve that must confine and has no way to start a confined
runner refuses every turn. On a Windows older than 10 1809 or Server
2019, which has no Unix sockets for app containers, serve refuses every
agent turn with one line.

The app container refuses TCP and UDP to loopback and to other hosts, and
name lookup. The proof sends a UDP packet to a receiver on loopback that
the test holds, and a DNS question to 1.1.1.1:53; the receiver gets
nothing and no answer comes back. An unconfined runner does both.

Proof tests, run on the `windows-latest` GitHub runner with `CI` set, so
they fail when they cannot run (`internal/cli/runner_confine_windows_test.go`):
`TestConfinedRunnerWindows` runs the checks in a real runner, first with
no container as a control, where each must succeed, then confined, where
each must fail, and reads the job's limits. `TestWindowsRunnerRefusesATurnOutsideAnAppContainer`
starts the runner with no container and expects the turn to be refused.
`TestWindowsRunnersCannotReachEachOther` runs two turns at once.
`TestWindowsRunnerGetsNoFolderThroughALink` puts a junction to the config
folder in a workspace. `TestWindowsStarterKeepsTheTurnInsideAgentRuns`,
`TestWindowsTurnsAreOffWithNoAppContainer` and `TestStaleRunnerContainersGo`
cover the rest. In `apps/ax-harness/serve`,
`TestWindowsRunnerSocketsTakeOnlyTheRunner` shows that a socket refuses
another process.

Gaps: the runner can read what Windows opens to every app container, such
as the system folders and parts of the registry. Its threads are capped in
the Go runtime only.

Agent runs read customer data only through the direct GraphJin tools. No run
hands a question to GraphJin's own agent, so GraphJin gets no provider key.
Migration 0128 removed the setting that did that. Admin chat plans GraphJin
config changes with OpenNeko's own model: the `graphjin-config` skill reads
the redacted config through `describe_source_config`, and the seed configs
set `agent.enabled: false`. The model fills a typed proposal
(`request_source_config_change`) and never writes YAML. After an admin
approves, `openneko web` applies the change to the running GraphJin and writes it
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
(`POST /api/v1/settings/file-sources`; `openneko web` answers it). Go
writes them to a folder per org and source under `files/` beside the
GraphJin config, or under `OPENNEKO_GRAPHJIN_FILES_LOCAL_DIR`. It refuses
new files for a source that GraphJin already serves or that an admin
approved. The upload and the approval job take the same lock file.

An admin adds an API source from an OpenAPI 3 document in the source form
(`GET` and `POST /api/v1/settings/openapi-specs`; `openneko web` answers
them). The admin uploads a YAML or JSON file of up to 2 MB, or gives an
HTTPS URL. Go fetches a URL only when every address of its host is public,
connects to the address it checked, and follows at most three redirects.
It refuses a document with an external `$ref`, sets the one base URL in
`servers`, counts the read and mutating operations, and stores the
document as YAML with its SHA-256 checksum. The approval job checks
that checksum before it writes the file for GraphJin.

OpenNeko says what a config does in plain sentences
(`apps/openneko/internal/webfront/configdescribe.go`): for each source, its kind,
host and database, whether GraphJin blocks changes, who may read, change and
delete, and which tables are hidden, admin-only or open. `describe_source_config`
returns them as `summary`, and Admin settings, GraphJin config shows them.
A valid preview also carries `summary` with the touched sources and roles
before and after the change, and their redacted YAML. The approval card shows
the sentences side by side and keeps the YAML diff behind a disclosure.

Admin settings, GraphJin config also shows the config file as read-only YAML
(`readGraphjinFileView` in `apps/openneko/internal/webfront/admingraphjin.go`). The page names the file's
path on the server and GraphJin's state from `.openneko-graphjin-state`:
running, or restarting with the reason. The same redaction code as
`describe_source_config` hides the secrets, and the view drops comments. The
page has no edit control and no route behind it. Admins edit the file on the
server and run `openneko graphjin check`.

A database source needs a login with a password. OpenNeko refuses a
proposal with no saved password, or with a saved password that is empty, at
the answer check and again at apply. The password goes to GraphJin only. The
control plane replaces it with `[REDACTED]` in every
error text, so GraphJin's errors cannot carry it to the model, a log line or
the thread.

The source check (`apps/openneko/internal/sourcecheck` and `apps/openneko/internal/webfront/sourcecheck.go`) tests one
PostgreSQL, MySQL or MariaDB source in order: the host resolves, the port
answers, TLS, the login, the rights to read the schema, and read-only rights.
It stops at the first failed step. Each failure names the answer at fault and,
where SQL fixes it, gives the SQL for the database administrator. A passed
check lists the tables the login can read, with the planner's row estimates.
The check runs in the control zone, because it needs the stored password.
Three callers use it: the `check_source` tool (through the broker, admin runs
only), the "Check connection" part of the source form
(`POST /api/v1/settings/source-check`, admins only), and `openneko source check`
(`POST /admin/sources/check` on the admin socket of `openneko web`). The form shows the tables with all
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

In prod mode, steps 1, 2, 3 and 6 and `web` read OpenNeko's database
URL from the Compose secret `database-url`. Serve does not get it. When the
URL names TLS files, start writes `database-tls.yml`, which gives the same
services and serve a copy of each file as a Compose secret. Dev, demo
and a legacy prod install load `bundled-db.yml`, which adds `neko-db` and
points the same services at it.

## The customer GraphJin

The network part of serve runs the customer GraphJin from the pinned
GraphJin module (v3.21.18) as a child process of the same executable:
`openneko graphjin serve --child`. The child keeps the network part's
confinement.
A GraphJin crash ends only the child. Serve listens on port 8080 and answers as `graphjin` on the
stack network, so stored endpoints such as `http://graphjin:8080` keep
working. Its config, `agentic.yml`, holds the customer's source logins and the
per-org JWT secret. It is in the agent zone, as it was when GraphJin had its
own container.

- `openneko-serve-init` creates `agentic.yml` from the seed in the serve image
  when the file is missing, with a new keystore key. It then finds or creates
  the organization row and writes the per-org JWT secret, the write policy and
  the `system` block into the file. GraphJin reads auth keys only when it
  starts, so `openneko start` runs this step before it starts serve and
  `web`.
- When `openneko web` changes the config, it writes a new token to
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
  `GJ_*`, `OTEL_*` and `OPENNEKO_OTEL_ENABLED` for its trace exporter,
  `GO_ENV`, `PATH`, `HOME`, `TZ`, the locale, the CA file paths and the Go
  runtime settings (`ChildEnv` in `apps/openneko/internal/gjhost/supervisor.go`).
  An OTLP header variable such as `OTEL_EXPORTER_OTLP_HEADERS` reaches the
  child too. It gets no serve credentials path, no agent
  socket and no sandbox key. It runs as the same user in the same
  container, so it can still read files that serve can read. Those files
  hold agent-zone secrets only, so this crosses no zone line.

## The subscriptions GraphJin

Serve also runs the GraphJin that serves the workflow output
subscriptions of `openneko web`, on port 8089. It is a second child process of the same kind,
with the same restarts, 503 answer and state file.

- It connects as `neko_subscriptions_<database>`. Serve reads the role's URL from
  `subscriptions.url` in the credentials folder at start and gives it to the
  network part, which writes the GraphJin config into a private folder
  (`OPENNEKO_SUBSCRIPTIONS_DIR`, default `subscriptions` in the data
  folder, mode 0700). A new URL takes effect when serve restarts. A URL for another
  role is refused.
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

In the Compose overlay and in `compose.native.yml` the sandbox container:

- sits **only** on its own network, `openneko-sandbox`, which is internal:
  no route out and no DNS for external names. Commands reach no service of
  the stack, no host on your network and no internet host, except through
  the egress proxy;
- drops every capability except `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `KILL`,
  `SETGID`, `SETUID`, `NET_ADMIN` and `SETPCAP`, and sets
  `no-new-privileges`. The sandbox uses the last two only at start: it sets
  a firewall in its own network namespace that lets the run users send
  packets only to its relay to the egress proxy, then drops both from every
  set. It does not start when it cannot do this;
- mounts only the sockets folder and its own volumes for run folders and
  homes. It mounts no configuration, no secret, no Docker socket and no
  folder of the stack.

Commands reach no host by default. An admin lists the hosts that an
organization's commands may reach at Admin, Settings, Agent; the list is
empty by default. The egress proxy is the only way out of the sandbox
network. Each run gets its own token for the proxy, and the token fails when
the run ends. The proxy allows only the run's hosts and ports, refuses a
name that resolves to a loopback, private, link-local or metadata address,
and logs each request with the run and the organization. A change of the
list goes in the audit chain as `agent:egress_changed`. The rules and the
log are in `recipes/sandbox/README.md`, "Egress".

The sandbox reports in `GET /v2/check` whether runs can get out: `closed`
needs the firewall in place, no default route, and a quiet probe. A probe
runs as a run user at start and every minute; a name that resolves or an
address that answers sets `open`.
Web gives a run the terminal only when the answer is `closed`, or when the
operator sets `OPENNEKO_SANDBOX_EGRESS_BY_OPERATOR=1` for a sandbox on
another host. Web asks again every 30 seconds and records each change as
`sandbox:egress_state` in the audit chain. `openneko release-check
--sandbox` prints `sandbox_egress` and fails on `open` and `unknown`; the
status page shows the Sandbox card as failing.

## Shared folders

- `openneko-agent-runs` → `/var/lib/openneko/agent-runs`, owned by
  `1001:1001`. `web` and serve share it. The sandbox does not
  mount it.
- `openneko-run` → `/run/openneko`, `1001:1001`, mode `0770`. It holds
  `serve.sock`, and `check/check.sock` in a folder of its own: the network
  part of serve gets rights on that folder only. The sandbox does not
  mount it.
- `openneko-sandbox-socket` → `/run/openneko-sandbox`, with the sandbox
  overlay only. It holds `sandbox.sock` (`root:1001`, mode `0660`). The
  sandbox, `web` and serve mount it; `web` joins group `1001` for it.
- `openneko-egress-control` → `/run/openneko-egress`, with the sandbox
  overlay only. Root owns the folder (mode `0700`) and the control socket
  (mode `0600`). Only the sandbox and the egress proxy mount it.

## Configuration

Compose sets all of these. They matter only for custom deployments.

| Variable | Where | Meaning |
|---|---|---|
| `OPENNEKO_SERVE_SOCKET` | web, serve | the agent socket of serve (`/run/openneko/serve.sock`) |
| `OPENNEKO_SERVE_MAX_TURNS` | serve | turns that run at once (default 32) |
| `OPENNEKO_AGENT_RUNS_DIR` | web, serve | where runs are staged; default `agent-runs` in the data folder (`~/.local/share/openneko` on Linux) |
| `OPENNEKO_SANDBOX_URL` | web, serve | the sandbox service (`unix:///path`, `http://` or `https://`); empty turns the terminal off |
| `OPENNEKO_SANDBOX_KEY` | web, serve, sandbox | the bearer key of the sandbox service |
| `OPENNEKO_SANDBOX_EGRESS_PROXY` | sandbox | the egress proxy that the sandbox's relay at `127.0.0.1:3128` passes commands to (`openneko-egress:3128`); empty gives commands no proxy |
| `OPENNEKO_SANDBOX_EGRESS_CONTROL` | sandbox | the control socket that the proxy asks (`unix:///run/openneko-egress/control.sock`) |
| `OPENNEKO_GRAPHJIN_CONFIG` | web, serve, serve-init | the customer GraphJin config (`/config/graphjin/agentic.yml`); empty runs no GraphJin in serve |
| `OPENNEKO_GRAPHJIN_ADDR` | serve | where the customer GraphJin listens (default `127.0.0.1:8080`; Compose sets `0.0.0.0:8080`) |
| `OPENNEKO_GRAPHJIN_SEED` | serve-init | the template for a new config; the serve image sets it |
| `OPENNEKO_SUBSCRIPTIONS_ADDR` | serve | where the subscriptions GraphJin listens (default `127.0.0.1:8089`; Compose sets `0.0.0.0:8089`); empty runs none |
| `OPENNEKO_SUBSCRIPTIONS_DIR` | serve | the private folder for its config and state |
| `OPENNEKO_GRAPHJIN_URL` | web | the subscriptions GraphJin (`http://openneko-serve:8089`) |

`openneko web` checks that serve answers on its socket before setup
completes and before onboarding work starts. When serve does not answer, it
reports that the agent runtime (`openneko serve`) is unavailable.

## Development

`pnpm dev` runs `openneko serve` on the host next to `openneko web`
(`scripts/dev-agent.sh`). That serve runs no GraphJin. The customer GraphJin
runs in the `openneko-serve` container that `pnpm dev:up` starts, on
`localhost:8080`. To try the terminal, run the sandbox recipe and set
`OPENNEKO_SANDBOX_URL`.
