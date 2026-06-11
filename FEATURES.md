# OpenNeko Features

OpenNeko is an AI teammate for your business data: ask questions in plain
language, get trustworthy answers, and let it watch things for you — safely,
with every action approved and audited. This page describes what shipped in
the 2026 roadmap release wave, in plain language.

---

## Ask anything, get real answers

Talk to your company data the way you'd talk to an analyst. The assistant
finds the right tables on its own, double-checks its work, and presents
results as readable cards and charts — not raw data dumps.

| Feature | What it does for you |
|---|---|
| Self-correcting answers | When a query comes back wrong or empty, the assistant notices, fixes its approach, and retries — instead of handing you a broken result. |
| Smart discovery | The assistant learns the paths through your data that answer common questions for your role, so repeat questions get faster and more reliable. |
| Rich answer cards | Answers arrive as tables, charts, and summary cards designed for the screen you're on, not walls of text. |
| On-demand data knowledge | Instead of being pre-loaded with a stale snapshot of your schema, the assistant looks up exactly what it needs, when it needs it — so answers reflect your data as it is today. |
| Hours-saved tracking | Every answer and action estimates the specialist time it saved you, and a live dashboard adds it up — so you can see the value in hours, not vibes. |

## Connect your data, conversationally

Hooking up databases and shaping who can see what no longer requires editing
config files. An admin does it from chat; the assistant proposes, the admin
approves.

| Feature | What it does for you |
|---|---|
| Chat-first data sources | Register, enable, disable, or switch your data connections by asking — the assistant proposes the change as a card, and an admin approves it. |
| Chat-first source configuration | Admins can add data sources, define roles, and set read/write access on the data engine itself from a conversation — no YAML, no redeploy. |
| Credentials stay out of chat | Passwords and keys are never typed into the conversation. They're entered once in a secure form, referred to by name afterwards, and stored sealed — the assistant never sees the value. |
| Personal access passes | Every request to your data carries a short-lived pass identifying who's asking and what they're allowed to see — access is per-person, not one shared key. |
| Multi-tenant isolation | In shared deployments, every query is automatically fenced to your organisation's rows — no way to read a neighbour's data. |

## It works while you're away

OpenNeko isn't just question-and-answer. It runs an operating loop: watching
metrics, surfacing findings, and proposing actions — with brakes so it never
runs away from you.

| Feature | What it does for you |
|---|---|
| Morning briefing | Important observations are elevated into briefing cards — a short, prioritised read on what changed and what needs you. |
| At-a-glance stats | A four-number strip (runs, findings, approvals waiting, budget used) tells you the state of your assistant in two seconds. |
| Watchers | Set a condition in plain language — "tell me if daily orders drop below 500" — and the assistant monitors it and alerts you when it trips. |
| Outside-world triggers | External systems can ping OpenNeko to kick off a workflow — so it reacts to events, not just schedules. |
| Code actions | The assistant can file issues and draft code patches as proposals — a human always reviews and applies them. |
| Mute and pause | Tired of a noisy topic? Mute it, or pause the assistant for the day with one tap — it resumes on its own tomorrow. |
| Safety brakes | Daily run budgets and fan-out caps mean a misbehaving workflow runs out of rope quickly instead of flooding you. |
| "Seen 3× today" dedupe | Repeat findings collapse into one card with a counter, instead of stacking up as noise. |

## Meet your team where it works

OpenNeko lives in your chat tools, knows who's talking, and routes messages to
the right workspace.

| Feature | What it does for you |
|---|---|
| Slack, WhatsApp & Telegram | Talk to your assistant from the tools your team already uses — replies are formatted natively for each app. |
| It knows who's asking | Every inbound message is tied to the actual sender, so permissions and personalisation follow the person, not the channel. |
| Workspace routing | Messages from a connected Slack workspace or phone number land in the right organisation automatically. |
| Identity linking | Chat accounts link to OpenNeko accounts — automatically by matching email, or with an admin's approval. Unlinked strangers get safe, read-only treatment. |
| Channel administration from chat | Admins can list workspaces, see who's linked, and approve or revoke links — all conversationally. |

## Built for teams

Every run knows who started it and what they're allowed to do. Admin work —
people, plugins, channels — happens in chat with explicit approvals.

| Feature | What it does for you |
|---|---|
| Real roles | Admins, members, and automated services each have distinct permissions, enforced everywhere — not just labels. |
| Every action has an owner | Whether something was started by a person in chat, a schedule, or a channel message, the record shows who and in what capacity. |
| Manage people from chat | Invite teammates, change roles, deactivate accounts — proposed by the assistant, approved by an admin. |
| Manage plugins from chat | Browse, install, and remove integrations conversationally, with each plugin's permissions shown on the card before you approve. |
| Personas | Tell OpenNeko what you do in your own words, and it tailors its briefings, language, and priorities to your role. |

## Yours and your team's knowledge

The assistant's memory, workflows, and settings are versioned like a careful
editor's drafts — personal by default, shareable on purpose, and always
restorable.

| Feature | What it does for you |
|---|---|
| Invisible versioning | Every change to skills, workflows, and memory is snapshotted automatically — nothing is ever lost to an edit. |
| Personal workflows | Your saved workflows are yours; the team's are the team's. Same name, no collisions. |
| Personal memory layer | Correct or hide a team memory just for yourself without changing it for anyone else — and pull in team updates when you want them. |
| Save, history, restore | Browse what changed and roll back to any earlier state of your assistant's configuration. |
| Promote and adopt | When a personal workflow or memory proves valuable, an admin can promote it to the whole team with full lineage of where it came from. |

## Security you can verify

The assistant is treated as untrusted by design: it runs in a locked-down
sandbox, never holds your keys, and leaves a tamper-evident trail you can
hand to an auditor.

| Feature | What it does for you |
|---|---|
| Always sandboxed | The AI agent and every plugin run inside isolation boxes with networking denied by default — this is the only mode, not an option you might forget to turn on. |
| Your keys never enter the box | API keys are injected on the wire by a gateway outside the sandbox; the agent only ever holds a placeholder. Verifiably — you can grep the box and find nothing. |
| Secrets encrypted at rest | Tokens, passwords, and keys are encrypted on disk everywhere they're stored. |
| Bring your own vault | Plug in an external secrets manager (Infisical) instead of local files, with no change to how anything else works. |
| Approval gates | Anything that changes the world — sending, installing, configuring, inviting — becomes a proposal a human approves first, governed by per-organisation policies. |
| Dual-identity audit | Every privileged call records both the human behind it and the agent acting for them — no anonymous actions. |
| Tamper-evident log | The action trail is hash-chained: any edit or deletion after the fact is mathematically detectable. Exportable for auditors and SIEM tools. |
| Memory integrity | Stored memories are sealed against tampering and expire on schedule — a poisoned or stale note can't quietly steer the assistant. |
| Behavioral alarms | Unusual volumes — too many actions per hour, too many memory writes — raise alerts automatically. |
| Security profiles | One dial — solo, team, org, or hardened — scales approval strictness and alarm sensitivity to your deployment. |
| Audit in plain language | Ask "what did the assistant do yesterday and who approved it?" and get a readable timeline. |
