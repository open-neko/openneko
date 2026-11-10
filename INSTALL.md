# Install OpenNeko

Sections below **Command reference** are reference — not needed to get started.
What you're installing, feature by feature, in plain language: **[FEATURES.md](FEATURES.md)**.

## Requirements

- Docker Desktop on macOS, or Docker Engine + Docker Compose on Linux
- An API key for at least one supported model provider

## Install

```bash
curl -fsSL https://openneko.app/install.sh | sh
mkdir -p ~/openneko && cd ~/openneko
openneko setup --mode demo
```

The installer picks the right path for your platform — Homebrew on macOS, a checksum-verified release binary on Linux — and checks for Docker. `openneko setup` then runs preflight (Docker daemon up, host supported, ports free), brings up the stack, and configures it (see [Setup](#setup)).

`--mode demo` pulls pinned images and loads AdventureWorks sample data with three example watchers. For your own data, use `--mode prod` ([Use your own data](#use-your-own-data)).

### Manual install

Prefer to manage the binary yourself:

- **macOS (Homebrew):** `brew install open-neko/tap/openneko`
- **Linux:** download `openneko_<version>_linux_<arch>.tar.gz` from the [latest release](https://github.com/open-neko/openneko/releases/latest), verify it against `checksums.txt`, and put `openneko` on your `PATH`.

Then run `openneko setup` (guided), or `openneko start` to bring the stack up without the guided flow.

## Setup

`openneko setup` walks through the steps below **in the terminal**. Prefer a browser? Choose **browser** at the first prompt (or pass `--skip-onboarding`, or just run `openneko start`) and open [http://localhost:3000](http://localhost:3000) for the same wizard. Either way:

1. Choose an admin database password.
2. Confirm the pre-filled GraphJin URL (`--mode demo`) or enter your own (`--mode prod`).
3. Pick the model provider the agent should use (Anthropic / OpenAI / Google / Ollama / others). The agent (Ax) runs in `openneko serve` with the full tool set, including the chat-first management tools. It gets a terminal only when you connect a [sandbox](#sandbox-optional).
4. Add your provider API key.
5. Add an industry research provider, or skip.

Both paths drive the same web endpoints with the same validation (a live provider-key test, a data-source connectivity check), and each step persists independently — so you can start in the terminal and finish in the browser, or vice versa. For unattended installs (CI), pass `--admin-password`, `--provider`, `--provider-key`, etc. to run setup headless.

Integrations come from **solution packs**, installed after setup from **Admin → Settings → Solution packs** or with `openneko pack install`. Slack, Telegram and the HUD are built-in channels that a pack turns on; sign-in (Scalekit SSO or email link) is configured under **Admin → Settings**. (`--skip-plugins` and `--plugins` are still accepted by `setup` but do nothing.)

The AdventureWorks seed pre-fills business onboarding (`AdventureWorks Cycles`, fiscal year `July`, seats `CEO`/`CFO`/`COO`, priorities `Defend wholesale margins` / `Grow DTC in Europe`). Otherwise fill it in at `/onboarding`.

## Multiple customer instances on one host

Give every customer a stable lowercase instance name. A named installation gets
its own Compose project, databases and named volumes, encrypted config and
secrets, backup namespace, runtime markers, and Docker network.

```bash
# Bind customer apps to loopback when Caddy, nginx, or another TLS proxy is
# the public entry point. Setup persists the port.
openneko --instance acme setup \
  --mode prod \
  --port 3101 \
  --bind-address 127.0.0.1

openneko --instance globex setup \
  --mode prod \
  --port 3102 \
  --bind-address 127.0.0.1
```

Route a separate TLS hostname to each loopback web port. For example, with
Caddy:

```caddyfile
acme.example.com {
  reverse_proxy 127.0.0.1:3101
}

globex.example.com {
  reverse_proxy 127.0.0.1:3102
}
```

Only the reverse proxy needs public `80`/`443` firewall access. The customer web
ports above stay on host loopback. Databases, GraphJin, brokers, the file
service and the agent runtime stay inside their private Docker networks.

For named instances, omitting `--port` makes setup choose an available port
and persist it. Inspect
the resulting inventory with:

```bash
openneko instances
```

Target every later operation explicitly:

```bash
openneko --instance acme status
openneko --instance acme logs -f
openneko --instance acme upgrade
openneko --instance acme backup
openneko --instance acme stop
```

Named settings live under
`~/.config/openneko/instances/<name>/installation.json`; durable runtime state
lives under
`${XDG_STATE_HOME:-$HOME/.local/state}/openneko/instances/<name>/`. Do not reuse
an instance name for two customers on the same Docker daemon. Each customer is
still a complete OpenNeko stack, so size VM memory, disk, and backup retention
for the number of concurrent instances.

Named instances are an operational and data-placement boundary, not a
hypervisor security boundary. A host administrator or anyone with Docker daemon
access can reach every stack. Use a separate VM or equivalent tenant boundary
when a customer contract, threat model, or compliance regime requires host-level
isolation.

## Use your own data

```bash
mkdir -p ~/openneko && cd ~/openneko
openneko setup --mode prod
```

Enter your GraphJin base URL when setup asks for it (terminal or browser). If GraphJin runs on your host, use `http://host.docker.internal:8080` — OpenNeko appends the GraphQL and MCP paths automatically.

## Solution packs and skills

```bash
openneko pack list
openneko pack install magento
openneko install https://github.com/<owner>/<repo>#<path/to/skill>   # community skill (admin opt-in)
```

Packs add sources, actions, workflows and skills, and can turn on the built-in Slack, Telegram and HUD channels. See **[PACKS.md](PACKS.md)**. `openneko doctor` checks your host can run the stack.

---

*Reference below — not needed to get started.*

## Command reference

```bash
openneko [--instance name] setup [--mode prod|dev|demo] [--port N] \
  [--bind-address IPv4]
openneko instances                 # configured host installations
openneko start [--mode prod|dev|demo] [--detach]
openneko upgrade [--version vX.Y.Z] [--mode auto|prod|dev|demo]
openneko status                    # docker compose ps proxy
openneko logs [service…] [-f]
openneko stop [--volumes]          # --volumes wipes data
openneko migrate                   # apply pending migrations against running neko-db
openneko serve [--agent-socket path]  # user files and file search; agent turns on a Unix socket when set (runs as the openneko-serve container)
openneko seed adventureworks       # one-shot demo data load (already done by --mode demo)
openneko [--instance name] graphjin import <customer-config.yml|->
openneko reset [--all]             # tear down + clear local config (--all also wipes secrets)
openneko doctor                    # host check, docker daemon, stack health
openneko version
```

Modes:

- **`prod`** (default) — the production control, customer-data, file, and
  agent runtime services. Six containers remain running after the six startup
  jobs complete.
- **`dev`** — source-checkout workflow: use `pnpm dev:setup` for Docker-only dependencies plus AdventureWorks demo data, then `pnpm dev` for hot-reloaded web/worker from source.
- **`demo`** — core + AdventureWorks: `adventureworks-db`, `adventureworks-init`, `neko-adventureworks-seed`. The full live-trial flow (continuous order trickle + scenario injector) lives in `compose.adventureworks.yml` and needs the [Build from source](#build-from-source-advanced) path.

`~/.config/openneko/compose.override.yml` is auto-applied if present (last `-f` to docker compose).

`graphjin import` accepts a YAML fragment with `sources`, `tables`, and/or
`relationships`. Use `-` to read it from standard input. It requires a running
production installation, validates the change through GraphJin, seals source
credentials, and preserves OpenNeko-managed authentication and keystore settings.

### Audit logging failure alerts

Every failed tamper-evident audit append emits a structured
`security.audit_logging_failure` event to stderr. The worker also exposes
process-local state at `GET /health/security`; monitor for
`status: "degraded"` or `auditLogging.healthy: false`.

For immediate delivery independent of PostgreSQL, configure an HTTPS webhook
on both processes that can write audit events:

```yaml
# ~/.config/openneko/compose.override.yml
services:
  web:
    environment:
      OPENNEKO_AUDIT_FAILURE_WEBHOOK_URL: https://alerts.example/audit-failure
      OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN: ${OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN}
  worker:
    environment:
      OPENNEKO_AUDIT_FAILURE_WEBHOOK_URL: https://alerts.example/audit-failure
      OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN: ${OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN}
```

The token is optional and is sent as a bearer token. Webhook delivery has a
two-second timeout, is fire-and-forget, and never blocks the governed operation.
Alert delivery failures are themselves emitted as structured critical stderr
events.

### Agent runtime

Web and worker start one agent runner per run. The runner stages the run's
files in a shared folder and sends each turn to `openneko serve` over a Unix
socket. The model key travels with the turn, and serve keeps every `*_API_KEY`
variable out of the agent's tools. The agent reads and writes files in its
run folder and reaches data only through the OpenNeko broker. The agent has
a terminal only when a sandbox is connected.

### Sandbox (optional)

OpenNeko runs no command for the agent. To give the agent a terminal, and to
turn on workflow script steps and the 13 bundled skills that run scripts
(xlsx, docx, pptx, pdf, document-extraction, skill-creator, excel-author,
pptx-author and the five financial-model skills), connect a sandbox service.
Give web, worker and `openneko-serve` its URL and key, then run
`openneko stop` and `openneko start`:

```yaml
# ~/.config/openneko/compose.override.yml
x-sandbox: &sandbox
  OPENNEKO_SANDBOX_URL: unix:///run/openneko/sandbox.sock   # or http(s)://host:port
  OPENNEKO_SANDBOX_KEY: <a long random string>
services:
  openneko-serve:
    environment: *sandbox
  web:
    environment: *sandbox
  worker:
    environment: *sandbox
```

Without a URL the agent has no terminal, the script skills are hidden from
every run, and a workflow script step fails with a message. The sandbox
gets each command, the run folder and the skill folder. It never gets a
model key, the broker token or a database password. The first sandbox
service is `recipes/sandbox`. It needs the `openneko-agent-runs` volume, so
it runs on the same host as the stack; see `recipes/sandbox/README.md`.

### Semantic search (optional)

OpenNeko finds memories and skills with Postgres full-text search. To find
them by meaning as well, connect an OpenAI-compatible embedding service. Give
web and worker the same values, then run `openneko stop` and `openneko start`:

```yaml
# ~/.config/openneko/compose.override.yml
x-embedding: &embedding
  NEKO_EMBEDDING_URL: https://embeddings.example/v1   # OpenNeko calls <url>/embeddings
  NEKO_EMBEDDING_MODEL: text-embedding-3-small
  NEKO_EMBEDDING_API_KEY: ${NEKO_EMBEDDING_API_KEY}   # optional
  NEKO_EMBEDDING_DIMENSIONS: "768"                    # optional
services:
  web:
    environment: *embedding
  worker:
    environment: *embedding
```

Semantic search is off until both the URL and the model are set. When it is
on, web and worker send memory, skill and Library text to that service. A
background job embeds stored rows; until it reaches a row, and whenever the
service fails, search uses full-text search. A change of model re-embeds
every row. To run a model on your own machine, see
`recipes/embedding-gemma/README.md`.

### File search

The `openneko-serve` container keeps each user's files in a private folder
on the `openneko-files` volume. The assistant gets three tools over the files
of the user who owns the run:

- search: keyword search over PDF, Word, PowerPoint, Excel, CSV, Markdown and
  text files, with the matching passages
- read text: the text of one file, with page, slide and sheet markers
- query sheet tables: one read-only DuckDB query over the sheets of
  spreadsheet and CSV files

The keyword index is in `neko-db`. The sheet tables are Parquet files on the
same volume. OpenNeko rebuilds both from the files, so you can delete them.
DuckDB runs with external access off and reads only the tables of that user.
Web and worker reach the service at `OPENNEKO_SERVE_URL` with a token that
expires after one minute. When `OPENNEKO_SERVE_URL` is not set, the assistant
does not get the file tools.

`openneko-serve` connects to `neko-db` as `neko_files`. That role can read
and write only the file index tables. The startup job `openneko-serve-init`
sets its password, derived from the secret key, and gives the service a key
that checks the tokens. The service never gets the secret key or the `neko`
password. On a database you manage yourself, give the `neko` login the
`CREATEROLE` right, so it can create `neko_files` and set its password.

Files a user adds to the Library go into that folder. When `OPENNEKO_SERVE_URL`
is not set, the Library refuses uploads. After an upgrade, the worker moves
Library files from older releases into the folder once, at start. It marks a
document as failed when its original file is gone.


## Backup and restore

`openneko backup` writes one encrypted file while the stack runs. The file
holds a `pg_dump` of `neko-db` and the config and file volumes
(`openneko-config`, `openneko-graphjin-cli` and `openneko-files`). Files go to
`${XDG_DATA_HOME:-$HOME/.local/share}/openneko/backups/`. Use `--to` or
`OPENNEKO_BACKUP_DIR` to write them somewhere else, such as a NAS.

```bash
openneko backup
openneko restore <file> --confirm RESTORE
```

Restore decrypts and checks the whole file before it changes anything. Then it
stops the stack, replaces the volumes, recreates the database and starts the
stack again. Migrations bring an older backup forward. `openneko doctor`
warns when the latest backup is missing or more than a week old.

Each installation has its own backup key. Keep a copy away from the backups:

```bash
openneko backup key export --to /secure/off-host/openneko-backup.key
openneko backup key adopt --from /secure/off-host/openneko-backup.key
```

On a new host, `adopt` installs the copied key. You can also pass
`--key-file` to a single restore. A file is AES-256-GCM in authenticated
chunks, so a wrong key, a cut file or any edit fails the check.

## Upgrade

`openneko upgrade` is the normal stack upgrade path. It:

1. Takes an `openneko backup` of the running stack and stops if the backup fails. Pass `--skip-backup` to upgrade without one.
2. Pulls the latest OpenNeko service, agent, and sandbox supervisor images.
3. Detects the mode recorded by your last `setup` or `start` (`prod`, `dev`, or `demo`) and recreates that stack detached.
4. Re-runs every idempotent initializer against the existing volumes, including runtime-artifact repair and pending migrations.
5. Waits for every declared service health check; a crash loop or unreachable data plane fails the upgrade instead of being reported as success.
6. Persists the new version and removes old OpenNeko image tags only after the stack is healthy (unless you pass `--no-prune`).

Run it from the same install directory you use for `openneko start`:

```bash
cd ~/openneko
openneko upgrade
```

Install a specific image version/tag:

```bash
openneko upgrade --version v1.18.0
# "1.18.0" is accepted too and is normalized to "v1.18.0".
```

Older installs without a saved mode marker are detected from their existing
Docker Compose project (`openneko`, `openneko-dev`, or `openneko-demo`). If more
than one OpenNeko stack exists on the machine, pass the intended mode explicitly:

```bash
openneko upgrade --mode demo
```

The command persists the selected image tag in `.openneko/runtime/.image-version`,
so later `openneko start` runs the same tag. Upgrading the host binary is still
recommended when a release includes CLI changes.

### macOS

```bash
brew update
brew upgrade openneko
cd ~/openneko
openneko upgrade
```

> **Installed before 1.17.2?** Those releases shipped a Homebrew *formula*; 1.17.2+ ships a *cask*, so `brew upgrade openneko` won't move you across. Switch once:
>
> ```bash
> brew uninstall --formula openneko
> brew install --cask open-neko/tap/openneko
> ```
>
> After that, `brew upgrade openneko` keeps the cask current as normal.

### Linux

```bash
TAG=$(curl -fsSL https://api.github.com/repos/open-neko/openneko/releases/latest | grep -oE '"tag_name": *"[^"]+"' | head -1 | cut -d'"' -f4)
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
curl -fsSL "https://github.com/open-neko/openneko/releases/download/$TAG/openneko_${TAG#v}_linux_$ARCH.tar.gz" | tar -xz openneko
sudo install -m 0755 openneko /usr/local/bin/ && rm -f openneko
cd ~/openneko
openneko upgrade
```

### What `openneko start` does

1. **Migrate.** Connects to `neko-db`, takes a Postgres advisory lock, applies pending embedded migrations, releases the lock. Idempotent.
2. **Compose up.** Writes embedded compose files to `.openneko/runtime/` and runs `docker compose up`. Docker pulls the pinned image tags.

`openneko stop` doesn't touch volumes. Use `--volumes` for a clean slate (wipes the metadata DB, demo state, agent workspaces).

## Reset

```bash
openneko stop --volumes   # wipes data; keeps secrets
openneko reset --all      # wipes everything including secrets
```

## Ports

The packaged stack publishes one host port: the web app, `3000` by default.
Postgres, GraphJin, the worker broker, the file service, the agent runtime and
the demo data services stay on the private Docker network.

Configure and persist the port during guided installation:

```bash
openneko setup --mode prod --port 3001
```

Setup still accepts `--openshell-port` and `--docker-subnet` from older
automation and ignores them. For the backward-compatible unnamed installation,
`OPENNEKO_PORT` remains available as a one-process environment override. Named
instances use their persisted assignments; re-run setup with explicit flags to
change them. `--bind-address 127.0.0.1` restricts the web listener to the host
for a reverse-proxy deployment.

## Build from source (advanced)

> Needed for developing OpenNeko itself or running the full live trial (continuous order trickle + scenario injector).

```bash
git clone https://github.com/open-neko/openneko.git
cd neko
docker compose -f compose.yml -f compose.adventureworks.yml up -d --build
docker compose -f compose.yml -f compose.adventureworks.yml run --rm neko-adventureworks-seed
```

### Live trial data

The source compose trickles fresh sales orders into the sample DB every 10 minutes. Briefing numbers drift, cron workflows fire, runs accumulate on `/runs`. External actions use their configured adapters and approval policies.

```bash
AW_SIM_INTERVAL_SEC=300 AW_SIM_ORDERS_MIN=1 AW_SIM_ORDERS_MAX=5 \
  docker compose -f compose.yml -f compose.adventureworks.yml up -d
```

Disable the trickle: `AW_SIM_ENABLED=0`.

### Watch the loop fire end-to-end

The seed pre-loads three watchers:

- **Daily Revenue Health Check** (9am cron) — yesterday's revenue vs trailing 7-day average.
- **Revenue Drop Alert** (hourly) — per-territory current hour vs same-hour-of-week baseline over 4 weeks; proposes a Slack alert if any territory falls below 50%.
- **Slow-Ship Operations** (8:30am cron) — orders stuck in *pending* > 5 days.

To see the loop without waiting for an organic dip, fire the Germany scenario (stops new orders for territory 8 for three hours):

```bash
docker compose -f compose.yml -f compose.adventureworks.yml \
  exec adventureworks-scenario-injector \
  /scripts/scenario-injector.sh fire germany-revenue-drop
```

Wait ~15 minutes, then click **+ Run now** on **Revenue Drop Alert** in `/workflows`. A finding lands on the Briefing; a proposed Slack alert queues for approval. Click approve; the receipt lands under **Fired on your behalf**.

Then write your own watcher from `/work`, and swap AdventureWorks for your data — see [Use your own data](#use-your-own-data).

## Developer setup

Stack pieces in Docker, app processes from source:

```bash
corepack enable
pnpm bootstrap
pnpm dev:setup
pnpm dev
```

Web and worker run on the host with hot reload. Everything else runs in Docker.

- `pnpm dev:up` starts the backing services with `compose.dev.yml`: `neko-db`, `neko-graphjin`, the customer GraphJin, and AdventureWorks with its simulator. It installs the pinned GraphJin CLI into `.openneko/dev/bin`, rebuilds a GraphJin image that reports another version than the pin, runs migrations from source, and writes the GraphJin JWT secret.
- `pnpm dev:seed` seeds demo workflows. `pnpm dev:setup` runs both steps.
- `pnpm dev` runs `next dev` on port 3000 and `tsx watch` for the worker. Edits to `apps/web`, `apps/worker`, and workspace packages reload without an image build.
- `scripts/dev-env.sh` holds the host settings. Each service a host process calls has a loopback port; `pnpm dev:web` and `pnpm dev:worker` source the file.
- The customer GraphJin runs in sources mode from `db/graphjin/dev.sources.example.yml`. Its config lives in `.openneko/dev/graphjin`, and the worker manages it like the core stack.
- OpenNeko state uses your XDG config home. Set `OPENNEKO_DEV_CONFIG_HOME=$PWD/.openneko/dev/config` to keep a worktree's stack separate, and a distinct `COMPOSE_PROJECT_NAME` with its ports when two stacks run at once.

Without `neko-graphjin`, subscription-chained workflows stay silent (`subscription manager ready (0 active)` in worker logs).

Install host-only CLIs the worker shells out to (Docker images already include them):

```bash
./scripts/install-clis.sh
```

Installs the GraphJin CLI.

In the dev flow, use `http://localhost:8080` for customer-data GraphJin in the setup wizard.

### Working on the openneko binary

```bash
cd apps/openneko
go test ./... -count=1
go test -tags=integration -count=1 -timeout 10m ./internal/db/...   # pgvector via testcontainers
go build -o /tmp/openneko ./cmd/openneko
```

Sync embedded migrations after editing `db/migrations/`:

```bash
apps/openneko/scripts/sync-migrations.sh
```

CI runs this with `--check` and fails on drift.

## Troubleshooting

**Docker not running.** Start Docker Desktop or the daemon; re-run `openneko start`.

**Port in use.** Re-run setup with `--port`, or use the `OPENNEKO_PORT`
environment override.

**Image pull `unauthorized`.** Confirm packages are public at https://github.com/orgs/open-neko/packages.

**Worker crashes on boot.** Check `openneko logs worker`. `ERR_MODULE_NOT_FOUND` for a workspace dep means an old binary — `brew upgrade openneko` (≥ 1.7.3).

**GraphJin connection fails.** `--mode demo` uses internal `http://graphjin:8080` (pre-filled). Dev mode (`pnpm dev`) on the host uses `http://localhost:8080`.

**Workflow subscriptions don't fire.** `neko-graphjin` must be healthy. The worker logs `subscription manager ready (N active)` on boot — if `N=0` despite subscriptions in the DB, GraphJin is unreachable or its password drifted. Rotate via `/setup`, then `openneko stop && openneko start`.

**Provider key fails.** Confirm it's active, has billing/quota, and matches the provider in `/settings`.

**Stale `pnpm dev` processes hammering port 5432.** `tsx watch` processes survive `openneko stop` and reconnect with a stale password (`password authentication failed for user "neko"` in logs):

```bash
pkill -f "tsx.*apps/worker/src/index"
pkill -f "tsx.*apps/web"
openneko stop --volumes && openneko start --mode demo --detach
```

**Clean slate.**

```bash
openneko reset --all
brew uninstall openneko && brew untap open-neko/tap   # if also reinstalling
pkill -f "tsx.*apps/(worker|web)"                     # if you ever ran pnpm dev
```
