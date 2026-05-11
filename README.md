# OpenNeko

Calm, chat-first morning briefings for CXOs.

![OpenNeko CFO briefing](cfo-briefing.png)

OpenNeko connects to operational data through GraphJin, builds role-aware business briefings, and lets leaders ask follow-up questions in a focused workspace.

## Features

- Morning briefing cards for executive roles
- Chat-first follow-up analysis against business data
- GraphJin data-source integration
- Worker-backed metric refresh and onboarding jobs
- Configurable agent backend and model provider
- Docker image includes GraphJin CLI, Hermes, and Claude Agent CLI
- Optional industry research provider
- Included AdventureWorks sample data stack

## Quickstart

Requirements:

- Docker Desktop or Docker Engine with Docker Compose
- An LLM provider API key for setup

Start OpenNeko with the included AdventureWorks sample-data services:

```bash
git clone https://github.com/open-neko/neko.git
cd neko
docker compose -f compose.yml -f compose.adventureworks.yml up -d --build
```

Optionally seed OpenNeko with the AdventureWorks GraphJin data source and onboarding answers:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml run --rm neko-adventureworks-seed
```

Open [http://localhost:3000](http://localhost:3000) and complete the setup wizard. If you skip the seed and need to enter the GraphJin URL manually, use:

```text
http://graphjin:8080
```

For detailed install, update, reset, and troubleshooting steps, see [INSTALL.md](INSTALL.md).

## Developer Setup

Install dependencies:

```bash
corepack enable
pnpm bootstrap
```

Run the metadata database and migrations:

```bash
docker compose up -d neko-db
pnpm --filter @neko/db migrate
```

Start the web app and worker from source:

```bash
pnpm dev
```

To develop with the AdventureWorks sample data:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml up -d neko-db adventureworks-db graphjin
pnpm --filter @neko/db migrate
pnpm dev
```

In the developer flow, use `http://localhost:8080` as the GraphJin URL when the setup wizard asks for the data source. To also seed OpenNeko metadata with the AdventureWorks data source and onboarding answers, run:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml run --rm neko-adventureworks-seed
```

## Repository Layout

```text
apps/
  web/                 Next.js UI and API routes
  worker/              Background job runner
packages/
  db/                  Drizzle ORM client, schema, migrations, job queue
  llm/                 Providers, agents, classifier, GraphJin work support
db/
  migrations/          Metadata database migrations
  graphjin/            Sample GraphJin config
  seeds/dev/           AdventureWorks seed assets
```

## Configuration

The setup wizard stores local runtime config in `~/.config/openneko/config.json`. In Docker, runtime state is kept in named volumes so database password changes, encrypted provider secrets, GraphJin CLI config, agent workspaces, skills, uploads, generated artifacts, and scratch files survive restarts and are shared between the web and worker containers.

Provider and agent settings are configured in the app under `/settings`. Docker installs include both supported agent CLIs, so users can choose Hermes or Claude Agent from the settings page.

## Contributing

Issues and pull requests are welcome. For code changes, use the developer setup above and run the relevant checks before opening a PR:

```bash
pnpm test
pnpm lint
pnpm build
```

## Issues

Please file bugs and feature requests at [github.com/open-neko/neko/issues](https://github.com/open-neko/neko/issues).

## License

OpenNeko is licensed under the [Apache License 2.0](LICENSE).

## Author

Created by [Amit Deshmukh](https://github.com/amitdeshmukh).
