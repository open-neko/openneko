# Go agent harness — M1 compatibility slice

This module qualifies Ax before wiring the new backend into the worker and web app.
It is not a selectable production backend yet. Ax is pinned to
`v0.0.0-20260918074503-5c43344f9ef3` in `go.mod` and `go.sum`.

Run from this directory:

```sh
go test -race -count=1 -v -timeout 60s ./...
go vet ./...
```

The tests use actual loopback HTTP requests through Ax's provider transport and
actual Goja execution. Only the model responses and external tool effects are
controlled fixtures. No API keys, live model calls, Docker services or active
OpenNeko stack are needed. The existing Go CI workflow runs the suite on Linux;
local verification on 2026-09-19 used Go 1.24.5 on macOS arm64. CI has not run yet.

## Verified contracts

- Native AxGen tool calls cross the host callback boundary. Allowed, denied and
  invalid-argument calls each produce one matching provider-visible tool result.
- Full AxAgent distiller/executor/responder execution over HTTP runs two host
  callbacks in one real Goja step. Only the allowed callback executes its effect;
  the responder receives both the real result and the denial.
- Cancelling an SSE request closes the upstream HTTP request. An interrupted,
  incomplete streamed tool call never dispatches.
- Cancelling a native callback retains exactly one terminal error result in Ax
  memory. A callback bound to a cancelled run cannot admit another effect.
- Generation, model and native-tool spans have parents and end once. Missing
  provider usage generates no usage-observer event; accounting must track that
  missing coverage separately.
- Actor callbacks can cooperate with a captured run context. CPU-only actor code
  is bounded by Goja's execution timeout, not immediate context cancellation.
- JSON runtime data can be snapshotted and restored through both Goja and the
  Agent-level API. Export/restore trace events and exported context-event state
  are available. Functions are omitted;
  oversized snapshots carry a truncation marker. Neither is a durable VM checkpoint.

## Pinned Ax compatibility issue

The synchronous native-tool path invokes `Tool.invoke` with a background context.
When `ContextHandler` is used, its scoped wrapper inherits that background context
instead of the generation context. Our first HTTP checks reproduced missing tool
spans and a hanging cancellation callback.

`internal/axbridge.BindTool` binds the host operation to one run's context and uses
Ax's run-scoped `Handler` wrapper for tracing. Both cancellation and span checks
pass with it. It is a narrow compatibility adapter, not a permission policy or
execution journal. Never reuse a bound tool for another run. Remove it only after
an upstream revision passes the same tests without it. No upstream issue or patch
has been published.

## Scope and remaining gates

The in-memory probe in the tests proves where authorization/journal hooks can run;
it does not provide durable storage or crash safety. The simple fixture validation
is not a production JSON-schema validator. Tool results on process crash still
require host-owned recovery.

Native-tool coverage here is AxGen's synchronous path. AxAgent's native async-tool
path is feature-gated by provider/session capability and background-tool mode; it
is not qualified by this basic OpenAI-compatible HTTP fixture. The initial agent
path uses explicitly registered, run-bound Goja callbacks. Do not enable Ax's
background/native async mode on the strength of these tests.

The qualified continuation boundary is an explicit, completed actor-step snapshot
of JSON data, with host callbacks freshly rebound to the new run. This does not
restore a suspended JavaScript stack, external effects or a full conversation.
Live context-compaction telemetry and durable crash recovery remain later gates.
M2 adds the real queue/worker/launcher/OpenShell path; M3
adds the existing browser/web flow and real seeded GraphJin. None of those services
were exercised by this suite. Do not describe these checks as full-stack acceptance.
