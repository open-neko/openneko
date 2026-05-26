# OpenNeko Plugins

Sandboxed plugins add new action kinds — Slack, Gmail, web search, Shopify, Sheets, and more. Browse the marketplace at **[open-neko.github.io/plugins](https://open-neko.github.io/plugins/)**.

Every plugin runs inside a microsandbox microVM with outbound network limited to its manifest's allowlist. Secrets (bot tokens, API keys, OAuth tokens) live in `~/.config/openneko/secrets.json` (0600 perms) and never touch `openneko.plugins.json` (tracked) or `action_request.payload` (logged).

## Capabilities

Any combination of:

- **`action`** — typed handlers the agent invokes (e.g. Slack's `send_slack_message`).
- **`auth`** — singleton SSO provider for the deployment (e.g. Scalekit). Lights up "Sign in with X" on `/signin`.
- **`connect`** — per-operator OAuth (e.g. Google Workspace). Operators authorise their own account at `/integrations`; the worker injects the right credential per invocation.

## Installing

```bash
openneko install @open-neko/plugin-parallel-search
```

The host CLI proxies into the running worker container via `docker exec`, so installs land inside the sandbox boundary. Plugins with required env values prompt at install time (hidden input) and write to the secrets file. Pass `--local` to bypass the proxy (for source-build dev flows where the worker runs via `pnpm dev`).

From a repo checkout:

```bash
pnpm openneko init
pnpm openneko install @open-neko/plugin-parallel-search
```

The registry watches `openneko.plugins.json` and the secrets file — no worker restart needed. MicroVMs start lazily on first use. `openneko doctor` checks your host can run microsandbox.

## Federated marketplaces

The official marketplace only ships first-party `@open-neko/*` plugins. Anyone can publish their own `marketplace.json`; operators add it explicitly:

```bash
openneko marketplace add https://example.com/marketplace.json
openneko install @example/openneko-plugin-foo
```

Non-official marketplaces are operator-trusted, not OpenNeko-vouched. The sandbox enforces capability declarations regardless of source. See [open-neko/plugins/CONTRIBUTING.md](https://github.com/open-neko/plugins/blob/main/CONTRIBUTING.md) to publish.

## Bypass marketplaces (`--unverified`)

Install directly from npm:

```bash
openneko install <npm-package-name> --unverified
```

The CLI prints a loud warning. Sandboxing and manifest enforcement still apply; only the marketplace integrity check is skipped.

## Install a community skill from a git URL

For pure-procedural-knowledge skills (no network, no secrets) shipped as agentskills.io-spec `SKILL.md` bundles:

```bash
# Whole-repo skill:
openneko install https://github.com/owner/repo

# Skill inside a monorepo:
openneko install https://github.com/NousResearch/hermes-agent#optional-skills/finance/dcf-model
```

The CLI shallow-clones, validates `SKILL.md`, and drops the folder under `~/.openneko/skills/<skill-name>/`. The worker picks it up on the next agent turn. URLs must be HTTPS against github / gitlab / codeberg.

## Install policy

`/settings/security` gates every install path. Defaults are secure: `--unverified` and git-URL installs are off; any signed-in operator opts in. (No admin/member separation today — that's a future change.)

| Switch | Controls |
|---|---|
| `allowUnverified` | `openneko install <pkg> --unverified` |
| `allowGitUrlInstalls` | `openneko install <git-url>` |
| `allowedMarketplaces` | Which `marketplace.json` URLs the install path trusts (official is always on) |
| `allowSandboxedSkillEscape` | Wrap untrusted community skill shell blocks in a one-shot microVM |

Flipping a switch off flags pre-existing installs — not yanked — so the registry surfaces them for manual `openneko remove`. Every install records `installSource` + `installedAt` + `policySnapshot`.

## Per-operator integrations (`/integrations`)

Plugins with `connect` (Google Workspace today) authorise per-operator OAuth. Each operator visits `/integrations`, clicks **Connect**, runs OAuth in their browser, and their tokens land in their secrets slot. The worker injects the right operator's credential per invocation. Disconnect wipes the credential. Refresh-token rotation happens inside the plugin sandbox and gets persisted by the worker.

## Host support

| Host | Plugin system |
|---|---|
| macOS arm64 (Apple Silicon) | ✓ supported |
| Linux x86_64 with `/dev/kvm` | ✓ supported |
| Linux arm64 with `/dev/kvm` | ✓ supported |
| macOS x86_64 (Intel) | ✗ microsandbox is arm64-only on macOS |
| Linux without KVM | ✗ |
| Windows | ✗ WSL2 viability under evaluation |

Plugins need hardware-virtualisation acceleration.

- **macOS arm64 with the brew binary**: install works (CLI proxies into the worker container, `plugins.json` hot-reloads), but execution fails because Docker Desktop hides Hypervisor.framework from Linux containers. Use *Developer Setup* (`pnpm dev` runs the worker on the host) for execution.
- **Linux with `/dev/kvm`** (amd64 or arm64): install and execution both work. The embedded compose's `plugins.linux.yml` overlay passes `/dev/kvm` into the worker container automatically.

On unsupported hosts the plugin subsystem is disabled with a clear log line; OpenNeko itself still runs. The built-in `send_webhook` adapter (no sandbox) remains as an escape hatch.
