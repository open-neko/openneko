# Contributing to OpenNeko

Thanks for your interest in contributing. Issues and pull requests are welcome.

## Reporting bugs and requesting features

File an issue at [github.com/open-neko/openneko/issues](https://github.com/open-neko/openneko/issues). Please include:

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
  worker/              Background job runner, plugin registry, microsandbox runtime
  openneko/            `openneko` CLI + stack supervisor (Go), distributed as a single binary
packages/
  db/                  Drizzle ORM client, schema, migrations, job queue
  llm/                 Providers, agents, classifier, GraphJin work support
  plugin-install/      Manifest, secrets store, marketplace client, install orchestrator (TS — consumed by worker)
  plugin-types/        Plugin RPC schemas (zod) + manifest types — shared with plugin authors
db/
  migrations/          Metadata database migrations (mirrored into apps/openneko/assets/migrations/ for embedding)
  graphjin/            Sample GraphJin config
  seeds/dev/           AdventureWorks seed assets
```

## Before opening a pull request

Run the relevant checks:

```bash
pnpm test
pnpm lint
pnpm build

# For changes that touch apps/openneko/** or db/migrations/**:
cd apps/openneko
go vet ./...
go test ./... -count=1
./scripts/sync-migrations.sh --check
# Integration tests (need docker on the host):
go test -tags=integration -count=1 -timeout 10m ./internal/db/...
```

Keep PRs focused — one change per PR makes review tractable. Include a short description of the user-visible behavior change, and link the issue it closes if there is one.

## License

By contributing, you agree that your contributions will be licensed under the [Apache License 2.0](LICENSE).
