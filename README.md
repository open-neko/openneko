# Neko

Calm, chat-first morning briefings for CXOs.

- Site: https://getneko.app
- Repo: https://github.com/open-neko/neko
- License: [Apache 2.0](LICENSE)

## Layout

```
apps/
  web/                 Next.js — UI + API routes
  worker/              pg-boss job runner (consumes queues from neko-db)
packages/
  db/                  Drizzle ORM client + schema + pg-boss enqueue for neko-db
  llm/                 Shared LLM machinery: providers, agents, classifier
db/
  migrations/          SQL migrations for neko-db
  seeds/dev/           Vendored AdventureWorks install.sql
  graphjin/            GraphJin config (dev.example.yml committed; dev.yml local-only)
  init-neko-db.sh      Sidecar entrypoint for neko-db-init
  load-adventureworks.sh  Sidecar entrypoint for adventureworks-init
```

## Run

Three things need to be running:

1. **A customer data source** — a GraphJin endpoint Neko can profile (your own, or use the vendored AdventureWorks stack — see [Optional: vendored AdventureWorks + GraphJin](#optional-vendored-adventureworks--graphjin) below)
2. **Neko's metadata DB** — Postgres, easiest via `docker compose up -d` (see [Docker Compose](#docker-compose))
3. **The web + worker** — from the repo root:

```bash
pnpm dev          # runs web + worker concurrently
# or in separate terminals:
pnpm dev:web
pnpm dev:worker
```

The web app and the worker talk to Postgres directly via Drizzle ORM (`@neko/db`). There is no internal GraphJin service to run for Neko itself.

### First-time setup

```bash
pnpm bootstrap         # pnpm install (with AX_SKIP_SKILL_INSTALL=1) + copies db/graphjin/dev.example.yml → dev.yml
docker compose up -d   # starts neko-db, applies migrations
pnpm dev               # web + worker
```

`pnpm bootstrap` sets `AX_SKIP_SKILL_INSTALL=1` for you so `@ax-llm/ax`'s postinstall doesn't write `.claude/skills/` into the repo — we keep those at user scope instead. If you ever invoke `pnpm install` directly, set `AX_SKIP_SKILL_INSTALL=1` in your shell rc.

Then open <http://localhost:3000>. On first boot you're sent to `/settings`, which renders a linear setup wizard until first-run is finished:

1. Setting an admin DB password (writes to `~/.config/neko/config.json`)
2. Connecting your customer data source (the GraphJin endpoint from #1 above)
3. Picking the agent + primary LLM provider
4. Optional industry-research provider

After the wizard finishes, the `/onboarding` business wizard becomes reachable, the briefing surface lives at `/`, and `/settings` flips into a card index for ongoing edits to providers, data source, and agent.

The web and worker apps don't read env vars from disk by default. The one allowed knob is `NEXT_PUBLIC_DEMO=true` on the web side — drop it into `apps/web/.env.local` (see [apps/web/.env.example](apps/web/.env.example)) to flip into the canned-mock briefing flow for screenshots / video without real data. The `NEXT_PUBLIC_` prefix lets both server and client code read the same flag; the legacy `DEMO=true` still works server-side for backward compatibility. All other configuration comes from `~/.config/neko/config.json` plus rows in the metadata DB itself.

## Docker Compose

`compose.yml` brings up the metadata DB. Works with any Docker that resolves `host.docker.internal` (OrbStack, Docker Desktop, Rancher Desktop; on Linux Docker Engine add `--add-host=host.docker.internal:host-gateway`).

What it does:

- Starts `neko-db` (Postgres 16) on `localhost:5432` with hardcoded creds `neko/secret/neko`
- Runs a one-shot `neko-db-init` sidecar that waits for `neko-db` and applies every file in [db/migrations/](db/migrations/) — the baseline [0001_init.sql](db/migrations/0001_init.sql) on an empty schema, then any incremental migrations on every restart (each must be idempotent)

The bootstrap creds in [compose.yml](compose.yml) are the **baseline only**. The `/setup` wizard rotates the password on first run and persists the new value to `~/.config/neko/config.json` on the host.

```bash
docker compose up -d            # start
docker compose down             # stop
docker compose down -v          # reset (drops the volume)
```

Compose has no parameterized variables. Port `5432:5432` is fixed; if it conflicts on your host, edit the published port in [compose.yml](compose.yml) directly.

### First-run state

No org or data-source rows are seeded. The app's `getOrgId()` auto-bootstraps a single organization row (random UUID, name `"My Workspace"`) the first time it runs, and the `/settings` setup wizard creates the `data_source` row when you paste your GraphJin URL.

### Optional: vendored AdventureWorks + GraphJin

If you don't already have a customer GraphJin running, opt into the self-contained AdventureWorks stack. This adds `adventureworks-db` (Postgres loaded with the AdventureWorks 2014 OLTP sample) and a `graphjin` server pointed at it.

`pnpm bootstrap` already copied `db/graphjin/dev.example.yml` → `db/graphjin/dev.yml` for you (the active config file is gitignored). Bring everything up:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml up -d
```

What it does on first run:

- Starts `adventureworks-db` on `localhost:5433`
- Runs a one-shot `adventureworks-init` sidecar ([db/load-adventureworks.sh](db/load-adventureworks.sh) → [apps/worker/scripts/load-adventureworks.ts](apps/worker/scripts/load-adventureworks.ts)) that downloads Microsoft's [AdventureWorks-oltp-install-script.zip](https://github.com/Microsoft/sql-server-samples/releases/download/adventureworks/AdventureWorks-oltp-install-script.zip), converts the BCP-formatted CSVs to tab-delimited, then loads [db/seeds/dev/adventureworks-install.sql](db/seeds/dev/adventureworks-install.sql) (a vendored copy of [lorint/AdventureWorks-for-Postgres](https://github.com/lorint/AdventureWorks-for-Postgres)) into a fresh `adventureworks` database
- Starts `graphjin` ([dosco/graphjin](https://hub.docker.com/r/dosco/graphjin) v3.18.10) on `localhost:8080`, configured from [db/graphjin/dev.example.yml](db/graphjin/dev.example.yml)

When prompted by the `/settings` setup wizard for a data source, point it at:

- GraphQL: `http://host.docker.internal:8080/api/v1/graphql`
- MCP: `http://host.docker.internal:8080/api/v1/mcp`

Variables this stack reads (all optional, defaults are baked in):

- `CUSTOMER_PGUSER` (default `postgres`)
- `CUSTOMER_PGPASSWORD` (default `postgres`)
- `ADVENTUREWORKS_DB` (default `adventureworks`)

Wipe the AdventureWorks volume and re-load from scratch:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml down -v
```

## Model Providers

All provider configuration lives in the app's `/settings` UI, scoped per-org. Two scopes:

- **Primary** — used for profiling, chat classification, bootstrap card generation, and metric refreshes
- **Industry research** — optional; used during the onboarding industry-briefing step

Without a primary provider configured the worker still starts (so `/settings` remains reachable), but AI-backed flows fail until one is set.

### Vertex note

`vertex` uses Google Application Default Credentials, so local auth is one of:

- `gcloud auth application-default login`
- `GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account.json`

The GCP project id is set in `/settings` (per-org secret, not an env var).
