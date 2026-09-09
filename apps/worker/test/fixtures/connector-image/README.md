# Pack execution test image

This image tests pack execution without a plugin or a Google account.
It supports echo, denied network access and timeout checks. Do not use it as a
production connector.

Build and publish to a registry that your OpenShell gateway can access:

```sh
docker build -t YOUR_REGISTRY/pack-fixture:step2 apps/worker/test/fixtures/connector-image
docker push YOUR_REGISTRY/pack-fixture:step2
```

Use the digest returned by the push, not the tag:

```sh
OPENNEKO_PACK_EXEC_TEST_IMAGE=YOUR_REGISTRY/pack-fixture@sha256:ACTUAL_DIGEST \
  pnpm --filter @neko/worker exec vitest run test/pack-execution.integration.test.ts \
  --maxWorkers=1 --minWorkers=1
```

Set the existing `NEKO_PG_*` connection variables for a migrated test database.
The test creates and removes its own organization. It requires a working
OpenShell gateway. It checks both embedded and uploaded pack installation,
review, content changes, non-root execution, denied writes, denied network
access, timeout, removal during execution, restart and an unavailable image.
Without the image variable, the live tests are skipped.

## Browser account test

The same image implements a test-only OAuth protocol. It checks PKCE and returns
synthetic tokens. The browser test supplies a simulated provider consent page;
the web routes, worker, PostgreSQL and OpenShell remain real.

After building the image, set the same image and database variables and run:

```sh
pnpm --filter @neko/worker exec tsx test/fixtures/pack-account-server.ts
```

This installs the test pack and starts an isolated worker handler on port 4113.
Start a test web server with `WORKER_ADMIN_URL=http://127.0.0.1:4113`. Point a
Playwright config at that web server and run `test/visual/pack-accounts.spec.ts`.
The test connects two accounts, executes a sandbox read with the selected account,
and disconnects it. It also checks desktop/phone controls and error recovery.
Stop the fixture server with SIGTERM to remove its installation and organization.

For ownership, callback replay, consent, refresh and lifecycle tests with a real
database and a substituted provider runner:

```sh
OPENNEKO_PACK_ACCOUNTS_TEST=1 pnpm --filter @neko/worker exec vitest run \
  test/pack-accounts.integration.test.ts --maxWorkers=1 --minWorkers=1
```
