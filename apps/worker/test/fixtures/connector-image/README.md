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
