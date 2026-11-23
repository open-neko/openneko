# @neko/llm

This package holds TypeScript helpers, types and fixtures. No OpenNeko process
runs it. In 6.0.0, `openneko web` runs the agent's tools and the host model
calls in Go (`apps/openneko/internal/webfront`, `apps/openneko/internal/llm`).
`openneko serve` runs each agent turn in a confined runner.

## Who uses it

- **The web app.** Vite builds `apps/web` with the imports below. The release
  holds the built files, not this package.
  - `work/answer-expression` and `work/answer-tool-check`: the A2UI answer
    blocks.
  - `workflows/fences` and `workflows/fence-schemas`: the rule chat bubble.
  - `model-tasks`, `model-routes` and `spend`: the Models and Spend settings.
  - `library/formats` and the types of `work` and
    `graphjin/pack-user-connections`.
- **The evals.** The scripts and tests in `packages/evals` use it to seed an
  install and to read a run (`work`, `workflows`, `secrets`, `files`,
  `graphjin`).
- **Repository scripts.** `scripts/skill-doctor.ts` reads the skill
  dependency list in `src/work/skill-deps.ts`. `scripts/dev-up.sh` reads the
  GraphJin version from `src/graphjin/version.ts`.

## Files that ship

`assets/builtin-skills` holds the built-in skills. The Dockerfile and
`scripts/build-native-release.sh` copy that folder into
`share/openneko/skills/builtin`. A test in `apps/openneko/assets` checks both
copies.

## Fixtures that Go tests share

- `test/fixtures/model-catalog.json`: the provider list and the default
  models. `apps/openneko/internal/llm` and `test/model-catalog.test.ts` check
  it.
- `test/fixtures/host-calls.json`: the Ax route and the price for each host
  call. `apps/openneko/internal/llm` and `test/host-calls-fixture.test.ts`
  check it.
- `src/spend/ax-model-catalog.json`: the model prices from the Ax harness.
  `apps/ax-harness/catalog` fails when the file is stale. To make it again,
  run `pnpm --filter @neko/llm catalog:ax`.

When you change one of these, change the Go code in the same commit.

## Tests

```sh
pnpm --filter @neko/llm test
```

The tests in `test/integration` skip unless their service answers.
`ax-serve.integration.test.ts` needs its own environment.

### Live GraphJin tests

`test/integration/graphjin-mcp-live.test.ts` and `agentic-knowledge-live.test.ts`
skip unless a sources-mode GraphJin answers at `OPENNEKO_TEST_GJ_SOURCES_URL`
(default `http://127.0.0.1:8090`). This rig loads the shipped customer config
with one `read_only` Postgres source, so the mutation case exercises the real
database block:

```sh
export XDG_CONFIG_HOME=/tmp/neko-live-xdg   # fresh install key for the tests
SECRET=$(packages/contract/node_modules/.bin/tsx -e 'import("./packages/llm/src/graphjin/token.ts").then(m => process.stdout.write(m.graphjinSigningSecretB64("org-gj4-live")))')
docker network create neko-live
docker run -d --name neko-live-pg --network neko-live -e POSTGRES_PASSWORD=pg -e POSTGRES_DB=app postgres:16-alpine
sleep 5 && docker exec neko-live-pg psql -U postgres -d app -c "create table orders(id serial primary key, note text); insert into orders(note) values ('seed');"
mkdir -p /tmp/neko-live-gj
sed -e "s|REPLACE_WITH_PER_ORG_SECRET_B64|$SECRET|" \
    -e "s|REPLACE_WITH_BASE64_32_BYTE_KEY|$(openssl rand -base64 32)|" \
    -e 's|^sources: \[\]|sources:\n  - name: app\n    kind: database\n    type: postgres\n    host: neko-live-pg\n    port: 5432\n    dbname: app\n    user: postgres\n    password: pg\n    read_only: true\n    access:\n      read: authenticated|' \
    db/graphjin/customer.sources.example.yml > /tmp/neko-live-gj/agentic.yml
docker run -d --name neko-gj-live --network neko-live -p 127.0.0.1:8090:8080 -e GO_ENV=agentic \
  -v /tmp/neko-live-gj:/config dosco/graphjin:3.21.18 serve --path /config
pnpm --filter @neko/llm exec vitest run test/integration/graphjin-mcp-live.test.ts
docker rm -f neko-gj-live neko-live-pg && docker network rm neko-live
```

A database source needs `access.read: authenticated` for the roles that `openneko web` mints to
read it; OpenNeko sets that on every source it registers. `query_catalog` is
absent from the tool list until a source is configured.
