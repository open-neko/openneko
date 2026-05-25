# OpenNeko

[![License](https://img.shields.io/github/license/open-neko/neko)](LICENSE)
[![Release](https://img.shields.io/github/v/release/open-neko/neko)](https://github.com/open-neko/neko/releases/latest)
[![Self-hosted · Docker](https://img.shields.io/badge/self--hosted-Docker-2496ED?logo=docker&logoColor=white)](INSTALL.md)
[![getneko.app](https://img.shields.io/badge/getneko.app-website-111111)](https://getneko.app)
[![Stars](https://img.shields.io/github/stars/open-neko/neko?style=social)](https://github.com/open-neko/neko/stargazers)

**OpenNeko learns your business from its own data and tells you where you're winning, where you're leaking, and what to do about it.** Self-hosted *executive intelligence for CXOs*: it reads your CRM, billing, and ops data through GraphJin, surfaces what matters on a daily Briefing, answers whatever you ask in plain English, and acts on rules you write the same way — *"if Germany revenue drops below its baseline, alert #revenue-alerts."* You stay in command.

*Not a dashboard. Not a CRM. Not an autonomous agent — dashboards make you look; OpenNeko brings the findings to you.*

![OpenNeko on mobile — Briefing, Ask, Workflows](cfo-briefing.png)

## Quickstart

You'll need Docker and one LLM provider API key.

```bash
# macOS
brew install open-neko/tap/openneko
mkdir -p ~/openneko && cd ~/openneko
openneko start --mode demo --detach
```

Linux: download the binary from the [latest release](https://github.com/open-neko/neko/releases/latest), then run the same `openneko start --mode demo --detach`.

Open [http://localhost:3000](http://localhost:3000) and finish the setup wizard. The demo seeds three workflows against sample data — within ~15 minutes a *"Germany revenue dropped"* finding lands on your Briefing with a proposed Slack alert waiting for your approval.

Full trial flow (live order simulator + scenario injector) and connecting your own data: see **[INSTALL.md](INSTALL.md)**.

## What it does

OpenNeko plugs into the systems where your business actually runs — your CRM, billing, ops databases — and sets watchers loose to sweat the small stuff. Findings land on the Briefing; when something needs to happen, OpenNeko drafts the action and waits for your call.

- **Briefing** — what's awaiting you, what's worth a look, what was quiet, what you've pinned. One surface, always current.
- **Ask** — chat against your business data when a finding raises a question. Pull the thread without leaving the workspace.
- **Workflows in chat** — describe a watcher in plain English. OpenNeko schedules it, runs it, and writes up what it found. Watchers can subscribe to each other's findings, so one workflow's output triggers the next.
- **Rules** — decide what auto-fires, what queues for review, what's never allowed. Authored in chat, edited in the UI.
- **Approval queue** — every action that needs your call lands here. Approve, reject, or let your rules decide. Receipts of what fired land back on the Briefing.

*Self-hosted via Docker. Bring your own LLM provider — Hermes runs against Anthropic / OpenAI / Google and others; Claude Agent runs Anthropic in-process.*

## Plugins

Extend OpenNeko with sandboxed plugins that add new action kinds — web search, Slack, Gmail, Shopify, Sheets, and more. Each runs in an isolated microVM with outbound network limited to what its manifest declares. Browse the marketplace at [open-neko.github.io/plugins](https://open-neko.github.io/plugins/), and see **[PLUGINS.md](PLUGINS.md)** for capabilities, the secrets/sandbox model, install policy, and host support.

```bash
openneko install @open-neko/plugin-parallel-search
```

## Docs

- [INSTALL.md](INSTALL.md) — install, [upgrade](INSTALL.md#upgrade), requirements, troubleshooting, connecting your data, full demo trial
- [ARCHITECTURE.md](ARCHITECTURE.md) — services, databases, agent runtime, operating-loop wiring (diagrams)
- [PLUGINS.md](PLUGINS.md) — plugin capabilities, sandbox/security model, marketplaces, install policy, host support
- [CONTRIBUTING.md](CONTRIBUTING.md) — dev setup, repo layout, pre-PR checks
- [CHANGELOG.md](CHANGELOG.md) — releases

## Issues

Please file bugs and feature requests at [github.com/open-neko/neko/issues](https://github.com/open-neko/neko/issues).

## Contributing

Pull requests are welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for the developer setup, repository layout, and the checks to run before opening a PR.

## License

OpenNeko is licensed under the [Apache License 2.0](LICENSE).

## Author

Created by [Amit Deshmukh](https://getneko.app/#about).
