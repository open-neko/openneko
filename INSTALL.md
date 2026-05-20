# Install OpenNeko

The recommended install is the single `openneko` Go binary. It supervises the whole stack via `docker compose` and supports three modes: `prod` (core only), `dev` (core + dev tooling), `demo` (core + AdventureWorks trial bundle).

## Requirements

- Docker Desktop on macOS, or Docker Engine + Docker Compose on Linux
- An API key for at least one supported model provider

## Quick install (Homebrew, macOS)

```bash
brew install open-neko/tap/openneko
mkdir -p ~/openneko && cd ~/openneko
openneko start --mode demo --detach
```

What the binary does:

- materializes its embedded compose into `.openneko/runtime/` in the current dir
- pulls the pinned image versions from `ghcr.io/open-neko/neko-*` (multi-arch — native arm64 on Apple Silicon, native amd64 on Linux)
- brings `neko-db` up and runs all SQL migrations in-process (no separate migration container)
- starts `neko-graphjin`, `web`, and `worker`
- in `--mode demo`: also brings up the AdventureWorks Postgres, runs the data loader once (loads ~68 tables), and seeds three pre-installed workflows against the demo data

Open [http://localhost:3000](http://localhost:3000) and finish the setup wizard.

## Quick install (Linux)

Download the matching tarball from [the latest release](https://github.com/open-neko/neko/releases/latest) and drop `openneko` somewhere on your `PATH`:

```bash
curl -fsSL https://github.com/open-neko/neko/releases/latest/download/openneko_$(uname -s | tr A-Z a-z)_$(uname -m | sed s/x86_64/amd64/).tar.gz \
  | tar -xz openneko
sudo install -m 0755 openneko /usr/local/bin/
mkdir -p ~/openneko && cd ~/openneko
openneko start --mode demo --detach
```

## Operating the stack

```bash
openneko start [--mode prod|dev|demo] [--detach]
openneko status                    # docker compose ps proxy
openneko logs [service…] [-f]      # tails logs
openneko stop [--volumes]          # --volumes wipes data
openneko migrate                   # apply pending migrations against running neko-db
openneko seed adventureworks       # one-shot demo data load (already done by --mode demo)
openneko reset [--all]             # tear down + clear local config (--all also wipes secrets/marketplaces)
openneko doctor                    # host check, docker version, manifest state
openneko version
```

Modes:

- **`prod`** (default) — core services only: `neko-db`, `neko-graphjin`, `web`, `worker`.
- **`dev`** — core + dev tooling. Empty overlay today; reserved for future dev-only services.
- **`demo`** — core + AdventureWorks: `adventureworks-db`, `adventureworks-init` (one-shot CSV loader), `neko-adventureworks-seed` (one-shot workflow seeder). The embedded demo bundle currently doesn't include the continuous order trickle or scenario-injector — those live in the repo-root `compose.adventureworks.yml` and need the *Build from source* path for the full live-trial flow.

A user-level compose override at `~/.config/openneko/compose.override.yml` is auto-applied when present (last `-f` to docker compose).

## Setup wizard

1. Choose an admin database password.
2. If you ran `--mode demo`, confirm the pre-filled GraphJin data source. If you started in `--mode prod` with your own data, enter your GraphJin URL.
3. Pick an agent backend. Hermes works with Anthropic / OpenAI / Google / Ollama and others; Claude Agent runs Anthropic in-process.
4. Add your primary model provider and API key.
5. Add an industry research provider, or skip it.

The AdventureWorks seed also pre-fills the business onboarding form:

- Company: `AdventureWorks Cycles`
- Fiscal year start: `July`
- Seats: `CEO`, `CFO`, `COO`
- Priorities: `Defend wholesale margins`, `Grow DTC in Europe`

If you skipped the seed, you can enter those values manually later on `/onboarding`.

## Use your own data

Start the core stack only, no AdventureWorks:

```bash
mkdir -p ~/openneko && cd ~/openneko
openneko start --mode prod --detach
```

In the setup wizard, enter your GraphJin base URL. If GraphJin runs on your host machine, use `http://host.docker.internal:8080`. OpenNeko appends the GraphQL and MCP endpoint paths automatically.

## Plugins

OpenNeko can be extended with sandboxed plugins that add new action kinds (web search via Parallel.ai, posting to Slack, etc.). Each plugin is pulled from npm, integrity-checked against a marketplace pin, and run inside a microsandbox microVM with outbound network limited to the hosts the plugin's manifest declared at install time.

### Host support

Plugins run inside a microVM with hardware-virtualization acceleration.

- **macOS arm64 with the brew-installed binary + `openneko start --mode demo`**: plugin *installation* works (the host openneko CLI auto-proxies into the running worker container via `docker exec`, npm installs into the isolated `/var/lib/openneko/plugins/` dir, plugins.json hot-reloads); plugin *execution* still needs Hypervisor.framework which Docker Desktop hides from Linux containers, so the microVM spawn fails at action time. Result: installs land cleanly, the marketplace is browsable from the binary, but actions that need a plugin VM no-op. Full execution requires the source-build *Developer Setup* path below — `pnpm dev` runs the worker on the macOS host where microsandbox uses Hypervisor.framework directly.
- **Linux with `/dev/kvm`** (amd64 or arm64): both install AND execution supported. The worker container needs `/dev/kvm` passed in; the embedded compose's `plugins.linux.yml` overlay does this automatically when the binary detects KVM at start time.

On unsupported hosts the plugin subsystem is disabled with a clear log line; OpenNeko itself still runs and the built-in `send_webhook` adapter remains as an unsandboxed extensibility path.

`openneko doctor` reports host capability, docker version, and manifest state.

### Installing a plugin

The host `openneko` CLI auto-detects a running `openneko-*-worker-1` container and proxies plugin-op commands into it via `docker exec`, so installs Just Work from your laptop:

```bash
openneko install @open-neko/plugin-parallel-search
```

If a plugin declares required env values (Slack tokens, API keys), the CLI prompts at install time with hidden input and saves them to `/config/openneko/secrets.json` inside the container (mode 0600, on the `openneko-config` volume). Secrets never enter the tracked plugin manifest and never enter `action_request.payload`.

Pass `--local` to bypass the proxy and install host-side instead (useful for source-build dev workflows where the worker runs via `pnpm dev`, not docker).

The worker watches `openneko.plugins.json` and the secrets file; new plugins are usable on the next action_request, rotated secrets take effect on the next execute_action. No restart needed.

### Adding a third-party marketplace

```bash
openneko marketplace add https://example.com/marketplace.json
openneko install @example/openneko-plugin-foo
```

OpenNeko makes no representation about the safety of non-official marketplaces — that trust is between you and the publisher.

## Update

```bash
brew upgrade openneko        # macOS
openneko stop                # leave volumes intact to preserve config + data
openneko start --mode demo --detach
```

`openneko start` always runs any pending migrations against `neko-db` in-process before bringing up the rest of the stack.

For Linux, re-download the latest tarball from the [releases page](https://github.com/open-neko/neko/releases/latest) and replace `/usr/local/bin/openneko`.

## Reset

Tear down + remove all volumes (wipes the metadata DB, the AdventureWorks DB, agent workspaces, etc.) but keep your `~/.config/openneko/secrets.json` and trusted marketplaces:

```bash
openneko stop --volumes
```

Or wipe everything (including secrets, marketplaces, and the local plugin manifest):

```bash
openneko reset --all
```

## Ports

Default host ports:

- OpenNeko app: `3000`
- Metadata Postgres: `5432`
- OpenNeko metadata GraphJin (`neko-graphjin`): `8089`

In `--mode demo` the AdventureWorks Postgres listens internally only; no host port collision.

Override common ports with env vars before `openneko start`:

```bash
OPENNEKO_PORT=3001 OPENNEKO_DB_PORT=55432 OPENNEKO_GRAPHJIN_PORT=8090 \
  openneko start --mode demo --detach
```

## Build from source (advanced)

The repo-root `compose.yml` builds images from source instead of pulling pre-built ones. Use this when you want to develop on the stack itself, run the full AdventureWorks trial including the live order simulator + scenario-injector, or work without an internet connection.

```bash
git clone https://github.com/open-neko/neko.git
cd neko
docker compose -f compose.yml -f compose.adventureworks.yml up -d --build
docker compose -f compose.yml -f compose.adventureworks.yml run --rm neko-adventureworks-seed
```

This is what powers the *Try it in 10 minutes* flow in [README.md](README.md) — the scenario-injector and live order trickle are only available through this path today.

### Live trial data (source-build only)

The source-compose ships with a small order simulator that trickles realistic new sales orders into the sample database every 10 minutes by default. This is what makes the trial workspace *react* — briefing numbers drift, cron workflows fire, runs accumulate on `/runs`.

If you ran `neko-adventureworks-seed`, three cron workflows are pre-installed against this live data:

- **Daily Revenue Health Check** — runs each morning, posts to the Briefing
- **Revenue Drop Alert** — hourly per-territory check; proposes a Slack notification when revenue tanks
- **Slow-Ship Operations** — daily check for orders stuck pending past SLA

External-action targets are auto-routed to a mock adapter during trial (`NEKO_ACTIONS_DRY_RUN=true`), so nothing real fires. Approvals still queue on `/approvals` so you can see the loop work end to end.

Tune the simulator via env vars:

```bash
AW_SIM_INTERVAL_SEC=300 AW_SIM_ORDERS_MIN=1 AW_SIM_ORDERS_MAX=5 \
  docker compose -f compose.yml -f compose.adventureworks.yml up -d
```

Disable the trickle with `AW_SIM_ENABLED=0`. Wire real webhooks past trial with `NEKO_ACTIONS_DRY_RUN=false`.

## Developer Setup

For working on OpenNeko itself (Next.js UI, worker, packages), run the stack pieces in Docker but the app processes from source.

```bash
corepack enable
pnpm bootstrap

# DB + metadata GraphJin only — app runs from source
docker compose up -d neko-db neko-graphjin
pnpm --filter @neko/db migrate

pnpm dev
```

If you skip `neko-graphjin`, OpenNeko still runs but workflows that chain via subscriptions stay silent — the worker logs `subscription manager ready (0 active)` and matches never fire.

Install external CLIs the worker shells out to (host-only — the Docker images already include them):

```bash
./scripts/install-clis.sh
```

This installs the GraphJin CLI, Hermes, and the Claude Agent CLI.

With sample data:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml up -d \
  neko-db neko-graphjin adventureworks-db graphjin
pnpm --filter @neko/db migrate
pnpm dev
```

In the developer flow use `http://localhost:8080` for the customer-data GraphJin in the setup wizard. The metadata GraphJin (`neko-graphjin`) is reached automatically at `http://127.0.0.1:8089`.

### Working on the openneko binary itself

```bash
cd apps/openneko
go test ./... -count=1
go test -tags=integration -count=1 -timeout 10m ./internal/db/...  # spins up pgvector via testcontainers
go build -o /tmp/openneko ./cmd/openneko
```

When migrations change, sync the embedded copies before building:

```bash
apps/openneko/scripts/sync-migrations.sh
```

CI runs this with `--check` and fails on drift.

## Troubleshooting

**Docker is not running.** Start Docker Desktop (or the daemon) and re-run `openneko start`.

**Port already in use.** Override with `OPENNEKO_PORT`, `OPENNEKO_DB_PORT`, or `OPENNEKO_GRAPHJIN_PORT` env vars.

**Image pull fails with `unauthorized`.** The first time `ghcr.io/open-neko/neko-*` images are published they default to private — if `openneko start` fails on pull, confirm the packages are public at https://github.com/orgs/open-neko/packages.

**Worker crashes on boot.** `openneko logs worker` — if you see `ERR_MODULE_NOT_FOUND` for a workspace dep, you're likely on an old binary version that predates the workspace-node_modules fix. `brew upgrade openneko` (≥ 1.7.3).

**GraphJin connection fails.** For the included sample data in `--mode demo`, OpenNeko points at the internal `http://graphjin:8080` over the compose network — the setup wizard pre-fills this. If you're running developer mode (`pnpm dev`) on your host, use `http://localhost:8080`.

**Workflow subscriptions don't fire.** The subscription manager needs `neko-graphjin` running. `openneko status` should show it healthy. The worker logs `subscription manager ready (N active)` on boot — if `N` is `0` despite subscriptions existing in the database, `neko-graphjin` is either unreachable or its password has drifted (rotate via `/setup`, then `openneko stop && openneko start`).

**Provider key fails.** Confirm the provider key is active, has billing/quota available, and matches the provider selected in `/settings`.

**Stale `pnpm dev` processes hammering port 5432.** If you previously ran the worker from source via `pnpm dev`, those `tsx watch` processes survive `openneko stop` (the binary only manages docker containers, not host processes). They reconnect every ~10s with a stale password and surface as `password authentication failed for user "neko"` bursts in `openneko logs neko-db`. Fix:

```bash
pkill -f "tsx.*apps/worker/src/index"
pkill -f "tsx.*apps/web"
```

Then `openneko stop --volumes && openneko start --mode demo --detach`.

**Start from a clean slate.**

```bash
openneko reset --all
brew uninstall openneko && brew untap open-neko/tap  # if also reinstalling the binary
pkill -f "tsx.*apps/(worker|web)"                    # if you ever ran pnpm dev
```
