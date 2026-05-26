# OpenNeko Plugins

OpenNeko can be extended with sandboxed plugins that add new action kinds — web search via Parallel.ai, posting messages and DMs to Slack, sending Gmail / updating Shopify orders / appending Sheets rows on each operator's connected account, more on the way. Every plugin runs inside a microsandbox microVM with outbound network limited to the hosts the plugin's manifest declared at install time, and any secrets it needs (Slack bot tokens, API keys, OAuth tokens) live in a per-user `~/.config/openneko/secrets.json` (0600 perms) that the worker injects into the VM at exec time — never in `openneko.plugins.json` (which is tracked) and never in `action_request.payload` (which is logged).

## Capabilities

Plugins can declare three capabilities (any combination):

- **`action`** — typed action handlers the agent invokes (e.g. Slack's `send_slack_message`).
- **`auth`** — singleton SSO provider for the whole deployment (e.g. Scalekit). Lights up "Sign in with X" on `/signin`.
- **`connect`** — per-operator OAuth (e.g. Google Workspace). Non-singleton; each operator authorises their own account at `/integrations` and OpenNeko persists their refresh token in the per-operator slot of the secrets file. The worker injects the right operator's credential at action invocation time.

## Installing plugins

Install from the official marketplace. The `openneko` CLI is a single Go binary — install via Homebrew (macOS) or download from the GitHub Releases page (Linux):

```bash
# macOS
brew install open-neko/tap/openneko

# Linux — download from https://github.com/open-neko/neko/releases
# or, from a repo checkout while developing:
pnpm openneko init
pnpm openneko install @open-neko/plugin-parallel-search
```

`pnpm openneko …` runs the Go binary via `go run` for developers working from a repo checkout; everyone else uses the installed `openneko` binary directly.

**No worker restart needed.** OpenNeko's plugin registry watches `openneko.plugins.json` and the per-user secrets file; new plugins are usable on the next action_request, rotated secrets take effect on the next execute_action. Each plugin's microVM starts lazily on first use.

When you run the brew/release binary, the host `openneko` CLI auto-detects the running `openneko-*-worker-1` container and proxies plugin-op commands into it via `docker exec`, so you can install from your laptop without extra setup. If a plugin declares required env values (Slack tokens, API keys), the CLI prompts at install time with hidden input and saves them to the secrets file inside the container. Pass `--local` to bypass the proxy and install host-side instead (useful for source-build dev workflows where the worker runs via `pnpm dev`, not docker).

Browse the marketplace at [open-neko.github.io/plugins](https://open-neko.github.io/plugins/). Run `openneko doctor` to check that your host can run microsandbox.

## Federated marketplaces

The official marketplace ships only first-party `@open-neko/*` plugins that the OpenNeko team writes and supports. Anyone else can publish their own `marketplace.json` at any stable URL and operators trust it explicitly:

```bash
openneko marketplace add https://example.com/marketplace.json
openneko install @example/openneko-plugin-foo
```

OpenNeko makes no representation about non-official marketplaces — that trust is between the operator and the publisher. The sandbox enforces capability declarations regardless of where a plugin came from. See [open-neko/plugins/CONTRIBUTING.md](https://github.com/open-neko/plugins/blob/main/CONTRIBUTING.md) for the marketplace publish guide.

## Bypass every marketplace (`--unverified`)

To install a plugin directly from npm without going through any marketplace (plugin authoring, or an emergency hotfix before a marketplace entry exists):

```bash
openneko install <npm-package-name> --unverified
```

The CLI prints a loud warning. The integrity hash is taken on trust from npm rather than verified against a marketplace listing; everything else (sandboxing, manifest capability enforcement) still applies.

## Install a community skill from a git URL

For pure-procedural-knowledge skills (no network, no secrets) that ship as agentskills.io-spec SKILL.md bundles without an npm package — e.g. anything in the Hermes or Claude Code skill catalogs — install directly from a git URL:

```bash
# Whole-repo skill:
openneko install https://github.com/owner/repo

# Skill inside a monorepo:
openneko install https://github.com/NousResearch/hermes-agent#optional-skills/finance/dcf-model
```

The CLI clones the URL (shallow), validates the `SKILL.md` against the agentskills.io spec, and drops the folder under `~/.openneko/skills/<skill-name>/`. The worker picks it up on the next agent turn; `pnpm skills:check` (or `openneko doctor` going forward) reports any missing binaries it declared in `prerequisites.commands`. URLs must be https against github / gitlab / codeberg — same allowlist the marketplace schema uses.

## Install policy

Every install path above is gated by a deployment-wide policy at `/settings/security`. Defaults are secure-by-default — `--unverified` and git-URL installs are both off out of the box; any signed-in operator opts in. (OpenNeko has no admin/member role separation today — every signed-in operator can change the install policy, same as every other `/settings` route. A real role gate is a future change.) The full switch list:

| Switch | What it controls |
|---|---|
| `allowUnverified` | `openneko install <pkg> --unverified` |
| `allowGitUrlInstalls` | `openneko install <git-url>` |
| `allowedMarketplaces` | Which `marketplace.json` URLs the install path trusts (official is always on) |
| `allowSandboxedSkillEscape` | When installing an untrusted community skill, wrap its shell blocks in a one-shot microVM |

When you flip a switch off, pre-existing installs are **flagged, not yanked** — the registry's status surfaces them as needing operator attention so they can be removed via `openneko remove` manually. Every install entry records `installSource` + `installedAt` + `policySnapshot` for audit.

## Per-operator integrations (`/integrations`)

Plugins that declare a `connect` capability (Google Workspace today) authorise per-operator OAuth — each operator visits `/integrations` in the web UI, clicks **Connect**, runs the OAuth flow in their own browser, and their tokens land in their own slot in `secrets.json`. The worker injects the right operator's credential at action invocation time. Disconnect is one click; the credential is wiped from the file. Refresh-token rotation happens inside the plugin's sandbox VM and gets persisted by the worker — the plugin never writes to disk directly.

## Host support

| Host | Plugin system |
|---|---|
| macOS arm64 (Apple Silicon) | ✓ supported |
| Linux x86_64 with `/dev/kvm` | ✓ supported |
| Linux arm64 with `/dev/kvm` | ✓ supported |
| macOS x86_64 (Intel) | ✗ not supported (microsandbox ships arm64 only on macOS) |
| Linux without KVM | ✗ not supported |
| Windows | ✗ WSL2 viability is being evaluated |

Plugins run inside a microVM with hardware-virtualization acceleration, which is why the host matters:

- **macOS arm64 with the brew-installed binary + `openneko start --mode demo`**: plugin *installation* works (the CLI proxies into the worker container, npm installs into an isolated dir, `plugins.json` hot-reloads), but plugin *execution* needs Hypervisor.framework, which Docker Desktop hides from Linux containers — so the microVM spawn fails at action time. Installs land cleanly and the marketplace is browsable, but actions that need a plugin VM no-op. Full execution requires the source-build *Developer Setup* path (`pnpm dev` runs the worker on the macOS host, where microsandbox uses Hypervisor.framework directly).
- **Linux with `/dev/kvm`** (amd64 or arm64): both install AND execution supported. The worker container needs `/dev/kvm` passed in; the embedded compose's `plugins.linux.yml` overlay does this automatically when the binary detects KVM at start time.

On unsupported hosts the plugin subsystem is disabled with a clear log line; OpenNeko itself still runs. The built-in `send_webhook` action adapter (no sandbox needed) remains as the extensibility escape hatch.
