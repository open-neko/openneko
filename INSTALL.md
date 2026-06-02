# Install OpenNeko

Sections below **Command reference** are reference — not needed to get started.

## Requirements

- Docker Desktop on macOS, or Docker Engine + Docker Compose on Linux
- An API key for at least one supported model provider

## Install

### macOS (Homebrew)

```bash
brew install open-neko/tap/openneko
mkdir -p ~/openneko && cd ~/openneko
openneko start --mode demo --detach
```

### Linux

```bash
TAG=$(curl -fsSL https://api.github.com/repos/open-neko/openneko/releases/latest | grep -oE '"tag_name": *"[^"]+"' | head -1 | cut -d'"' -f4)
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
curl -fsSL "https://github.com/open-neko/openneko/releases/download/$TAG/openneko_${TAG#v}_linux_$ARCH.tar.gz" | tar -xz openneko
sudo install -m 0755 openneko /usr/local/bin/ && rm -f openneko
mkdir -p ~/openneko && cd ~/openneko
openneko start --mode demo --detach
```

Open [http://localhost:3000](http://localhost:3000) and finish the setup wizard.

`--mode demo` pulls pinned images and loads AdventureWorks sample data with three example watchers. For your own data, use `--mode prod` ([Use your own data](#use-your-own-data)).

## Setup wizard

1. Choose an admin database password.
2. Confirm the pre-filled GraphJin URL (`--mode demo`) or enter your own (`--mode prod`).
3. Pick an agent backend — Hermes (Anthropic / OpenAI / Google / Ollama / others) or Claude Agent (Anthropic in-process).
4. Add your provider API key.
5. Add an industry research provider, or skip.

The AdventureWorks seed pre-fills business onboarding (`AdventureWorks Cycles`, fiscal year `July`, seats `CEO`/`CFO`/`COO`, priorities `Defend wholesale margins` / `Grow DTC in Europe`). Otherwise fill it in at `/onboarding`.

## Use your own data

```bash
mkdir -p ~/openneko && cd ~/openneko
openneko start --mode prod --detach
```

Enter your GraphJin base URL in the setup wizard. If GraphJin runs on your host, use `http://host.docker.internal:8080` — OpenNeko appends the GraphQL and MCP paths automatically.

## Plugins

```bash
openneko install @open-neko/plugin-parallel-search
```

Browse the marketplace at [open-neko.github.io/plugins](https://open-neko.github.io/plugins/). `openneko doctor` checks your host can run sandboxed plugins. Full sandbox model in **[PLUGINS.md](PLUGINS.md)**.

---

*Reference below — not needed to get started.*

## Command reference

```bash
openneko start [--mode prod|dev|demo] [--detach]
openneko status                    # docker compose ps proxy
openneko logs [service…] [-f]
openneko stop [--volumes]          # --volumes wipes data
openneko migrate                   # apply pending migrations against running neko-db
openneko seed adventureworks       # one-shot demo data load (already done by --mode demo)
openneko reset [--all]             # tear down + clear local config (--all also wipes secrets/marketplaces)
openneko doctor                    # host check, docker version, manifest state
openneko version
```

Modes:

- **`prod`** (default) — core services: `neko-db`, `neko-graphjin`, `web`, `worker`.
- **`dev`** — core + dev tooling (empty overlay today).
- **`demo`** — core + AdventureWorks: `adventureworks-db`, `adventureworks-init`, `neko-adventureworks-seed`. The full live-trial flow (continuous order trickle + scenario injector) lives in `compose.adventureworks.yml` and needs the [Build from source](#build-from-source-advanced) path.

`~/.config/openneko/compose.override.yml` is auto-applied if present (last `-f` to docker compose).

## Upgrade

`openneko` upgrades by replacing the binary. The new binary embeds bumped image pins and new migrations; both apply on the next `openneko start`. No `openneko upgrade` subcommand — subscribe to [release notifications](https://github.com/open-neko/openneko/releases) for pushes.

### macOS

```bash
brew update
brew upgrade openneko
cd ~/openneko
openneko stop                          # leaves volumes intact
openneko start --mode demo --detach    # use the same --mode you started with
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
openneko stop
openneko start --mode demo --detach
```

### What `openneko start` does

1. **Migrate.** Connects to `neko-db`, takes a Postgres advisory lock, applies pending embedded migrations, releases the lock. Idempotent.
2. **Compose up.** Writes embedded compose files to `.openneko/runtime/` and runs `docker compose up`. Docker pulls the pinned image tags.

`openneko stop` doesn't touch volumes. Use `--volumes` for a clean slate (wipes the metadata DB, demo state, agent workspaces).

## Reset

```bash
openneko stop --volumes   # wipes data; keeps secrets + marketplaces
openneko reset --all      # wipes everything including secrets and marketplaces
```

## Ports

Defaults:

- App: `3000`
- Metadata Postgres: `5432`
- Metadata GraphJin: `8089`

In `--mode demo` the AdventureWorks Postgres is internal-only.

Override:

```bash
OPENNEKO_PORT=3001 OPENNEKO_DB_PORT=55432 OPENNEKO_GRAPHJIN_PORT=8090 \
  openneko start --mode demo --detach
```

## Build from source (advanced)

> Needed for developing OpenNeko itself or running the full live trial (continuous order trickle + scenario injector).

```bash
git clone https://github.com/open-neko/openneko.git
cd neko
docker compose -f compose.yml -f compose.adventureworks.yml up -d --build
docker compose -f compose.yml -f compose.adventureworks.yml run --rm neko-adventureworks-seed
```

### Live trial data

The source compose trickles fresh sales orders into the sample DB every 10 minutes. Briefing numbers drift, cron workflows fire, runs accumulate on `/runs`. External-action targets route to a mock adapter during trial (`NEKO_ACTIONS_DRY_RUN=true`); approvals still queue.

```bash
AW_SIM_INTERVAL_SEC=300 AW_SIM_ORDERS_MIN=1 AW_SIM_ORDERS_MAX=5 \
  docker compose -f compose.yml -f compose.adventureworks.yml up -d
```

Disable the trickle: `AW_SIM_ENABLED=0`. Wire real webhooks past trial: `NEKO_ACTIONS_DRY_RUN=false`.

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
pnpm dev:up
(cd apps/openneko && go run ./cmd/openneko migrate)
pnpm dev
```

`pnpm dev:up` bind-mounts `~/.config/openneko` into `neko-graphjin` so host web/worker and in-Docker GraphJin share `config.json` (including the DB password after `/setup` rotates it). Demo/prod don't need this — web+worker run in compose and share the named volume.

Without `neko-graphjin`, subscription-chained workflows stay silent (`subscription manager ready (0 active)` in worker logs).

Install host-only CLIs the worker shells out to (Docker images already include them):

```bash
./scripts/install-clis.sh
```

Installs the GraphJin CLI, Hermes, and the Claude Agent CLI.

With sample data:

```bash
mkdir -p "$HOME/.config/openneko"
OPENNEKO_CONFIG_VOLUME="$HOME/.config/openneko" \
  docker compose -f compose.yml -f compose.adventureworks.yml up -d \
  neko-db neko-graphjin adventureworks-db graphjin
(cd apps/openneko && go run ./cmd/openneko migrate)
pnpm dev
```

In the dev flow, use `http://localhost:8080` for customer-data GraphJin in the setup wizard. Metadata GraphJin reaches `http://127.0.0.1:8089` automatically.

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

**Port in use.** Override via `OPENNEKO_PORT` / `OPENNEKO_DB_PORT` / `OPENNEKO_GRAPHJIN_PORT`.

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
