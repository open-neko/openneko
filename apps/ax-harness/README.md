# Ax Harness

Ax Harness is a Go agent harness built on the [Ax](https://github.com/ax-llm/ax)
framework. It is OpenNeko's agent backend.

The harness runs one agent turn. It reads one run specification, runs an Ax
agent with tools, and writes events as JSON lines. `openneko serve
--agent-socket` runs each turn in a confined runner process, one turn per
socket connection. The `ax-harness` binary refuses a turn. The host owns
history, approvals, the sandbox and scheduling.

## Layout

| Path | Contents |
| --- | --- |
| `internal/agent` | The agent loop, budgets, the budget summary, usage and cost, model routes and fallback, reasoning, the child agent and the skills catalog. |
| `internal/command` | Run input, model routes for any Ax provider, and the event stream. |
| `internal/mcp` | A stdio MCP client that exposes server tools as capabilities. |
| `internal/localtool` | File, upload and skill tools confined to one directory, and the `terminal` tool. |
| `sandbox` | The client that sends `terminal` commands, with the run's changed files, to the external sandbox service and writes back the files they changed. |
| `harness` | One turn in-process (`Run`, `RunTurn`). It is the only OpenNeko-aware code. |
| `embedding` | The gateway to an OpenAI-compatible embedding service at a URL. |
| `serve` | Turns, host model calls and embedding calls over a Unix socket for `openneko serve`. The socket protocol is in `serve/serve.go`. |
| `keydoor` | The serve side of the key door. Serve gets a token in place of each model key and sends model calls through the door in `openneko web`. |
| `inference` | The one gateway to model providers, for agent turns and the host's own calls. |
| `catalog` | Model call prices from the model catalog of the pinned Ax module. |
| `webfetch` | The `web_fetch` tool. It runs in `openneko web`, and the runner reaches it over the tool bridge. |
| `ownacl` | Limits files and folders to the account that runs OpenNeko. |
| `cmd/ax-harness` | The binary: the model catalog, and host generate and embed calls. It refuses a turn. |
| `compat` | Checks of the pinned Ax behavior that the harness uses. |

The protocol is in [docs/RUN-PROTOCOL.md](docs/RUN-PROTOCOL.md). Notes on Ax
development are in [docs/AX-DEVELOPMENT.md](docs/AX-DEVELOPMENT.md).

## Checks

```sh
go vet ./...
go test -race -count=1 -timeout 10m ./...
```
