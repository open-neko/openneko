# Contributing to OpenNeko

Thanks for your interest in contributing. Issues and pull requests are welcome.

## Reporting bugs and requesting features

File an issue at [github.com/open-neko/neko/issues](https://github.com/open-neko/neko/issues). Please include:

- What you were doing
- What you expected
- What actually happened
- OpenNeko version and how you installed it (Docker / dev source)

## Developer setup

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

To develop against the AdventureWorks sample data:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml up -d neko-db adventureworks-db graphjin
pnpm --filter @neko/db migrate
pnpm dev
```

In the developer flow, use `http://localhost:8080` as the GraphJin URL when the setup wizard asks for the data source. To pre-fill the GraphJin data source and AdventureWorks onboarding answers, run:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml run --rm neko-adventureworks-seed
```

The full Docker install path and reset / update steps live in [INSTALL.md](INSTALL.md).

## Repository layout

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

## Before opening a pull request

Run the relevant checks:

```bash
pnpm test
pnpm lint
pnpm build
```

Keep PRs focused — one change per PR makes review tractable. Include a short description of the user-visible behavior change, and link the issue it closes if there is one.

## License

By contributing, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).
