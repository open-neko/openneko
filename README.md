# OpenNeko

[![License](https://img.shields.io/github/license/open-neko/neko)](LICENSE)
[![Release](https://img.shields.io/github/v/release/open-neko/neko)](https://github.com/open-neko/neko/releases/latest)
[![Self-hosted · Docker](https://img.shields.io/badge/self--hosted-Docker-2496ED?logo=docker&logoColor=white)](INSTALL.md)
[![getneko.app](https://img.shields.io/badge/getneko.app-website-111111)](https://getneko.app)
[![Stars](https://img.shields.io/github/stars/open-neko/neko?style=social)](https://github.com/open-neko/neko/stargazers)

**OpenNeko keeps an eye on your business data and points out things worth a look — then drafts what to do, for you to approve.** It's a self-hosted tool that connects to your CRM, billing, and ops databases through GraphJin. You describe checks in plain English; it runs them on a schedule and posts what it finds to a daily Briefing. You can ask follow-up questions about your data, and write simple rules like *"if Germany revenue drops below its baseline, alert #revenue-alerts"* — OpenNeko drafts the action and waits for your go-ahead.

It's not a dashboard, a CRM, or an autonomous agent. The idea is to bring findings to you, instead of you going to look for them.

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

OpenNeko connects to the systems your business runs on — CRM, billing, ops databases — and runs background checks (watchers) against them. Findings show up on the Briefing; when something needs action, OpenNeko drafts it and waits for you to approve.

- **Briefing** — what's waiting on you, what's worth a look, what was quiet, and anything you've pinned. Kept current.
- **Ask** — ask follow-up questions about your business data without leaving the app.
- **Workflows in chat** — describe a watcher in plain English; OpenNeko schedules it, runs it, and writes up what it found. Watchers can subscribe to each other, so one workflow's output can trigger the next.
- **Rules** — decide what runs automatically, what queues for review, and what's never allowed. Written in chat, edited in the UI.
- **Approval queue** — actions that need your sign-off land here. Approve, reject, or let your rules decide; a record of what ran goes back to the Briefing.

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
