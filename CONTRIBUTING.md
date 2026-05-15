# Contributing to OpenNeko

Thanks for your interest in contributing. Issues and pull requests are welcome.

## Reporting bugs and requesting features

File an issue at [github.com/open-neko/neko/issues](https://github.com/open-neko/neko/issues). Please include:

- What you were doing
- What you expected
- What actually happened
- OpenNeko version and how you installed it (Docker / dev source)

## Setting up your dev environment

Setup commands — Node deps, migrations, `pnpm dev`, the two GraphJin services (sample-data and metadata), the AdventureWorks dev variant, and the external CLI installer (`./scripts/install-clis.sh`) — live in [INSTALL.md → Developer Setup](INSTALL.md#developer-setup). Update / reset / troubleshooting are in the same file.

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
