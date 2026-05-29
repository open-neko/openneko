# OpenNeko

[![License](https://img.shields.io/github/license/open-neko/openneko)](LICENSE)
[![Release](https://img.shields.io/github/v/release/open-neko/openneko)](https://github.com/open-neko/openneko/releases/latest)
[![Self-hosted · Docker](https://img.shields.io/badge/self--hosted-Docker-2496ED?logo=docker&logoColor=white)](INSTALL.md)
[![getneko.app](https://img.shields.io/badge/getneko.app-website-111111)](https://getneko.app)
[![Stars](https://img.shields.io/github/stars/open-neko/openneko?style=social)](https://github.com/open-neko/openneko/stargazers)

**OpenNeko watches your business data, points out what's worth a look, and drafts the actions to take next — for you to approve.** Self-hosted on your infrastructure, with whichever LLM you prefer. The intelligence is rented; the findings, rules, and decisions are yours.

Models will keep changing — better one this quarter, cheaper one next. What shouldn't keep changing is the memory of how your business actually runs: the promises, exceptions, baselines, and decisions. That layer doesn't belong inside the same vendor that rents you intelligence. OpenNeko keeps the agent and the memory layer on your infrastructure, even when the model isn't.

![OpenNeko on mobile — Briefing, Ask, Workflows](cfo-briefing.png)

## Quickstart

You'll need Docker and one LLM provider API key.

```bash
# macOS
brew install open-neko/tap/openneko
mkdir -p ~/openneko && cd ~/openneko
openneko start --mode demo --detach
```

Linux: download the binary from the [latest release](https://github.com/open-neko/openneko/releases/latest), then run the same `openneko start --mode demo --detach`.

Open [http://localhost:3000](http://localhost:3000) and finish the setup wizard. The demo seeds three watchers against sample data — kick off the Slow-Ship Operations watcher from `/workflows` and watch an *"orders stuck in pending > 5 days"* finding land on your Briefing. The full propose-action-and-approve walkthrough is in **[INSTALL.md](INSTALL.md)**.

Full trial flow (live order simulator + scenario injector) and connecting your own data: see **[INSTALL.md](INSTALL.md)**.

## What you get

- **Operational findings on the Briefing.** A SKU below reorder, orders stuck in *pending* for days, payment retries piling up — checked on a schedule and posted as findings.
- **Watchers you describe in plain English.** *"Alert me when any SKU's on-hand stock drops below its reorder point."* OpenNeko schedules it, runs it, and writes up what it found.
- **Actions drafted, not auto-fired.** Slack alerts, Gmail follow-ups, Sheets updates, Shopify writes — proposed with the finding that triggered them, queued for your approval. Write a rule when you want a specific class of safe action to auto-fire.
- **Follow-up questions about your data, in chat.** *"Which territories drove this week's revenue drop?"* — drill in on a finding without leaving the app or writing SQL.
- **A complete record of what fired, when, and why.** Every proposal, decision, and execution stays against the finding that triggered it — auditable, searchable, yours.

## How your data gets in

OpenNeko reads through **[GraphJin](https://graphjin.com)** — a GraphQL gateway you point at the data you already have. GraphJin auto-generates a query surface over your **databases** (Postgres, MySQL, SQL Server), and its script layer brings in **files, external APIs, and custom code** as first-class GraphQL fields. The agent queries one consistent surface no matter where the data actually lives — you don't write per-connector plumbing.

## What stays yours

OpenNeko separates the *intelligence* (the model) from the *memory* (your business's context) on purpose. Swap the model whenever something better ships; the rest doesn't move.

The memory layer runs on your own Postgres, on your own infrastructure:

- **Findings & briefings** — every watcher run and result, kept against the watcher that produced it.
- **Pinned facts & learned rules** — what the agent figured out and you confirmed, kept separate from what the agent proposed and you haven't decided on yet.
- **Action policies** — your rules for what auto-fires, what queues, what's blocked.
- **Decision history** — every approval, rejection, and execution receipt.

Apache 2.0, self-hosted, single Postgres. Take a backup, take it with you.

## Plugins

Extend OpenNeko with sandboxed plugins that add new action kinds — web search, Slack, Gmail, Shopify, Sheets, Telegram, and more. **Every plugin runs in an isolated microVM** with outbound network limited to what its manifest declares; secrets are scoped per plugin and never reach the model context. Browse the marketplace at [open-neko.github.io/plugins](https://open-neko.github.io/plugins/), and see **[PLUGINS.md](PLUGINS.md)** for the capability model, install policy, and host support.

```bash
openneko install @open-neko/plugin-parallel-search
```

## Under the hood

- **Self-hosted via Docker.** One binary, one `start` command. Data lives on your Postgres.
- **Bring your own LLM.** Hermes runs against Anthropic, OpenAI, Google, Ollama, and others; Claude Agent runs Anthropic in-process. Pick per task, swap any time.
- **Plugins in microVM sandboxes.** Outbound network is allowlisted per manifest, not blanket-open.
- **Apache 2.0.** Inspect everything. Fork it. Take your data with you.

## Docs

- [INSTALL.md](INSTALL.md) — install, [upgrade](INSTALL.md#upgrade), requirements, troubleshooting, connecting your data, full demo trial
- [ARCHITECTURE.md](ARCHITECTURE.md) — services, databases, agent runtime, operating-loop wiring (diagrams)
- [PLUGINS.md](PLUGINS.md) — plugin capabilities, sandbox/security model, marketplaces, install policy, host support
- [CONTRIBUTING.md](CONTRIBUTING.md) — dev setup, repo layout, pre-PR checks
- [CHANGELOG.md](CHANGELOG.md) — releases

## Issues

Please file bugs and feature requests at [github.com/open-neko/openneko/issues](https://github.com/open-neko/openneko/issues).

## Contributing

Pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the developer setup, repository layout, and the checks to run before opening a PR.

## License

OpenNeko is licensed under the [Apache License 2.0](LICENSE).

## Author

Created by [Amit Deshmukh](https://getneko.app/#about).
