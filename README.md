# Harness

Independent Go agent harness using Ax, with OpenShell execution support.
OpenNeko is the first consumer, integrated through an optional adapter. The Go
module namespace `github.com/open-neko/harness` names this repository; it does not
import or require the OpenNeko application. No sibling checkout is needed to build
or run the standalone checks.

## Current implementation

- `cmd/harness`, `internal/agent`: initial headless AxAgent run and NDJSON lifecycle.
- `internal/axbridge`: run-bound Ax tool callbacks for cancellation and telemetry.
- `compat`: actual HTTP/Goja checks for tool pairing, cancellation and snapshots.
- `integration`: isolated real OpenShell transport and credential-policy checks.
- `internal/session`: bounded durable acceptance, typed operation receipts and recovery.
- `adapters/openneko`: optional GraphJin lookup, governed proposals and product launch adapter.

The [headless run protocol](docs/RUN-PROTOCOL.md), M3 product path and
[M4 governed recovery](docs/M4-RECOVERY.md) are implemented and locally verified.
Acceptance covers real web approval/reload, queue worker death, broker, OpenShell,
GraphJin and controlled HTTP effects. Unknown effects are never automatically
redispatched. M2 raw OpenShell proxy cancellation is an accepted upstream limitation;
arbitrary VM recovery, M5–M8 and production rollout are not qualified.

```sh
go test -race -count=1 -timeout 60s ./...
go vet ./...
OPENSHELL_TEST_CLI=/absolute/path/to/openshell-0.0.116 ./integration/run.sh
```

The default integration suite requires Docker and the pinned OpenShell CLI; it
requires no application worker, broker, database or web app. Consumer acceptance
checks are separate and will progressively exercise those real services.

## Integration boundary

The harness owns execution semantics, Ax routing, tools, recovery and neutral
telemetry. Consumers supply trusted run configuration, authorization, model routes,
scoped capabilities and persistence/telemetry bindings. Consumer adapters translate
launch, event and result contracts; core packages never import them. No dynamic
plugin loader or consumer-specific policy is built into the core. OpenNeko is the
concrete first integration target, not a reason to generalize for hypothetical consumers.
Prefer a small, justified product entrypoint change over a permanent compatibility
workaround when it reduces total complexity.

See [the OpenNeko adapter](adapters/openneko/README.md) for its build and test commands.
The earlier M1 copy was committed to OpenNeko main as `643b4a8`; that historical
commit remains. The runtime lives here; the opt-in product adapter is a small separate OpenNeko change.

See [building blocks](docs/BUILDING-BLOCKS.md), [design](docs/DESIGN.md), [milestones](docs/MILESTONES.md), [OpenShell findings](docs/OPENSHELL.md)
and [live integration evidence](integration/README.md). The `claude_code` reference
dump and generated binaries are excluded from version control.

Current inventory and gaps: [stocktake](docs/STOCKTAKE.md).
