# Harness stocktake

Reviewed 2026-09-19 against this working tree and local test results.

## Current position

This is an independent Go repository and a tightly integrated companion project to
OpenNeko. It contains compatibility experiments, a small Ax binding, a consumer
launcher shim and real OpenShell tests. It does **not** yet contain a usable agent
runtime. M1's synchronous execution path is qualified locally; M2 is partially
qualified; M3–M8 remain unimplemented or unverified.

## Repository organization

```text
README.md                         Entry point, ownership and commands
go.mod / go.sum                  Independent module and pinned Ax dependency
internal/axbridge/               Run-bound Ax callback compatibility
compat/                          Ax HTTP, Goja and lifecycle contract tests
integration/                     Consumer-neutral OpenShell test deployment
adapters/openneko/
   cmd/openshell-compat/           Optional CLI shim executable
   internal/openshellcompat/      Legacy argument translation and tests
   integration.sh                 Optional real launcher compatibility checks
   README.md                      Consumer contract and remaining integration
docs/                            Design, OpenShell research, milestones, stocktake
.github/workflows/go.yml         Race tests, vet and dependency-boundary check
```

Core code does not import consumer adapters. The Go module namespace identifies
this project, not an application dependency. No OpenNeko checkout is needed for
builds or standalone tests. The local `claude_code` reference dump, built probes
and `bin/` outputs are ignored. No empty future-runtime packages were scaffolded.

## Implemented and verified

| Area | Present evidence | Limit |
| --- | --- | --- |
| Ax HTTP execution | Native tool allow/deny/invalid-argument checks and provider-visible result pairing | Tests of pinned Ax behavior, not a durable harness invariant across crashes |
| AxAgent / Goja | Full actor callback path, two callbacks, denial, responder evidence, CPU timeout | Background/native async tool mode remains unqualified |
| Cancellation | HTTP stream closure, native cancellation result, partial-call non-dispatch, run-bound callback cancellation | No worker-to-sandbox-to-GraphJin cancellation chain yet |
| Snapshots | Goja and Agent JSON state round trips; omitted functions and truncation behavior | Completed-step serialization only; no arbitrary crash resume |
| Telemetry | In-process run-scoped model/tool span checks and missing-usage behavior | No end-to-end collector export, neutral event schema or production telemetry pipeline |
| OpenShell transport | Separate 0.0.116 gateway, mTLS, actual sandbox Go/Ax streaming, synthetic key replacement | Model fixture uses HTTP; gateway mTLS does not prove model HTTPS interception |
| OpenShell denial | Wrong credential-binding path and unauthorized curl binary each return HTTP 403 | Not a complete containment audit |
| OpenNeko CLI adapter | Legacy failure reproduced; translated create/upload/exec/delete; failed-upload cleanup | CLI contract checks, not execution of the real worker launcher or warm pool |
| Repository checks | Race tests, vet, shell syntax and no core dependency on adapters | Hosted Linux CI is configured but has not run |

Local commands successfully run during the organization work:

```sh
go test -race -count=1 -timeout 60s ./...
go vet ./...
OPENSHELL_TEST_CLI=/tmp/openneko-m2-tools/openshell ./integration/run.sh
OPENSHELL_TEST_CLI=/tmp/openneko-m2-tools/openshell ./integration/run.sh adapters/openneko/integration.sh
```

The CLI path is local evidence, not a dependency on that installation path. Both
live runs ended with `openshell_transport_suite` success. The adapter run also
reported `legacy_launcher_adapter_upload_exec_delete` and
`legacy_launcher_upload_failure_cleanup` success. Test containers were cleaned up.
Images remain cached. No real model credentials were used. See the
[integration record](../integration/README.md) for platform and image digests.

## Not built yet

- Headless runtime entrypoint, concrete versioned run/event/result types, and a
  session reducer with durable input acceptance and terminal-state ownership.
- Journal/checkpoints, operation reconciliation, approval continuations and
  crash-recovery tests.
- Production tool pipeline, read-parallel/write-exclusive scheduler, Read/Edit/Bash
  tools, freshness checks and process-tree lifecycle management.
- Approved routing profiles and fallback, aggregate budget controls, context
  compaction and run-wide retry limits. Ax supplies capabilities; this project has
  not yet integrated and qualified these controls.
- GraphJin broker client, product event/result projection, deployable agent image
  and real worker/queue/broker/web integration.
- Production observability/export, evaluation corpus and release/rollback artifacts.

## Remaining M2 gates

Qualify model HTTPS interception and Go CA trust, query authentication, two provider
routes, managed refresh/expiry, static rotation/detach, streaming cancellation/idle
behavior and actual OTLP collector delivery. Then exercise the real consumer
worker, queue and broker. Probe success does not close those gates.

## Decisions and next sequence

1. Keep this repository independent, with static consumer adapters. OpenNeko is the
   concrete first consumer; do not add a generic plugin framework.
2. Finish the transport gates, starting with HTTPS/Go CA trust and cancellation in
   the real sandbox. They determine whether the deployed runtime path is sound.
3. Implement one headless runtime slice with explicit input, ordered events, one
   terminal result and cancellation. Define concrete types alongside that behavior;
   integrate neutral telemetry at the same time.
4. Connect that slice to the real OpenNeko worker and read-only server-side GraphJin
   agent. Compare an adapter-only deployment with a minimal explicit product
   entrypoint change before making the CLI shim a permanent requirement.
5. Advance recovery, tools and routing using the progressive
   [milestone gates](MILESTONES.md), including browser-visible verification.

## Repository and historical state

The local Harness Git repository is initialized on `main`, with no commits or
remote yet. Files remain uncommitted; nothing was pushed. The earlier OpenNeko
M1 commit `643b4a8` remains historical and has not been reverted. The sibling
`OpenNeko-harness` worktree also retains earlier uncommitted README/integration
copies; current implementation authority is this repository. Those copies need
explicit reconciliation before any later product commit, not silent deletion.
No OpenNeko source was changed during this organization pass.
