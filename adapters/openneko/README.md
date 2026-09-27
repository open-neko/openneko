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

The Harness MCP adapter admits pinned memory/library, actor-filtered workflow
list, and Records catalog,
find/get, blueprint and recycle-bin reads through OpenNeko's trusted stdio
bridge. Records-only turns omit GraphJin lookup and customer memory. The
isolated connected run now reads a populated Records app through real GraphJin,
the actor-bound broker, OpenShell, MCP and Ax.
Customer Work turns also admit six read-only management lists (plugins, users,
groups, channels, data sources and action rules) through pinned schemas; their
adjacent request/save/delete routes remain broker-denied. A queued Work run
read all six and recovered the seeded rule without a mutation operation.
The audit trail is a separate pinned read. It uses the run's actor and
organization at the host; a queued admin received a seeded action request,
while a queued member received only the admin-only denial.
The source-config MCP grant is narrower still: it requires the organization's
source-config feature, a current admin Work actor and the web channel. It pins
only source graph description, source secret names and OpenAPI asset listing.
The broker rejects import, config-agent and change requests. A queued admin
turn received seeded names through all three reads; a member was denied.
The connected upload fixture sends Markdown to the real Work HTTP endpoint,
then runs extraction, distillation and embedding through the queue before a
separate Work turn retrieves its sourced concept with `mcp_library_search`.
The separate `memory_save` capability uses a Work-run-only broker grant and
host operation receipt. A lost receipt leaves the save outcome unknown and
prevents later writes; the agent never retries the save automatically.
Customer Work runs can also use the direct `workflow_save` capability. New
definitions require `expectedVersion=absent`; edits require the exact
`versionToken` from the actor-filtered MCP workflow list. The host binds the
actor, journals the effect, applies the version check in the workflow store,
and persists workflow and subscription confirmation cards before returning a
receipt. A connected queued run created and edited a cron/batch workflow and
proved a stale version cannot overwrite it. Chromium found the edited Work card
once before and after reload. Delete and rule writes are still
outside the grant; data-change subscriptions and watchers also await a separate
partial-failure recovery contract. The image builder bundles the checkout's MCP bridge alongside
the Go runner so the listed version token reaches Ax unchanged.
Admin customer Work runs can also use `rule_save`. A new name needs
`expectedVersion=absent`; an edit must use the `versionToken` from the pinned
MCP rule list. The host rechecks current admin authority, journals the effect,
serializes same-name writes and persists a confirmation card before returning
the receipt. A connected run covered approval-required and low-risk auto-approve
definitions, stale/member/disabled-admin rejection, concurrent create-only
collision, and a Chromium card reload. Harness proposals remain held for human
approval regardless of the rule's auto-approve mode.
Customer Work runs can admit Ax's owned `team.researcher` child with only
GraphJin lookup and memory search. Its model calls and read operations share
the parent run's limits and journal. A connected two-investigation fixture
passed through OpenShell, MCP and the actor-bound broker. A queued workflow
now binds two read-only GraphJin children to its owning `work_run`; the parent
emits one finding through a journaled, workflow-bound broker tool. API-admitted
workflow runs and one governed pack-action proposal passed connected checks;
other product action families remain open.

`process_run` is an opt-in, model-visible Work tool backed by the run-bound
broker and a separate host-side `processshell` executable. The host accepts
selected current-thread upload basenames, stages bounded regular files in a
run-owned input directory, and supplies approved argv and declared output
names after acquiring the durable run lease. The executor snapshots those
inputs, starts a separate no-provider/no-network OpenShell sandbox, limits
output and execution time, then publishes only validated files after successful
completion and mandatory sandbox deletion. The broker journals the durable
operation, and the worker emits the validated result through its existing Work
artifact route. The connected queue and browser fixtures verify an exact CSV,
one artifact event, one operation receipt, credential and network isolation,
symlink rejection, cancellation and teardown. This is a bounded process
capability; the separate real OpenShell suite also rejects publication after
a script writes then fails, a partial-output cancellation, and an oversized
declared output. Larger and failed/cancelled artifact cases still need
product-level qualification.
The `./adapters/openneko/cmd/process` host executable accepts only a bounded
`{"Argv":[...],"Outputs":[...]}` request on stdin. The trusted launcher
must supply `HARNESS_PROCESS_RUN_ID`, `HARNESS_PROCESS_OPERATION_ID`,
`HARNESS_PROCESS_INPUT_ROOT`, `HARNESS_PROCESS_OUTPUT_ROOT`,
`HARNESS_PROCESS_IMAGE`, `HARNESS_OPENSHELL_BIN` and `OPENSHELL_GATEWAY`.
Its connected test invokes the executable itself. Do not install it in the
agent image or pass the broker token to the process compartment.

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
