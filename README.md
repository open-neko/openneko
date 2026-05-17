# OpenNeko

An always-on operating loop for CXOs and operators.

*Your business watches itself, surfaces findings on the Briefing, and proposes the next move — you approve or reject.*

![OpenNeko on mobile — Briefing, Ask, Workflows](cfo-briefing.png)

OpenNeko plugs into the systems where your business actually runs — your CRM, billing, ops databases — and sets watchers loose to sweat the small stuff for you. Findings land on the Briefing. When something needs to happen, OpenNeko drafts the action and waits for your **approve** or **reject**. Nothing fires on its own.

Not a dashboard. Not a CRM. Not an autonomous agent. The operating loop is its own thing — *dashboards make you look; the operating loop brings the findings to you.*

## Features

- **Briefing** — what's awaiting you, what's worth a look, what was quiet, what you've pinned. One surface, always current.
- **Workflows in chat** — describe a watcher in plain English. OpenNeko schedules it, runs it, and writes up what it found. Watchers can subscribe to each other's findings, so one workflow's output becomes the next's trigger.
- **Approval queue** — every action that needs your call lands here. Approve, reject, or let your rules decide. The receipts of what fired (with you or without you) land on the Briefing.
- **Rules** — decide what auto-fires, what queues for review, what's never allowed. Authored in chat, edited in the UI.
- **Ask** — chat against your business data when a finding raises a question. Pull the thread without leaving the workspace.

*Self-hosted via Docker. Bring your own LLM provider — Hermes runs against Anthropic / OpenAI / Google and others; Claude Agent runs Anthropic in-process.*

## Plugins

OpenNeko can be extended with sandboxed plugins that add new action kinds (e.g. web search via Parallel.ai). Every plugin runs inside a microsandbox microVM with outbound network limited to the hosts the plugin's manifest declared at install time — neither the plugin nor the agent can reach anything else.

Install from the official marketplace:

```bash
npm install -g @open-neko/cli
openneko init
openneko install @open-neko/plugin-parallel-search
```

Browse it at [open-neko.github.io/plugins](https://open-neko.github.io/plugins/). Run `openneko doctor` to check that your host can run microsandbox.

### Federated marketplaces

The official marketplace ships only first-party `@open-neko/*` plugins that the OpenNeko team writes and supports. Anyone else can publish their own `marketplace.json` at any stable URL and operators trust it explicitly:

```bash
openneko marketplace add https://example.com/marketplace.json
openneko install @example/openneko-plugin-foo
```

OpenNeko makes no representation about non-official marketplaces — that trust is between the operator and the publisher. The sandbox enforces capability declarations regardless of where a plugin came from. See [open-neko/plugins/CONTRIBUTING.md](https://github.com/open-neko/plugins/blob/main/CONTRIBUTING.md) for the marketplace publish guide.

### Bypass every marketplace (`--unverified`)

To install a plugin directly from npm without going through any marketplace (plugin authoring, or an emergency hotfix before a marketplace entry exists):

```bash
openneko install <npm-package-name> --unverified
```

The CLI prints a loud warning. The integrity hash is taken on trust from npm rather than verified against a marketplace listing; everything else (sandboxing, manifest capability enforcement) still applies.

### Host support

| Host | Plugin system |
|---|---|
| macOS arm64 (Apple Silicon) | ✓ supported |
| Linux x86_64 with `/dev/kvm` | ✓ supported |
| Linux arm64 with `/dev/kvm` | ✓ supported |
| macOS x86_64 (Intel) | ✗ not supported (microsandbox ships arm64 only on macOS) |
| Linux without KVM | ✗ not supported |
| Windows | ✗ WSL2 viability is being evaluated |

On unsupported hosts the plugin subsystem is disabled with a clear log line; OpenNeko itself still runs. The built-in `send_webhook` action adapter (no sandbox needed) remains as the extensibility escape hatch.

## Try it in 10 minutes

What you're about to do: clone the repo, start it, finish a setup wizard, and watch a *"Germany revenue dropped"* alert land on the Briefing within ~15 minutes.

You'll need Docker and one LLM provider API key. Spin up OpenNeko with the included AdventureWorks sample-data services:

```bash
git clone https://github.com/open-neko/neko.git
cd neko
docker compose -f compose.yml -f compose.adventureworks.yml up -d --build
docker compose -f compose.yml -f compose.adventureworks.yml run --rm neko-adventureworks-seed
```

Open [http://localhost:3000](http://localhost:3000) and finish the setup wizard.

For full install steps, requirements, updates, troubleshooting, and connecting your own data, see [INSTALL.md](INSTALL.md).

### Watch the loop fire end-to-end

The seed pre-loads three workflows on the AdventureWorks data so the trial isn't a blank page:

- **Daily Revenue Health Check** (9am cron) — yesterday's revenue vs the trailing 7-day average; lands on the Briefing tagged good / watch / act.
- **Revenue Drop Alert** (hourly cron) — per-territory current-hour revenue vs the same hour-of-week baseline averaged over the prior 4 weeks; if any territory falls below 50%, proposes a Slack alert to `#revenue-alerts` for your approval.
- **Slow-Ship Operations** (8:30am cron) — orders stuck in *pending* for more than 5 days, with the oldest 3 order IDs.

To see the loop without waiting for an organic dip, fire the Germany revenue-drop scenario. It tells the order trickle to stop generating new orders for territory 8 (Germany) for three hours:

```bash
docker compose -f compose.yml -f compose.adventureworks.yml \
  exec adventureworks-scenario-injector \
  /scripts/scenario-injector.sh fire germany-revenue-drop
```

Wait ~15 minutes for the trickle to skip a couple of Germany ticks, then click **+ Run now** on **Revenue Drop Alert** in `/workflows`. Within seconds a finding lands on the Briefing — Germany's hourly revenue well below its baseline — and a proposed Slack alert sits in the approvals queue for your approve / reject. Click approve; the receipt drops onto the Briefing under **Fired on your behalf** — the loop closing in front of you. (The trial defaults `NEKO_ACTIONS_DRY_RUN=true`, so external actions go to a mock adapter until you wire real webhooks.)

That's the loop: watcher runs → finding lands → action proposed → you approve → receipt on the Briefing. Once it clicks, write your own watcher in chat from `/work`, and when you're ready, swap AdventureWorks for your real data source — see [INSTALL.md](INSTALL.md) for connecting GraphJin to your CRM, billing, or warehouse.

## Docs

- **Getting started**
  - [INSTALL.md](INSTALL.md) — install, update, requirements, troubleshooting, connecting your data
- **How it works**
  - [ARCHITECTURE.md](ARCHITECTURE.md) — services, databases, agent runtime, operating-loop wiring (diagrams)
- **Project**
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
