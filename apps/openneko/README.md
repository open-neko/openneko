# openneko

The OpenNeko operator CLI — a single Go binary that supervises the Docker
stack and manages sandboxed plugins. Same binary works on an operator's
laptop, on a self-hosted server, and inside the worker container.

## Install

**macOS (Apple Silicon):**

```bash
brew install open-neko/tap/openneko
```

**Linux (amd64 / arm64):** download the tarball for your platform from
[github.com/open-neko/neko/releases](https://github.com/open-neko/neko/releases)
and put `openneko` somewhere on your `PATH`.

You also need Docker (Docker Desktop on macOS, Docker Engine on Linux).

## Usage

### Stack supervision

```bash
openneko start [--mode prod|dev|demo] [--detach]
openneko stop [--volumes]
openneko status
openneko logs [service…] [-f]
openneko migrate
openneko seed adventureworks
openneko reset [--all]
```

Modes:

- `prod` — core services only (default)
- `dev` — core + dev tooling
- `demo` — core + AdventureWorks trial bundle

The binary materializes its embedded compose files to `.openneko/runtime/`
in the current working directory before invoking `docker compose`. A
project- or user-level override at `~/.config/openneko/compose.override.yml`
is appended automatically when present.

### Plugin management

```bash
openneko init
openneko install <name>[@<marketplace>] [--version <v>] [--unverified]
openneko remove <name>
openneko list
openneko marketplace {list,add,remove}
openneko secrets {list,set,unset}
openneko doctor
```

Plugin packages install via `npm install`; the binary itself only manages
the manifest (`openneko.plugins.json`) and the per-user secrets store at
`~/.config/openneko/secrets.json` (mode 0600). See the repo-root
[README](../../README.md) for the full plugin docs.

## Building from source

```bash
cd apps/openneko
go build -o openneko ./cmd/openneko
./openneko version
```

## Testing

```bash
go test ./... -count=1
# Integration tests (need docker on the host):
go test -tags=integration -count=1 -timeout 10m ./internal/db/...
```

## License

Apache 2.0. See [LICENSE](./LICENSE).
