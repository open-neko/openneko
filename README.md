# OpenNeko

[![License](https://img.shields.io/badge/license-Apache_2.0-blue)](LICENSE)
[![Release](https://img.shields.io/github/v/release/open-neko/openneko)](https://github.com/open-neko/openneko/releases/latest)
[![Self-hosted · Docker](https://img.shields.io/badge/self--hosted-Docker-2496ED?logo=docker&logoColor=white)](INSTALL.md)
[![openneko.app](https://img.shields.io/badge/openneko.app-website-111111)](https://openneko.app)
[![Stars](https://img.shields.io/github/stars/open-neko/openneko?style=social)](https://github.com/open-neko/openneko/stargazers)

**OpenNeko watches your business data, flags what's worth a look, and drafts the next action — for you to approve.** Self-hosted, on your infrastructure, with whichever LLM you prefer.

> The intelligence is rented; the findings, rules, and decisions are yours. Models keep changing — the memory of how your business actually runs (promises, exceptions, baselines, decisions) shouldn't live inside the vendor that rents you the model. OpenNeko keeps the agent **and** that memory on your infrastructure.

![OpenNeko on mobile — Briefing, Ask, Workflows](cfo-briefing.png)

## Quickstart

You'll need **Docker** and **one LLM provider API key**.

**macOS** — Homebrew:

```bash
brew install open-neko/tap/openneko
mkdir -p ~/openneko && cd ~/openneko
openneko start --mode demo --detach
```

**Linux** — latest release binary:

```bash
TAG=$(curl -fsSL https://api.github.com/repos/open-neko/openneko/releases/latest | grep -oE '"tag_name": *"[^"]+"' | head -1 | cut -d'"' -f4)
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
curl -fsSL "https://github.com/open-neko/openneko/releases/download/$TAG/openneko_${TAG#v}_linux_$ARCH.tar.gz" | tar -xz openneko
sudo install -m 0755 openneko /usr/local/bin/ && rm -f openneko
mkdir -p ~/openneko && cd ~/openneko
openneko start --mode demo --detach
```

Open [http://localhost:3000](http://localhost:3000) and finish the setup wizard. The demo seeds three watchers against sample data — kick off **Slow-Ship Operations** from `/workflows` and watch an *"orders stuck in pending > 5 days"* finding land on your Briefing.

Full propose-and-approve walkthrough, the live trial (order simulator + scenario injector), and connecting your own data → **[INSTALL.md](INSTALL.md)**.

## What you get

The full feature catalog, in plain language, lives in **[FEATURES.md](FEATURES.md)** — ask-anything answers, chat-first administration, watchers, channels (Slack / WhatsApp / Telegram), personal-vs-team knowledge, and the verifiable security model. The highlights:

- **Operational findings on the Briefing.** A SKU below reorder, orders stuck in *pending* for days, payment retries piling up — checked on a schedule and posted as findings.
- **Watchers you describe in plain English.** *"Alert me when any SKU's on-hand stock drops below its reorder point."* OpenNeko schedules it, runs it, and writes up what it found.
- **Actions drafted, not auto-fired.** Slack alerts, Gmail follow-ups, Sheets updates, Shopify writes — proposed with the finding that triggered them, queued for your approval. Write a rule when you want a specific class of safe action to auto-fire.
- **Follow-up questions about your data, in chat.** *"Which territories drove this week's revenue drop?"* — drill in on a finding without leaving the app or writing SQL.
- **A complete record of what fired, when, and why.** Every proposal, decision, and execution stays against the finding that triggered it — auditable, searchable, yours.

## How your data gets in

OpenNeko reads through **[GraphJin](https://graphjin.com)**, a GraphQL gateway you point at data you already have:

- **Databases** — Postgres, MySQL, and [more](https://graphjin.com), with an auto-generated query surface.
- **Files, external APIs, custom code** — first-class GraphQL fields via GraphJin's script layer.

One consistent surface no matter where the data lives — you don't write per-connector plumbing.

## What stays yours

OpenNeko separates the *intelligence* (the model) from the *memory* (your business's context) on purpose. Swap the model whenever something better ships; the rest doesn't move.

The memory layer runs on your own Postgres, on your own infrastructure:

- **Findings & briefings** — every watcher run and result, kept against the watcher that produced it.
- **Pinned facts & learned rules** — what the agent figured out and you confirmed, kept separate from what the agent proposed and you haven't decided on yet.
- **Action policies** — your rules for what auto-fires, what queues, what's blocked.
- **Decision history** — every approval, rejection, and execution receipt.

Apache 2.0, self-hosted, single Postgres. Take a backup, take it with you.

## Plugins

Add new action kinds with sandboxed plugins — web search, Slack, Gmail, Shopify, Sheets, Telegram, and more.

- **Every plugin runs in an isolated microVM**, with outbound network limited to what its manifest declares.
- **Secrets are scoped per plugin** and never reach the model context.

Browse the [marketplace](https://open-neko.github.io/plugins/) · capability model, install policy, and host support in **[PLUGINS.md](PLUGINS.md)**.

```bash
openneko install @open-neko/plugin-parallel-search
```

## Under the hood

- **Self-hosted via Docker.** One binary, one `start` command. Data lives on your Postgres.
- **Bring your own LLM.** Hermes runs against Anthropic, OpenAI, Google, Ollama, and others; Claude Agent runs Anthropic in-process. Pick per task, swap any time.
- **Plugins in OpenShell sandboxes.** Outbound network is allowlisted per manifest, not blanket-open.
- **Agent in a sandbox by default.** The agent loop itself runs inside an OpenShell policy sandbox — default-deny egress, and the model API key never enters the box (the gateway proxy injects it on the wire). See [OPENSHELL.md](OPENSHELL.md).
- **Apache 2.0.** Inspect everything. Fork it. Take your data with you.

## Docs

- [INSTALL.md](INSTALL.md) — install, [upgrade](INSTALL.md#upgrade), requirements, troubleshooting, connecting your data, full demo trial
- [PLUGINS.md](PLUGINS.md) — plugin capabilities, sandbox/security model, marketplaces, install policy, host support
- [OPENSHELL.md](OPENSHELL.md) — preview: running the agent itself in an OpenShell policy sandbox (architecture, security, how to enable)
- [CONTRIBUTING.md](CONTRIBUTING.md) — dev setup, repo layout, pre-PR checks
- [CHANGELOG.md](CHANGELOG.md) — releases

## Issues

Please file bugs and feature requests at [github.com/open-neko/openneko/issues](https://github.com/open-neko/openneko/issues).

## Contributing

Pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the developer setup, repository layout, and the checks to run before opening a PR.

## License

OpenNeko is licensed under the [Apache License 2.0](LICENSE).

## Author

Created by [Amit Deshmukh](https://openneko.app/#about).
