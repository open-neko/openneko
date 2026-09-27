# OpenNeko adapter

Optional consumer integration. Core packages do not import this directory, and
building this adapter requires no OpenNeko checkout or application libraries.
The optional product change adds a Harness backend while preserving Hermes as the
default. See [OpenNeko consumer acceptance](../../integration/openneko/README.md) for the concrete
launcher, broker, queue and browser checks. Build the product image with
`./adapters/openneko/build-image.sh /absolute/OpenNeko-checkout`.

The M5b batch prototype is a separate `./adapters/openneko/cmd/batch` binary.
It takes one UTC day argument and trusted `HARNESS_BATCH_SCRIPT`,
`HARNESS_BATCH_SCRIPT_SHA256`, `HARNESS_BATCH_BUNDLE_DIR`,
`HARNESS_BATCH_BUNDLE_SHA256`, `HARNESS_BATCH_WORK_DIR`,
`HARNESS_BATCH_ARTIFACT_DIR`, `HARNESS_BATCH_IMAGE`, `HARNESS_OPENSHELL_BIN`,
and `OPENSHELL_GATEWAY` bindings. A trusted worker may also set
`HARNESS_BATCH_RUN_ID` after acquiring database ownership for that Work run;
this gives its sandbox a stable name and lets a retry remove only a stranded
sandbox carrying the same run label. The whole script bundle, including imports, is
pinned. The script runs in a separate OpenShell sandbox without a provider,
broker token, or network grant. Its only data channel is the query-cache file
handoff; the host owns `batchRead` GraphJin calls, receipts and artifact
validation. This executor must be bound to a workflow definition and
`workflow_run`; its eligibility does not depend on skill grants. Skill files
and scripts must never call a model provider directly:
model routing, budgets and telemetry belong to Harness/Ax. The isolated fixture
passes with OpenShell 0.0.116. Web and worker read the same trusted,
read-only `HARNESS_BATCH_EXECUTOR_REGISTRY` JSON file. Its `version: 1`
document has an `executors` array; each entry binds a `workflowId`, `revision`,
and `active` flag to `binary`, `binarySha256`, `openshellBin`,
`openshellSha256`, `gateway`, `image`, `script`, `scriptSha256`, `bundleDir`,
and `bundleSha256`. Paths are absolute and hashes are lowercase SHA-256 hex.
At most one revision may be active for a workflow. Admission snapshots the
active revision and its configuration fingerprint; the worker resolves that
exact revision, checks binary hashes, and the Go runner verifies the script
and entire bundle. Keep retired entries until their accepted runs drain; use
immutable image references in production. The worker also verifies the
workflow definition, admitted contract and actor before publishing the artifact.
`POST /api/v1/workflows/{id}/runs` in the
default `single` mode accepts `{"targetDay":"YYYY-MM-DD"}` and an idempotency key,
returns `202` with a run URL, and exposes the validated CSV through the
authenticated artifact URL after completion. The caller cannot edit or approve
the queued run. An isolated Postgres + worker + web test passed admission,
definition edits after admission, duplicate delivery, status and exact-byte
download with a no-provider fixture. A connected API admission also passed
through the production queue, Go, OpenShell and a seeded GraphJin broker,
publishing one validated CSV. The same path now passes public HTTP submission,
status polling and exact-byte artifact download with the versioned registry.
A real-data Daily Lead run remains in
[M5b](../../docs/MILESTONES.md#m5b--file-backed-batch-path-and-mcp-readinteraction-slice).

The Harness MCP adapter admits pinned memory/library and Records catalog,
find/get, blueprint and recycle-bin reads through OpenNeko's trusted stdio
bridge. Records-only turns omit GraphJin lookup and customer memory. The
isolated connected run now reads a populated Records app through real GraphJin,
the actor-bound broker, OpenShell, MCP and Ax.
The separate `memory_save` capability uses a Work-run-only broker grant and
host operation receipt. A lost receipt leaves the save outcome unknown and
prevents later writes; the agent never retries the save automatically.

## Implemented: OpenShell cold-launch compatibility

From the repository root:

```sh
go build -o bin/openshell ./adapters/openneko/cmd/openshell-compat
OPENSHELL_TEST_CLI=/absolute/path/to/openshell-0.0.116 ./integration/run.sh adapters/openneko/integration.sh
```

Set `HARNESS_OPENSHELL_BIN` to the absolute path of the real 0.0.116 CLI. A worker
packaging configuration can put this adapter on its PATH as `openshell`; nothing
here installs it globally or changes an active gateway. The gateway must use the
qualified matching release. This adapter is not needed by standalone Harness.

The exact legacy cold-create `/bin/sh -lc true` command becomes detached creation
with a live main process, followed by uploads. Failed uploads delete the created
sandbox. Other commands pass through. Failed creation remains the caller's
responsibility. The live suite checks the original failure, staged bytes, exec,
deletion and cleanup after a missing upload source.

## Original integration contract (M3 implementation described above)

Translate the existing launch/job format into the harness run specification,
project neutral events/results into `__openneko_event__` and
`__openneko_agent_result__`, and propagate cancellation. Bind GraphJin through the
existing server-side agent broker capability; caller identity and approvals remain
OpenNeko-owned. Map neutral observations into product telemetry without double
counting model or delegated usage.

The host must also install the matching native recovery helper:

```sh
go build -o bin/harness-inspect ./cmd/harness-inspect
export HARNESS_INSPECT_BIN=/absolute/path/to/bin/harness-inspect
```

Both worker and web launchers require it when Harness is selected. The image build
includes the Linux helper. Apply OpenNeko migrations 0084–0090 and drain older workers before rollout. Deploy the matching Harness image and broker together; the new lookup route deliberately has no unjournaled fallback. Hosts share
PostgreSQL and the gateway; their local filesystem admission caches may differ.
Local helpers still require POSIX locking. See [recovery](../../docs/M4-RECOVERY.md) for adoption, ambiguity and
remaining M4 gates. Hermes requires neither helper nor configuration change.
