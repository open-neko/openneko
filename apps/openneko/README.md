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
[github.com/open-neko/openneko/releases](https://github.com/open-neko/openneko/releases)
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

### Plugin + skill management

```bash
openneko init
openneko install <name>[@<marketplace>] [--version <v>] [--unverified]
openneko install <git-url>[#<sub-path>]
openneko remove <name>
openneko list
openneko marketplace {list,add,remove}
openneko secrets {list,set,unset}
openneko doctor
```

Two install lanes:

1. **Marketplace (default)** — `openneko install <name>` resolves against
   trusted marketplaces, runs `npm install`, writes the manifest entry.
   `--unverified` bypasses every marketplace and installs directly from
   npm. Both gated by the deployment install policy at `/settings/security`.

2. **Git URL** — `openneko install <https-url>[#<sub-path>]` clones the
   URL (shallow), validates the agentskills.io-spec `SKILL.md` at the
   root or the given sub-path, and copies the folder under
   `~/.openneko/skills/<skill-name>/`. No plugin half, no npm install —
   for pure-procedural community skills. URLs must be https against
   github / gitlab / codeberg. Gated by `allowGitUrlInstalls`.

Packages whose `package.json` declares `openneko.skill: "./skill"` also
drop the bundled SKILL.md half under `~/.openneko/skills/<name>/` during
install — that's how connectors like Google Workspace and Shopify ship
their procedural-knowledge skills alongside their typed action handlers.

The binary manages three things on the operator's host:

- `openneko.plugins.json` (tracked) — installed plugins manifest; each
  entry carries `installSource` + `installedAt` + `policySnapshot` for
  audit.
- `~/.config/openneko/secrets.json` (mode 0600) — per-user env vars for
  static API keys, plus `_operators[opId][plugin]` slots for per-operator
  OAuth credentials produced by `connect`-capable plugins.
- `~/.openneko/skills/` — community + bundled-half skill folders the
  worker loads at agent-turn time. `pnpm skills:check` validates declared
  deps.

See the repo-root [README](../../README.md) for the full operator-side
docs including `/integrations` (per-operator OAuth) and `/settings/security`
(install-policy switches).

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
