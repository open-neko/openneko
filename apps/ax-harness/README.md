# Ax Harness

Ax Harness is a Go agent harness built on the [Ax](https://github.com/ax-llm/ax)
framework. It is OpenNeko's agent backend.

The harness runs one agent turn. It reads one run specification, runs an Ax
agent with tools, and writes events as JSON lines. The `ax-harness` binary runs
one turn per process on stdin and stdout. `openneko serve --agent-socket` runs
many turns at once in one process, one turn per socket connection. The host
owns history, approvals, the sandbox and scheduling.

## Layout

| Path | Contents |
| --- | --- |
| `internal/agent` | The agent loop, budgets, the budget summary, usage and cost, model routes and fallback, reasoning, the child agent and the skills catalog. |
| `internal/command` | Run input, model routes for any Ax provider, and the event stream. |
| `internal/mcp` | A stdio MCP client that exposes server tools as capabilities. |
| `internal/localtool` | File, upload and skill tools confined to one directory, and the `terminal` tool. |
| `sandbox` | The client that sends `terminal` commands to the external sandbox service. |
| `harness` | One turn in-process (`Run`, `RunTurn`). It is the only OpenNeko-aware code. |
| `serve` | Turns over a Unix socket for `openneko serve`. The socket protocol is in `serve/serve.go`. |
| `cmd/ax-harness` | The binary: one turn per process. |
| `compat` | Checks of the pinned Ax behavior that the harness uses. |

The protocol is in [docs/RUN-PROTOCOL.md](docs/RUN-PROTOCOL.md). Notes on Ax
development are in [docs/AX-DEVELOPMENT.md](docs/AX-DEVELOPMENT.md).

## Checks

```sh
go vet ./...
go test -race -count=1 -timeout 10m ./...
```

`internal/mcp` has opt-in tests against the real OpenNeko bridge. Install the
`apps/worker` dependencies, then set `OPENNEKO_BRIDGE_TEST=1`.
