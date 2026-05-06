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
  seeds/dev/           Idempotent dev seeds + vendored AdventureWorks install.sql
  graphjin/            GraphJin config (dev.example.yml committed; dev.yml local-only)
  init-neko-db.sh      Sidecar entrypoint for neko-db-init
  load-adventureworks.sh  Sidecar entrypoint for adventureworks-init
```

## Dev

Three processes, three terminals:

```bash
# 1. AdventureWorks GraphJin (customer data, MCP-enabled)   :8080
# 2. Worker
pnpm dev:worker
# 3. Web
pnpm dev:web
```

Neko's database is plain Postgres. The web app and the worker
talk to it directly via Drizzle ORM (`@neko/db`). No internal
GraphJin service to run.

> **Note:** `@ax-llm/ax` auto-installs Claude Code skills into `.claude/skills/`
> on every install. We keep those at user scope (`~/.claude/skills/`) instead,
> so set `AX_SKIP_SKILL_INSTALL=1` in your shell rc to prevent pnpm from
> recreating the project-scope copy.

### First-time setup

```bash
pnpm install

# Per-app env files (each app loads its own — Next reads apps/web/.env,
# the worker reads apps/worker/.env via dotenv. There is no shared root .env.)
cp apps/web/.env.example apps/web/.env
cp apps/worker/.env.example apps/worker/.env

# Create the Neko database
PGPASSWORD=postgres psql -h localhost -U postgres -c "create database neko"
PGPASSWORD=postgres psql -h localhost -U postgres -d neko -f db/migrations/0001_init.sql
PGPASSWORD=postgres psql -h localhost -U postgres -d neko -f db/seeds/dev/0001_dev_bootstrap.sql
```

## Docker Compose

If you want Docker to run the Neko database for you, use the root [compose.yml](compose.yml). Works with any Docker that resolves `host.docker.internal` (OrbStack, Docker Desktop, Rancher Desktop; on Linux Docker Engine add `--add-host=host.docker.internal:host-gateway`).

What it does:

- Starts `neko-db` (Postgres 16) on `localhost:5432`
- Runs a one-shot `neko-db-init` sidecar that:
  - waits for `neko-db`
  - runs the consolidated schema file [db/migrations/0001_init.sql](db/migrations/0001_init.sql) if the schema is still empty
  - applies an idempotent dev seed from [db/seeds/dev/0001_dev_bootstrap.sql](db/seeds/dev/0001_dev_bootstrap.sql)

No manual `create database` step is needed for the compose path. The Postgres
container creates the `neko` database on first boot, and `neko-db-init`
handles migrations plus the dev seed.

Start the stack:

```bash
docker compose up -d
```

Stop it:

```bash
docker compose down
```

Reset the Neko database completely:

```bash
docker compose down -v
```

Important compose variables:

- `METADATA_PGUSER`
- `METADATA_PGPASSWORD`
- `METADATA_PGDATABASE`
- `METADATA_PGPORT`
- `DEV_ORG_ID`
- `DEV_ORG_NAME`
- `CUSTOMER_GRAPHQL_URL`
- `CUSTOMER_MCP_URL`

Defaults are baked into [compose.yml](compose.yml), so the simplest path is just:

```bash
docker compose up -d
```

If `5432` is already in use on your machine, remap the published host port:

```bash
METADATA_PGPORT=55432 docker compose up -d
```

### Customer data source wiring

`compose.yml` only brings up Neko's own database. The seeded `data_source` row still needs to point at a customer-facing GraphJin endpoint for profiling and metric refresh.

By default the seed uses:

- GraphQL: `http://host.docker.internal:8080/api/v1/graphql`
- MCP: `http://host.docker.internal:8080/api/v1/mcp`

Override those before `docker compose up -d` if your customer GraphJin is elsewhere:

```bash
export CUSTOMER_GRAPHQL_URL="http://host.docker.internal:8080/api/v1/graphql"
export CUSTOMER_MCP_URL="http://host.docker.internal:8080/api/v1/mcp"
docker compose up -d
```

### Optional: vendored AdventureWorks + GraphJin

If you don't already have a customer GraphJin running on `:8080`, you can opt into a self-contained AdventureWorks stack. This adds `adventureworks-db` (Postgres loaded with the AdventureWorks 2014 OLTP sample) and a `graphjin` server pointed at it.

One-time setup — copy the example GraphJin config to its active filename (gitignored):

```bash
cp db/graphjin/dev.example.yml db/graphjin/dev.yml
```

Then bring everything up:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml up -d
```

What it does on first run:

- Starts `adventureworks-db` on `localhost:5433`
- Runs a one-shot `adventureworks-init` sidecar ([db/load-adventureworks.sh](db/load-adventureworks.sh) → [apps/worker/scripts/load-adventureworks.ts](apps/worker/scripts/load-adventureworks.ts)) that downloads Microsoft's [AdventureWorks-oltp-install-script.zip](https://github.com/Microsoft/sql-server-samples/releases/download/adventureworks/AdventureWorks-oltp-install-script.zip), converts the BCP-formatted CSVs to tab-delimited, then loads [db/seeds/dev/adventureworks-install.sql](db/seeds/dev/adventureworks-install.sql) (a vendored copy of [lorint/AdventureWorks-for-Postgres](https://github.com/lorint/AdventureWorks-for-Postgres)) into a fresh `adventureworks` database
- Starts `graphjin` ([dosco/graphjin](https://hub.docker.com/r/dosco/graphjin) v3.18.10) on `localhost:8080`, configured from [db/graphjin/dev.example.yml](db/graphjin/dev.example.yml)

GraphJin reads its config from `db/graphjin/`. The committed `dev.example.yml` is loaded by default; copy it to `dev.yml` (gitignored) if you want local-only edits.

To wipe the AdventureWorks volume and re-load from scratch:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml down -v
```

### What gets seeded

The dev seed ensures there is always at least one dev org matching the app's current hardcoded tenant:

- `organization.id = 'default-org'` (override via `DEV_ORG_ID`)
- one `data_source` row for that org if none exists yet

If you already have tables, `neko-db-init` skips schema creation and only reapplies the idempotent seed.

## Model Providers

Neko supports per-org provider configuration from the app UI at `/settings`.

- Primary provider: used for profiling, chat classification, bootstrap card generation, and metric refreshes.
- Industry research provider: used for the onboarding industry-briefing step.

You can configure providers in one of two ways:

1. Instance defaults via environment variables in `apps/web/.env` and/or `apps/worker/.env` (mirror shared values in both — there is no root `.env`)
2. Org-level overrides via the settings screen

Resolution order is:

```text
org settings -> env defaults -> unconfigured
```

If no primary provider is configured, the worker still starts so you can open `/settings`, but AI-backed flows will fail until a provider is set.

### Vertex note

`vertex` remains supported as a special provider for the existing Vertex MaaS path. It uses Google Application Default Credentials, so local auth typically means either:

- `gcloud auth application-default login`
- `GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account.json`

Set `GCP_PROJECT_ID` and optionally `GCP_REGION` in env or in the settings screen.
