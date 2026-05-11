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
- AdventureWorks Postgres: `5433`
- Sample GraphJin: `8080`

Override common host ports:

```bash
OPENNEKO_PORT=3001 OPENNEKO_DB_PORT=55432 GRAPHJIN_PORT=8081 \
  docker compose -f compose.yml -f compose.adventureworks.yml up -d --build
```

If you change `GRAPHJIN_PORT`, the Docker setup wizard URL is still `http://graphjin:8080` because OpenNeko talks to GraphJin over the internal Compose network.

## Developer Setup

Install Node dependencies:

```bash
corepack enable
pnpm bootstrap
```

Run only the metadata database:

```bash
docker compose up -d neko-db
pnpm --filter @neko/db migrate
```

Start the app from source:

```bash
pnpm dev
```

Install external CLIs used by the worker:

```bash
./scripts/install-clis.sh
```

This host-only developer command installs GraphJin CLI, Hermes, and Claude Agent CLI. The Docker install already includes them.

To develop with sample data:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml up -d neko-db adventureworks-db graphjin
pnpm --filter @neko/db migrate
pnpm dev
```

In the developer flow, use this GraphJin URL:

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

Change the host port with `OPENNEKO_PORT`, `OPENNEKO_DB_PORT`, `CUSTOMER_PGPORT`, or `GRAPHJIN_PORT`.

**GraphJin connection fails in Docker**

For the included sample data, use `http://graphjin:8080`, not `http://localhost:8080`. Inside Docker, `localhost` means the current container.

If you customize the GraphJin service, mount a writable directory at `/config`. GraphJin uses that app directory for queries, fragments, workflows, scratch files, and generated specs.

**GraphJin connection fails in developer mode**

When the web app and worker run on your host with `pnpm dev`, use `http://localhost:8080`.

**Provider key fails**

Confirm the provider key is active, has billing/quota available, and matches the provider selected in `/settings`.

**Start from a clean slate**

Run:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml down -v
```

Then start again.
