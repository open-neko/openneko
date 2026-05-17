# Install OpenNeko

This guide covers the Docker install path, the included sample-data setup, and the developer setup.

## Requirements

- Docker Desktop on macOS/Windows, or Docker Engine with Docker Compose on Linux
- Git, or a downloaded ZIP of this repository
- An API key for at least one supported model provider

OpenNeko also needs a GraphJin data source. The easiest first run uses the included AdventureWorks sample data.

## Recommended First Run

Clone the repository:

```bash
git clone https://github.com/open-neko/neko.git
cd neko
```

Start OpenNeko with the sample-data services:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml up -d --build
```

This starts OpenNeko, Postgres, AdventureWorks, and GraphJin. It does not seed OpenNeko metadata automatically.

The Docker image includes both supported agent CLIs: Hermes and Claude Agent. Choose either one in `/settings`; Hermes works with providers such as Gemini, OpenAI, and Anthropic, while Claude Agent requires Anthropic.

The Compose stack creates writable named volumes for OpenNeko config, GraphJin runtime config, agent workspaces, skills, uploads, artifacts, and temp files. Do not make these paths read-only; GraphJin, Hermes, Claude Agent, and skills all write working files while jobs run.

To pre-fill the GraphJin data source and AdventureWorks business onboarding answers, run:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml run --rm neko-adventureworks-seed
```

Open [http://localhost:3000](http://localhost:3000).

In the setup wizard:

1. Choose an admin database password.
2. If you ran the seed, confirm the pre-filled GraphJin data source. If you skipped it, enter:

   ```text
   http://graphjin:8080
   ```

3. Pick an agent backend.
4. Add your primary model provider and API key.
5. Add an industry research provider, or skip it.

The AdventureWorks seed also pre-fills the business onboarding form:

- Company: `AdventureWorks Cycles`
- Fiscal year start: `July`
- Seats: `CEO`, `CFO`, `COO`
- Priorities: `Defend wholesale margins`, `Grow DTC in Europe`

If you skipped the seed, you can enter those values manually later on `/onboarding`.

After setup, OpenNeko builds the first business profile and briefing cards from the sample data.

### Live trial data

The AdventureWorks compose ships with a small order simulator that trickles realistic new sales orders into the sample database every 10 minutes by default. This is what makes the trial workspace *react* — briefing numbers drift, cron workflows fire, runs accumulate on `/runs`.

If you ran the seed, three cron workflows are pre-installed against this live data:

- **Daily Revenue Health Check** — runs each morning, posts to the Briefing
- **Revenue Drop Alert** — hourly per-territory check; proposes a Slack notification when revenue tanks
- **Slow-Ship Operations** — daily check for orders stuck pending past SLA

External-action targets (e.g. the Slack webhook) are auto-routed to a mock adapter during trial (`NEKO_ACTIONS_DRY_RUN=true`), so nothing real fires. Approvals still queue on `/approvals` so you can see the loop work end to end.

Tune the simulator via env vars:

```bash
AW_SIM_INTERVAL_SEC=300 AW_SIM_ORDERS_MIN=1 AW_SIM_ORDERS_MAX=5 \
  docker compose -f compose.yml -f compose.adventureworks.yml up -d
```

Disable it entirely with `AW_SIM_ENABLED=0`. To wire real webhooks past trial, set `NEKO_ACTIONS_DRY_RUN=false`.

## Run In The Background

Use `-d` to run the containers in the background:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml up -d --build
```

Watch logs:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml logs -f web worker
```

Stop OpenNeko:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml down
```

## Use Your Own Data

If you already have a GraphJin endpoint, start only the OpenNeko stack:

```bash
docker compose up --build
```

Open [http://localhost:3000](http://localhost:3000) and enter your GraphJin base URL in the setup wizard.

If GraphJin runs on your host machine and OpenNeko runs in Docker, use:

```text
http://host.docker.internal:8080
```

If GraphJin runs as another service in the same Compose project, use that service name, for example:

```text
http://graphjin:8080
```

OpenNeko appends the GraphQL and MCP endpoint paths automatically.

## Plugins

OpenNeko can be extended with sandboxed plugins that add new action kinds (web search, Slack messaging, etc.). Each plugin is pulled from npm, integrity-checked against a marketplace pin, and run inside a microsandbox microVM with outbound network limited to the hosts the plugin's manifest declared at install time.

### Host support

Plugins run inside a microVM with hardware-virtualization acceleration. The Docker install path supports plugins **only on Linux hosts with `/dev/kvm`**:

```bash
docker compose -f compose.yml -f compose.plugins.yml up -d --build
```

The overlay passes `/dev/kvm` into the worker container. If your kernel doesn't expose KVM, Docker Compose refuses to start — that's the correct failure mode (a worker without KVM can't run plugins).

**macOS:** Docker Desktop's Linux VM hides KVM from containers, so plugins do not work under Docker on macOS. macOS operators run the worker directly on the host with `pnpm dev` (see Developer Setup); microsandbox then uses macOS Hypervisor.framework.

**Windows:** unsupported for plugins. Use Linux or macOS.

### Installing a plugin

Inside the running worker container the `openneko` CLI is on `PATH`. From your host:

```bash
docker compose exec worker openneko init
docker compose exec worker openneko install @open-neko/plugin-parallel-search
```

You can also let the agent drive these via its Bash tool — `openneko install/list/remove/secrets/marketplace` are documented in `openneko --help`.

If a plugin declares required env values (Slack tokens, API keys), the CLI prompts at install time with hidden input and saves them to a per-user secrets file at `/config/openneko/secrets.json` (mode 0600). Secrets never enter the tracked plugin manifest and never enter `action_request.payload`.

### Where state lives

- `/config/openneko/plugins.json` (on the `openneko-config` volume) — installed-plugin manifest. The worker watches this file and hot-loads plugins on the next action_request; no restart needed.
- `/config/openneko/secrets.json` — per-deployment plugin secrets, 0600.

Override the manifest path with the `OPENNEKO_PLUGINS_MANIFEST_PATH` env var if you want it somewhere else.

### Adding a third-party marketplace

The official `@open-neko/*` plugins ship from `https://open-neko.github.io/plugins/`. To trust an additional publisher:

```bash
docker compose exec worker openneko marketplace add https://example.com/marketplace.json
```

OpenNeko makes no representation about the safety of non-official marketplaces — that trust is between you and the publisher.

### Checking plugin health

```bash
docker compose exec worker openneko doctor
```

Reports host capability (KVM/Hypervisor.framework detected), manifest path + plugin count, and whether the runtime is available.

## Update

Pull the latest code and rebuild:

```bash
git pull
docker compose -f compose.yml -f compose.adventureworks.yml up -d --build
```

If you are not using the AdventureWorks sample stack:

```bash
git pull
docker compose up -d --build
```

The database migrator reads the persisted OpenNeko config, so updates continue to work after the setup wizard changes the database password.

## Reset

This removes the metadata database, sample database, local OpenNeko config, encrypted provider secrets, GraphJin runtime files, agent workspaces, skills, uploads, artifacts, and temp files:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml down -v
```

Start again with:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml up -d --build
```

## Ports

Default ports:

- OpenNeko app: `3000`
- Metadata Postgres: `5432`
- OpenNeko metadata GraphJin (`neko-graphjin`): `8089`
- AdventureWorks Postgres: `5433`
- Sample GraphJin (`graphjin`): `8080`

Override common host ports:

```bash
OPENNEKO_PORT=3001 OPENNEKO_DB_PORT=55432 \
  OPENNEKO_GRAPHJIN_PORT=8090 GRAPHJIN_PORT=8081 \
  docker compose -f compose.yml -f compose.adventureworks.yml up -d --build
```

If you change `GRAPHJIN_PORT`, the Docker setup wizard URL is still `http://graphjin:8080` because OpenNeko talks to GraphJin over the internal Compose network.

## Developer Setup

Install Node dependencies:

```bash
corepack enable
pnpm bootstrap
```

Run the metadata database plus OpenNeko's own GraphJin (powers
output-match subscriptions):

```bash
docker compose up -d neko-db neko-graphjin
pnpm --filter @neko/db migrate
```

Start the app from source:

```bash
pnpm dev
```

If you skip `neko-graphjin`, OpenNeko still runs but workflows that
chain via subscriptions stay silent — the worker logs `subscription
manager ready (0 active)` and matches never fire.

Install external CLIs used by the worker:

```bash
./scripts/install-clis.sh
```

This host-only developer command installs GraphJin CLI, Hermes, and Claude Agent CLI. The Docker install already includes them.

To develop with sample data:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml up -d \
  neko-db neko-graphjin adventureworks-db graphjin
pnpm --filter @neko/db migrate
pnpm dev
```

Two GraphJin services run side by side here:

- `graphjin` on port `8080` — connects to the AdventureWorks sample
  data; this is the URL you enter in the setup wizard.
- `neko-graphjin` on port `8089` — connects to OpenNeko's metadata DB
  and powers output-match subscriptions. The worker connects to it
  automatically.

In the developer flow, use this GraphJin URL in the setup wizard:

```text
http://localhost:8080
```

To also seed OpenNeko metadata with the AdventureWorks data source and onboarding answers:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml run --rm neko-adventureworks-seed
```

## Troubleshooting

**Docker is not running**

Start Docker Desktop or the Docker daemon, then run the compose command again.

**Port already in use**

Change the host port with `OPENNEKO_PORT`, `OPENNEKO_DB_PORT`, `OPENNEKO_GRAPHJIN_PORT`, `CUSTOMER_PGPORT`, or `GRAPHJIN_PORT`.

**GraphJin connection fails in Docker**

For the included sample data, use `http://graphjin:8080`, not `http://localhost:8080`. Inside Docker, `localhost` means the current container.

If you customize the GraphJin service, mount a writable directory at `/config`. GraphJin uses that app directory for queries, fragments, workflows, scratch files, and generated specs.

**GraphJin connection fails in developer mode**

When the web app and worker run on your host with `pnpm dev`, use `http://localhost:8080` for the customer-data GraphJin in the setup wizard. The OpenNeko metadata GraphJin (`neko-graphjin`) is reached automatically at `http://127.0.0.1:8089`; if you skip starting it, workflows still run but subscription matches don't fire.

**Workflow subscriptions don't fire**

OpenNeko's subscription manager needs `neko-graphjin` running. Start it with `docker compose up -d neko-graphjin`. The worker logs `subscription manager ready (N active)` on boot — if `N` is `0` despite subscriptions existing in the database, `neko-graphjin` is either unreachable or its password has drifted (rotate it via `/setup` then `docker compose restart neko-graphjin`).

**Provider key fails**

Confirm the provider key is active, has billing/quota available, and matches the provider selected in `/settings`.

**Start from a clean slate**

Run:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml down -v
```

Then start again.
