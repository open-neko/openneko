# OpenNeko Features

OpenNeko is an AI teammate for your business data: ask questions in plain
language, get trustworthy answers, and let it watch things for you — safely,
with every action approved and audited. This page describes everything that
has shipped, in plain language, organized by what it does for you. Each
feature links the pull request(s) that shipped it; the [complete PR
index](#complete-pr-index) at the bottom covers every PR in one table.

---

## Your own bots

The web app opens on chat. Each person makes their own bots and talks to them
the way they would text a colleague. Admins reach the full console from the
profile menu.

| Feature | What it does for you | Shipped in |
|---|---|---|
| Bots you make yourself | Pick a name, a coat and a pattern on the New bot screen. Each bot gets a neko avatar and can use everything you can access. | #449 |
| One running chat per bot | A bot keeps one conversation. Desktop shows your bots in a sidebar beside the open chat. Long chats open at the latest message and load older turns as you scroll up. | #449 |
| Instructions the bot keeps current | The bot writes its own instructions from what you tell it and tunes them as you talk. | #449 |
| Routines in the chat | Tell a bot when to act: on a schedule, on an event, or when a value changes. Results post to its chat. A watcher stays quiet while the value stays the same. | #449 |
| Skills found by meaning | The bot finds the right skill among thousands by what the skill does, and you can edit a skill from the chat. | #449 |
| Connectors | The profile menu lists the connectors you can use and the ones coming next. | #449 |
| Existing chats carry over | On upgrade, each earlier chat thread becomes a bot with a random neko, so your history stays in the list. | #449 |

## Ask anything, get real answers

Talk to your company data the way you'd talk to an analyst. The assistant
finds the right tables on its own, double-checks its work, and presents
results as readable cards and charts — not raw data dumps.

| Feature | What it does for you | Shipped in |
|---|---|---|
| The Ask workspace | A persistent chat surface with threads, run history, and shareable per-thread URLs — your conversations with your data are first-class records, not ephemeral sessions. | #2, #4 |
| Live streaming runs | Watch the assistant work in real time — messages and tool activity stream in as they happen, and cancel takes effect instantly. The first answer text reaches the screen at most 100 ms after the model writes it; later text follows every 300 ms. When a run ends, OpenNeko stores each piece of streamed text as one row, and a tab that reconnects gets only the text it does not have. Text that could be the start of a stored secret waits until the rest arrives, so a secret split across two updates is still redacted. A table, code block or card appears only when it is complete; a small placeholder holds its place until then. When the model starts its answer again, the new text replaces the old text on the screen. A tab that was open before an upgrade keeps the first attempt until the page reloads. | #3, #5 |
| Self-correcting answers | When a query comes back wrong or empty, the assistant notices, fixes its approach, and retries — instead of handing you a broken result. | #96 |
| Smart discovery | The assistant learns the paths through your data that answer common questions for your role, so repeat questions get faster and more reliable. | #96 |
| Rich answer cards | Answers arrive as tables, charts, and summary cards designed for the screen you're on, not walls of text — with headings scaled for conversation, not billboards. When the model writes more than one card set, the answer shows one card with a section for each set. When a card fails its check, OpenNeko asks the model to fix it and shows "Fixing a chart" meanwhile: at most two fixes per answer, within 30 seconds, counted in the run's spend cap. A fix may change only the fields that failed the check. When a card still fails, the answer shows "A chart could not be shown", and the run's telemetry counts the fixes and the cards not shown. | #4, #78, #129 |
| On-demand data knowledge | Instead of a stale schema snapshot, the assistant carries a compact live map of your data — hub tables, join paths, query patterns — and looks up details exactly when it needs them. Answers reflect your data as it is today, at the speed of the old pre-loaded packs. | #96, #129, #132 |
| Hours-saved tracking | Every answer and action estimates the specialist time it saved you. The answer footer and the action cards show it, and the Workflows page shows the hours each workflow saved in the last 30 days. | #88, #94 |
| A memory the agent keeps | Teach the assistant a fact once and it recalls it when a question needs it. The agent saves and searches its own memory with Postgres full-text search. Connect an embedding service and it also recalls by meaning. | #17 |
| Editable business profile | The assistant's understanding of your company is right there to read — click into it and edit it like a document. | #10 |
| One runtime, any model provider | Ax is the agent runtime for every organisation. It keeps the full administrative toolset, works with Anthropic, Google, OpenAI-compatible and other providers, and prices each run from the model price list. | #3, #112, #436, #449 |
| One gateway for every model call | Agent turns and the app's own short calls, such as bot names, bot instructions and conversation compaction, go through one Go gateway in `openneko serve`. It works with every Ax provider and any OpenAI-compatible URL, and the key stays with the call. The provider test in Admin, Settings now works for Ollama. | Phase 2 |
| Run without Docker | `openneko start --native` runs OpenNeko on a Linux, macOS or Windows host as plain processes, with your Postgres database by URL. `install-native.sh` installs it on Linux and macOS, `install-native.ps1` on Windows, both with no admin rights. On Linux, `server-install` runs serve and web as two system users. A supervisor starts `openneko serve` and `openneko web` again when one stops. `status`, `logs`, `stop`, `doctor`, `backup`, `restore` and `upgrade --from <release folder>` act on that install. A model server on the same host answers on `127.0.0.1`. | Phase 3 |
| The answer starts sooner | The model writes the answer at a low reasoning effort by default; the agent's own steps keep the model's effort. In the backend eval on Gemini 3.8 Flash, the answer call took 5.8 s instead of about 21 s (median, two runs of 63 cases at each level), and the pass count stayed within run-to-run noise. In 30 runs with cards on, every chart or table question still got its chart and table. An admin can raise it at Admin, Settings, Agent, Answer effort. | Phase 3 |
| A model for each task | Admins add more models and pick one for each task: agent turns, the question classifier, conversation compaction and four more. A task can name a fallback model, which answers when the first provider is down or overloaded. Each model can carry its own price. Admin, Settings, Spend shows the last 30 days of spend by task and model. | Phase 2 |
| Information-dense Compact mode | A density toggle switches the whole app between a comfortable reading layout and an information-dense operator view. | #88 |

## Connect your data, conversationally

Hooking up databases and shaping who can see what no longer requires editing
config files. An admin does it from chat; the assistant proposes, the admin
approves.

| Feature | What it does for you | Shipped in |
|---|---|---|
| Chat-first data sources | Register, enable, disable, or switch your data connections by asking — the assistant proposes the change as a card, and an admin approves it. | #96 |
| Chat-first source configuration | Admins can add data sources, define roles, and set read/write access on the data engine itself from a conversation — no YAML, no redeploy. The assistant reads the engine's current config with every secret hidden and explains it from a reference for the pinned GraphJin version. To connect a PostgreSQL, MySQL or MariaDB database, the admin answers plain questions in a card: where the database runs, which read-only login, which tables no one may read and which only admins may read. The admin types the password into the card, which stores it encrypted; the model, the logs, the traces and the thread history never hold it. A login with no password is refused for now. Before the admin files the source, "Check connection" in the card tests it step by step: the host, the port, TLS, the login, its rights to read, and whether it is read-only. A failed step says what is wrong and gives the SQL for the database administrator, for example how to create a read-only login. A passed check lists the tables the login can read, all selected; the tables the admin clears become the tables no one may read. `openneko source check` runs the same check from a terminal. Code writes the config file from the approved answers, roles included, so a restart keeps every approved change. Before an admin sees "approve", OpenNeko checks the answers, runs GraphJin's preview, and loads the resulting config file with the pinned GraphJin loader. A problem comes back in plain words beside the answer at fault, for example "The login name is empty." or "The database refused the login with this password." OpenNeko also says what the config does in plain sentences: each source, what it exposes, who may read or change it, and which tables are hidden. Admin settings shows these sentences for the current config. Admin settings also shows the config file itself, read-only, with secrets and comments hidden, its path on the server and whether GraphJin is running. To change the file by hand, an admin edits it on the server and runs `openneko graphjin check`. The approval card shows them for the touched sources and roles, now and after approval, and keeps the YAML diff one click away. When the system has a pack with a ready config (Magento, Shopify, Google Workspace, Slack, Resend, Parallel web search, or an uploaded pack), the assistant shows that pack's setup card in chat. The admin enters only the address and the login, reviews the plan and approves it in the card, the same install as Admin settings, Packs. Only admins get these tools and the guide. The tools are on for new installs; an upgrade keeps the setting it had. | #101 |
| Credentials stay out of chat | Passwords and keys are never typed into the conversation. They're entered once in a secure form, referred to by name afterwards, and stored sealed — the assistant never sees the value. | #101 |
| Personal access passes | Every request to your data carries a short-lived pass identifying who's asking and what they're allowed to see — access is per-person, not one shared key. | #96 |
| Multi-tenant isolation | In shared deployments, every query is automatically fenced to your organisation's rows — no way to read a neighbour's data. | #96 |

## It works while you're away

OpenNeko isn't just question-and-answer. It runs an operating loop —
observe, understand, decide, act — watching metrics, surfacing findings,
and proposing actions, with brakes so it never runs away from you.

| Feature | What it does for you | Shipped in |
|---|---|---|
| The operating loop | Workflows observe your data on schedules, surface findings as observations, and propose actions through an approval stack — the full loop from data to decision. | #11 |
| Build workflows by chatting | Create, update, and look up workflows and approval rules in the same chat where you ask questions. Only an admin saves an approval rule, and a pack's rule changes only in Settings. Every artifact links back to the conversation that produced it. | #46 |
| Retire workflows by @mention | Delete a noisy or obsolete workflow mid-conversation by @mentioning it — composer autocomplete included. | #110 |
| Row-level data triggers | Workflows fire the moment a row changes — stock dips below its reorder point, an account flips to at-risk — not just on a schedule. Wired in plain language when you save the workflow. | #50, #52, #63 |
| Watchers | Set a condition in plain language — "tell me if daily orders drop below 500" — and the assistant monitors it and alerts you when it trips. | #96 |
| Outside-world triggers | External systems can ping OpenNeko to kick off a workflow — so it reacts to events, not just schedules. | #96 |
| Action receipts | When a rule auto-approves an action, the receipt doesn't vanish — every fired action has a deep-dive page showing what ran, why, and on whose authority. | #15 |
| Code actions | The assistant can file issues and draft code patches as proposals — a human always reviews and applies them. | #96 |
| Pause for today | Pause a noisy workflow for the rest of the day with one tap. Its watchers stop their queries while it is paused. It resumes on its own at midnight UTC. Turning the workflow off or uninstalling its pack ends the pause, so it stays off. | #96 |
| Safety brakes | Daily run budgets and fan-out caps mean a misbehaving workflow runs out of rope quickly instead of flooding you. | #11 |
| Stop a workflow run | An admin stops a scheduled, triggered, watcher or API run from its run page. The run ends as cancelled and is not tried again, and the audit chain names the admin. A batch API run that is already running ends at its runtime limit. | Phase 3 |
| "Seen 3× today" dedupe | Repeat findings collapse into one card with a counter, instead of stacking up as noise. | #96 |

## Meet your team where it works

OpenNeko lives in your chat tools, knows who's talking, and routes messages to
the right workspace.

| Feature | What it does for you | Shipped in |
|---|---|---|
| Slack & Telegram | Talk to your assistant from the tools your team already uses — replies are formatted natively for each app. | #68, #96 |
| Built-in channels | Slack, Telegram and the HUD are built into the core behind one modality-free interface. A channel turns on when an admin saves its credentials in settings; no pack is needed. | #68, #70 |
| Zero-config inbound | When a channel turns on, it starts listening: inbound polling starts and the first contact binds to your workspace. No env flags. | #72 |
| It knows who's asking | Every inbound message is tied to the actual sender, so permissions and personalisation follow the person, not the channel. | #96 |
| Workspace routing | Messages from a connected Slack workspace or phone number land in the right organisation automatically. | #96 |
| Identity linking | Chat accounts link to OpenNeko accounts — automatically by matching email, or with an admin's approval. Unlinked strangers get safe, read-only treatment. | #96 |
| Channel administration from chat | Admins can list workspaces, see who's linked, and approve or revoke links — all conversationally. | #96 |
| Per-channel rendering | Answers are composed for the surface they'll appear on — cards on the web, native formatting in chat apps — from one channel-neutral brain. | #88 |

## Built for teams

Every run knows who started it and what they're allowed to do. Admin work —
people, channels, settings — happens in chat with explicit approvals.

| Feature | What it does for you | Shipped in |
|---|---|---|
| Built-in enterprise SSO | Connect Scalekit (Okta, Entra ID, Google Workspace and more behind it) or turn on email-link sign-in from settings, and "Sign in with…" lights up — no install step. | #22, #24 |
| Real roles | Admins, members, and automated services each have distinct permissions, enforced everywhere — not just labels. | #96 |
| Every action has an owner | Whether something was started by a person in chat, a schedule, or a channel message, the record shows who and in what capacity. | #96 |
| Manage people from chat | Invite teammates, change roles, deactivate accounts — proposed by the assistant, approved by an admin. | #96 |
| Personas | Tell OpenNeko what you do in your own words — free text, not a dropdown — and it tailors its answers, language, and priorities to your role, from onboarding onwards. | #96, #120 |

## Yours and your team's knowledge

The assistant's memory, workflows and skills are personal by default and
shareable on purpose. OpenNeko records each change; it keeps no old versions.

| Feature | What it does for you | Shipped in |
|---|---|---|
| Change record | Each change to a skill, a workflow or a team memory adds a `config_change` row: who made it, what changed, when, and a short summary. OpenNeko keeps no old versions and has no undo. To try a change safely, copy the skill and edit the copy. | #96 |
| Personal workflows | Your saved workflows are yours; the team's are the team's. Same name, no collisions. A workflow that the assistant saves in your conversation is yours and runs with your access. A workflow with no owner runs only when a pack installed it or an admin adopted it for the team; an admin assigns an owner to any other one at Admin, Settings, Workflow API. | #96 |
| Personal memory layer | Correct or hide a team memory just for yourself without changing it for anyone else — and pull in team updates when you want them. | #96 |
| Your own skills | Copy any skill you can see to a new name. The copy is yours: only you see it, and you edit or delete it as you like. The team skill stays as it is. An admin can share their own skill with the team. Only an admin edits or deletes a team skill. Your conversations, your workflows and the workflow runs you start load your own skills beside the team skills your groups hold. A scheduled run of a workflow with no owner, and a pack workflow, load team skills only. | Phase 1 |
| Skill edits that stick | When the assistant changes a skill during a conversation, OpenNeko saves the change when the turn ends. An admin's change goes to the team skill. A member's change to a team skill goes to a private copy named after the skill and the member, for example `report-ann`, and the team skill stays as it is. A change to your own skill is saved in place, and a new skill becomes your own. | #413 |
| Promote and adopt | When a personal workflow or memory proves valuable, an admin can promote it to the whole team with full lineage of where it came from. | #96 |
| Document library | Documents and spreadsheets you upload, or attach in a conversation and add to the library, go into your private folder. The assistant finds them with file search. Nobody else can see them. | #220 |
| Search inside your files | The assistant searches the text of your PDF, Word, PowerPoint, Excel and CSV files, reads a whole file, and runs SQL over spreadsheet sheets to sum, count and compare. Your files stay in your private folder: nobody else's assistant can read them. The folder is on the server by default; an admin can keep the files in an S3-compatible bucket instead. | Phase 1 |
| Excel workbooks | With a sandbox connected, the assistant creates and edits Excel workbooks with formulas, styles, dates and named inputs. It calculates every formula and checks the results for errors before it gives you the file. | Phase 1 |
| Word documents | With a sandbox connected, the assistant creates, reads and edits Word documents with headings, lists, tables, images and page numbers. It can make its edits as tracked changes with comments, so you accept or reject each one in Word. | Phase 1 |
| PowerPoint decks | With a sandbox connected, the assistant creates, reads and edits PowerPoint decks with bullets, tables, charts, pictures and speaker notes. Give it your company template and it builds the deck from your layouts, or fills the markers in a deck you already use. | Phase 1 |
| PDF files | With a sandbox connected, the assistant reads PDFs page by page and reads scanned pages with OCR. It creates PDF reports, with ₹, Greek, Cyrillic, Chinese, Japanese and Korean text, fills and flattens forms, merges and splits files, and rotates, deletes, stamps and numbers pages. It refuses to change a signed file until you agree, and it tells you that a box on a page does not redact the text under it. | Phase 1 |
| Skills as plain files | Each skill is a folder with a plain-Markdown `SKILL.md` that any editor reads. Memories and workflows live in OpenNeko's database, and `openneko backup` holds them. | #220 |

## Security you can verify

The assistant is treated as untrusted by design: it runs no code, its tools
never hold your keys, and it leaves a tamper-evident trail you can hand to an
auditor.

| Feature | What it does for you | Shipped in |
|---|---|---|
| The agent runs no code in OpenNeko | The agent reads and writes files in its own run folder and reaches data only through the OpenNeko broker. Its terminal, the script skills and workflow script steps run in an external sandbox service, and only when one is connected. An admin can turn them off for one organization with the Terminal setting at Admin, Settings, Agent. The sandbox gets no model key and no broker token. | #82, #96, #98 |
| The terminal reaches only the hosts you list | Sandbox commands reach no host by default. An admin lists the hosts they may reach at Admin, Settings, Agent, and the egress proxy lets each run reach only those hosts. Each change of the list goes in the audit chain, and the proxy logs each connection with the run and the organization. | 6.0.0 |
| The agent never sees your keys | Model keys stay in `openneko web`. `openneko serve` gets a token for one call, and web's key door puts the key on the call to the provider ([AGENT_RUNTIME.md](AGENT_RUNTIME.md#the-key-door)). Serve removes every `*_API_KEY` variable, cloud credential, database URL and `NEKO_SECRET_KEY` from its own environment. | #82 |
| Serve cannot reach web's secrets (Linux) | `openneko serve` confines itself with Landlock at start: it cannot read the config folder, `secrets.json` or the web admin token, cannot write outside its folders and cannot run a file it wrote ([AGENT_RUNTIME.md](AGENT_RUNTIME.md#serve-confines-itself-linux)). | 6.0.0 |
| An answer cannot send data out through the browser | The web app loads images, fonts, styles, frames and data only from your install (and map tiles from OpenFreeMap). An image from another host in an answer shows as a link with its host, so opening an answer fetches nothing that the text names. A link to another host shows its real host and sends no referrer. | 6.0.0 |
| Secrets encrypted at rest | OpenNeko encrypts the tokens, passwords and keys it stores with AES-256-GCM under the install's secret key: the secrets file, the database login in `config.json`, and the settings and source secrets in the database (`apps/openneko/internal/config/crypt.go`). The GraphJin config file holds only `gjsecret://` references to GraphJin's keystore. The secret key itself is a plain file, mode 0600. | #96 |
| Approval gates | Anything that changes the world — sending, installing, configuring, inviting — becomes a proposal a human approves first, governed by per-organisation policies. | #11, #96 |
| Passkey approvals | Register a passkey on your phone or computer on your profile. From then on, each approval you give in the browser asks for that passkey, and removing your last passkey asks for it too. Approvals from Slack, Telegram and a device token work as before. The audit log records how each approval came in: passkey, session, Slack, Telegram or token. | Phase 4 |
| Dual-identity audit | Every privileged call records both the human behind it and the agent acting for them — no anonymous actions. | #96 |
| Tamper-evident log | The action trail is hash-chained: any edit or deletion after the fact is mathematically detectable. Exportable for auditors and SIEM tools. | #96 |
| Memory integrity | Stored memories are sealed against tampering and expire on schedule — a poisoned or stale note can't quietly steer the assistant. | #96 |
| Behavioral alarms | Unusual volumes — too many actions per hour, too many memory writes — raise alerts automatically. | #96 |
| Security profiles | `OPENNEKO_PROFILE` (solo, team, org or hardened; default solo) scales the behavioral alarm thresholds: `org` halves them and `hardened` quarters them. It does not change approvals. With `hardened`, `openneko web` also logs a warning at start when the model host is a public provider (`apps/openneko/internal/webfront/cronsweep.go`, `deploymentprofile.go`). | #96 |
| Audit in plain language | Ask "what did the assistant do yesterday and who approved it?" and get a readable timeline. | #96 |

## Plug in anything

Integrations are solution packs: sources, actions, workflows and skills for an
application, installed from settings or one command and governed by the same
approval policies as everything else. See [PACKS.md](PACKS.md).

| Feature | What it does for you | Shipped in |
|---|---|---|
| Pack actions as agent tools | Installed packs surface their actions directly to the assistant, governed by the same approval policies as everything else. | #24, #66 |
| Per-person integrations | Each operator connects their own accounts (OAuth) on the /integrations page — tokens are stored per-person, never shared. | #42 |
| Device sign-in for the installed app | The app shows an eight-letter code. The person approves it on `/device` in a browser that is signed in through SSO, and the app gets a device token that works on every route a browser session reaches. Admin, Users, Devices lists every device; Revoke signs one out at once. A token also stops after 30 days without use, when its user is disabled, or when the app signs out. Only a SHA-256 digest of each token is stored, and approvals, revokes and sign-outs go into the audit chain. | Phase 2 |
| Mobile web app and Web Push | The web app installs to a phone home screen (`/manifest.webmanifest` and `/sw.js`), caches its shell for offline launch, and sends RFC 8291 + VAPID Web Push notifications when an approval waits, a chat run finishes, or a workflow needs attention. Notifications carry only a short fixed notice and a relative app path — never business numbers or run content. A user keeps at most 10 push subscriptions; a new one removes the oldest. Sign-out removes this browser's push subscription on the server. The push sender stops with an error when the stored VAPID key pair is not valid; it does not make a new pair. A question typed offline waits in this browser and sends once when the network returns. The browser stores a waiting question in clear text (local storage, under the user's own key) until the app sends it, for at most 7 days. When the session confirms a user, the app deletes the waiting questions of every other user of this browser. A question typed before the session loads waits in this tab only and goes to the first user that this tab confirms. Sign-out removes the waiting questions. When the browser cannot store them, the app keeps them in the page and says that a reload loses them. The app sends the questions of a thread in the order typed, from every thread, also while the user reads another one. The questions of a new chat go to the thread that the first one creates. While a run streams and the network is gone, the composer takes a question and queues it. A busy server (HTTP 408, 409, 425, 429 or 5xx) keeps the question to send again, after a delay that doubles each time up to 5 minutes; another refusal stays on the question with the server's reason until the user sends it again or removes it. The server makes one run, and one new thread, for each question, also when the app sends it again after a lost response. A thread runs one question at a time: while a run is queued or running, the server refuses a new one (HTTP 409, `run_in_flight`), and the app sends the question again after the Retry-After time. The app names its user in each new question and new thread; the server refuses a request for a user other than the signed-in one (HTTP 409, `user_mismatch`). | Phase 4 |
| One API contract | Every server route is under `/api/v1` and is listed in one OpenAPI file, `api/openapi.yaml`, with the access rule for each route. A test fails when a route, the file and the web app disagree. Older `/api/<path>` URLs still work, so sign-in callbacks, OAuth apps and webhooks that hold them keep working after an upgrade. | Phase 2 |
| Contract tests for every route | `packages/contract` sends at least one request to each of the operations in `api/openapi.yaml` and checks the status and the response body against the file. It also checks that a revoked device token gets 401 on every signed-in route, and that a member gets 403 on every admin route. Give it a server address and two device tokens, and it runs against any server that holds its seed data. `pnpm --filter @neko/contract local` runs it against a local server on a new database. | Phase 2 |
| Static web app and Go server | Every page renders in the static single-page app (`apps/web/dist`), and `openneko web` answers the `/api/v1` routes, serves `openneko pack` on its local admin socket, runs River background jobs, executes solution packs and pack OAuth, and runs Ax Harness agent turns in Go. On Linux, macOS and Windows, the native install puts `openneko` and the built web app on the host with no Docker and no Node.js. The Docker images hold no Node.js either. | Phase 3 |
| Community skills | Install a skill straight from a git URL once an admin allows it; the policy is enforced at the CLI and the server. | #42, #44 |

## Install in one command, operate calmly

A single binary brings the stack up, keeps it honest, and tells you the
truth when something is wrong — in language an operator can act on.

| Feature | What it does for you | Shipped in |
|---|---|---|
| The `openneko` binary | One Go binary supervises everything: start/stop/status/logs, migrations, seeding, packs, secrets, doctor. No Node toolchain required to operate. | #26 |
| One-command install | `openneko start` brings up the full stack, the agent runtime included, with migrations applied before anything that needs them. In prod mode two containers keep running, `web` and `openneko-serve`, after the one-shot start steps, and OpenNeko's database is a Postgres server you run, given by one URL. Start checks that database first and prints the SQL for any missing right. `openneko database move` moves an older install off its bundled database. Demo mode runs its own. | #82, #49, #54 |
| A demo with living data | The trial workspace ships with simulated sales trickling in and scripted scenarios firing, so its four workflows have something real to find. | #47, #118, #121 |
| Encrypted backups | `openneko backup` writes one file with the database and config, encrypted with AES-256-GCM. It works with the bundled database and with a database URL. `openneko restore` checks the whole file before it changes anything. With a database URL, it stops when the login owns objects that OpenNeko did not make. `upgrade` takes a backup first. | — |
| Optional services by URL | An embedding service, a sandbox service and a trace collector each connect by a URL and a key. Each one is off until you set it, and OpenNeko works without it: search falls back to full-text search, and the agent has no terminal. Every embedding call goes through one Go gateway in `openneko serve`. Each sandbox command carries the run's changed files there and back, so the sandbox can run on another host. The compose recipe shares one volume with the stack, `openneko-sandbox-socket`, which holds only the sandbox's socket (`recipes/sandbox/compose.yml`). | Phase 1 |
| External services on the status page | Inference, embedding, the sandbox and the file store follow one pattern: a URL, a key and a health check. Admin, Administration shows each one as On, Off or Failing. An Off service says what still works without it. | Phase 2 |
| Honest status | `openneko status` reports the stack as serving, degraded or down. A service that is starting or fails its health check, or a setup step that failed while the core stays up, reads as degraded, not down (`apps/openneko/internal/cli/status.go`). | #86 |
| Releases that gate themselves | Every release is installed and smoke-tested after it builds; a broken one is automatically demoted so installers always get the last good version. | #86, #105, #106, #107 |
| Deploys follow smoked releases | Production updates only when a release has passed its smoke — never on raw merges. Disk is garbage-collected on the way. | #116, #124, #125, #127 |
| Zero-click release cycle | Merge to main and the pipeline does the rest: version cut, binaries, images, smoke, deploy. | #16, #45, #92, #93 |
| Database password change | After an admin changes the database password, `openneko web` opens each new database connection with the new password from `config.json`. | #121, #125 |
| Survives flaky networks | Image builds retry package and file downloads. Channel polling waits longer after each failure, up to 60 seconds, and handles each update once (`apps/openneko/internal/webfront/channelloops.go`). | #80, #84 |
| Diagnosable agent failures | When an agent turn fails, the error gives the failure code and the last lines of the turn's diagnostics. A turn that runs out of time says so and names `OPENNEKO_AGENT_TURN_TIMEOUT_MS`, which sets the limit (default 9 minutes; 15 minutes for a workflow turn, `OPENNEKO_WORKFLOW_TURN_TIMEOUT_MS`) (`apps/openneko/internal/webfront/turnax.go`). | #129, #132 |

---

## Complete PR index

Every merged pull request and the top thing it shipped. Release-cut PRs
(`chore(main): release X.Y.Z`) are version bumps produced by the pipeline
and are omitted — each one corresponds to a release of the features listed
here. Some early features are gone: the plugin system, the OpenShell agent
runtime, the Hermes backend, WhatsApp, the Library's concepts and
distillation, Records, and the briefing and dashboard pages. A note in
brackets marks each row they touch.

| PR | Date | Top feature shipped |
|---|---|---|
| #2 | 2026-05-10 | The /work chat surface: per-org isolated agent runtime with threads, runs, and a durable job lifecycle |
| #3 | 2026-05-10 | Hermes backend rewritten on ACP — live streamed message and tool events (Hermes is removed) |
| #4 | 2026-05-10 | Former Claude backend parity (retired); run-activity rendering and per-thread URLs |
| #5 | 2026-05-12 | Chat runs in-process: instant cancel, working file downloads, Hermes card fixes |
| #6 | 2026-05-12 | Self-healing database pool after shutdown — no more dead-pool cascades |
| #7 | 2026-05-12 | Former Claude SDK PATH fix (backend retired) |
| #8 | 2026-05-12 | Editorial design refresh; briefing cards open a "Deep dive" Ask thread (the briefing pages are gone) |
| #9 | 2026-05-12 | Unblocked deploys: dead branch tripping the production typecheck removed |
| #10 | 2026-05-13 | Business profile and industry insights are inline-editable documents |
| #11 | 2026-05-14 | The OUDA operating loop: workflows, observations, subscriptions, action approval stack, operator UX |
| #15 | 2026-05-15 | Action receipts — auto-fired actions get a visible, auditable deep-dive page |
| #16 | 2026-05-15 | Zero-click releases: deduped PR checks + auto-merged release PRs |
| #17 | 2026-05-16 | Embedding-backed memory the agent saves and searches by meaning (pgvector) |
| #20 | 2026-05-17 | Sandboxed plugin system: per-plugin isolation, declared egress, hot-reload registry (the plugin system is removed) |
| #22 | 2026-05-19 | Enterprise SSO as a thin contract over the plugin runtime (Scalekit-ready) (plugins are removed; SSO is built in) |
| #24 | 2026-05-19 | SSO page gating end-to-end + plugin actions exposed as agent tools (plugins are removed; pack actions are agent tools) |
| #26 | 2026-05-20 | The `openneko` Go binary: stack supervisor and plugin manager in one (the plugin manager is removed) |
| #41 | 2026-05-21 | Hermes agent updated to v0.14.0 (Hermes is removed) |
| #42 | 2026-05-21 | Per-operator OAuth (/integrations), install-policy enforcement, git-URL plugin installs (plugins are removed; git-URL skill installs remain) |
| #44 | 2026-05-21 | Removed a broken admin gate that locked operators out of install policy |
| #45 | 2026-05-21 | Supervisor smoke moved post-release — main stays green and honest |
| #46 | 2026-05-22 | Workflows and approval rules are created by chatting, with full provenance |
| #47 | 2026-05-22 | Packaged demo gains living data: sales simulator + scenario injector |
| #49 | 2026-05-22 | One migrator: the Go binary migrates everywhere (Node migrator retired) |
| #50 | 2026-05-23 | IFTTT-style triggers: workflows fire on data-source row changes |
| #52 | 2026-05-23 | GraphJin 3.18.25: column-reference comparisons for row triggers |
| #54 | 2026-05-23 | Dedicated one-shot migrate service breaks a compose dependency deadlock |
| #63 | 2026-05-24 | Data triggers fold into workflow-save — same plain-language path on both backends |
| #65 | 2026-05-24 | Deploys upgrade installed plugins to marketplace-latest (plugins are removed) |
| #66 | 2026-05-24 | Plugin action examples survive marketplace installs (small models stay accurate) (plugins are removed) |
| #68 | 2026-05-25 | Channels V2: modality-free interaction layer + live bidirectional Telegram |
| #70 | 2026-05-25 | Channel plugins install through the CLI capability path (plugins are removed; channels are built in) |
| #72 | 2026-05-25 | Channels auto-enable inbound and auto-bind on first contact |
| #74 | 2026-05-25 | README redesigned as a scannable landing page |
| #75 | 2026-05-26 | INSTALL distilled to a three-command happy path |
| #78 | 2026-06-02 | Confirmation cards, new-thread Ask, workflow management + settings polish |
| #80 | 2026-06-02 | Inbound channel polling backs off and dedupes under failure |
| #82 | 2026-06-04 | OpenShell sandboxed agent runtime — model keys never enter the box — one-command install (OpenShell is removed; `openneko serve` runs agent turns) |
| #84 | 2026-06-04 | Image builds tolerate registry flakiness (embedding prewarm retries) (the embedding container is removed) |
| #86 | 2026-06-04 | Failures made legible to non-technical operators; post-release smoke becomes a gate |
| #88 | 2026-06-04 | Compact information-dense UI, hours-saved tracking, per-channel rendering, Ask context rail |
| #90 | 2026-06-05 | Responsive/styling consistency sweep + workflow-run reliability |
| #92 | 2026-06-05 | Release auto-merge survives apostrophes in release notes |
| #93 | 2026-06-05 | All GitHub Actions on Node 24 majors ahead of the June 2026 deadline |
| #94 | 2026-06-10 | Hours saved surfaces on every Ask thread + live dashboard sparkline (the dashboard is gone) |
| #96 | 2026-06-11 | The enterprise wave (45 commits): identity & RBAC, per-person data passes, sources-mode GraphJin, context versioning, Slack & WhatsApp, security hardening (encryption, audit chain, behavioral alarms, profiles) (WhatsApp is removed) |
| #98 | 2026-06-11 | OpenShell becomes the only agent runtime — microsandbox and in-process paths removed (OpenShell is removed) |
| #100 | 2026-06-11 | Lockfile cleanup completing the microsandbox removal |
| #101 | 2026-06-11 | Chat-first configuration of the customer data engine: sources, roles, access — credentials value-blind |
| #103 | 2026-06-11 | Runtime-flag residue cleanup + the first edition of this document |
| #105 | 2026-06-11 | Post-release smoke tests the release it just built (was frozen on a stale tag) |
| #106 | 2026-06-11 | Smoke pre-pulls the OpenShell gateway image (OpenShell is removed) |
| #107 | 2026-06-11 | Smoke pre-pull list derived from the compose files themselves — no more hardcodes |
| #108 | 2026-06-11 | Seven live-sweep fixes: plugin-base default, sandbox corpse cleanup, egress, provisioning, Hermes ACP (plugins, the agent sandbox of that time and Hermes are removed) |
| #110 | 2026-06-11 | Delete a workflow from chat by @mentioning it, with composer autocomplete |
| #112 | 2026-06-11 | Hermes gains the full neko MCP toolset via stdio bridges — chat-first admin on every backend (Hermes is removed) |
| #114 | 2026-06-12 | Agent-runtime resilience batch: bridge proxy env, broker warm-up, doc links |
| #116 | 2026-06-12 | Production runs the released compose stack — source-built deploy path retired |
| #118 | 2026-06-12 | Fresh demo boots clean in sources mode (env conflict + keystore generation fixed) |
| #120 | 2026-06-12 | Free-text personas in onboarding (solo + multi-user) and a two-card briefing grid (the briefing pages are gone) |
| #121 | 2026-06-12 | Demo seed survives the wizard's password rotation |
| #124 | 2026-06-12 | Deploys garbage-collect superseded images — no more disk-full failures |
| #125 | 2026-06-12 | Agent runs survive the setup wizard: the sandbox gateway tracks the rotated DB password (the sandbox gateway is removed) |
| #127 | 2026-06-12 | Deploys follow smoked releases, not merges |
| #129 | 2026-06-12 | Thread-scale answer surfaces, sidebar timestamps, diagnosable agent exits |
| #132 | 2026-06-12 | Agentic answers reach hand-tuned speed (join-path hubs, query pattern cards, compact digests everywhere) and the "agent died mid-run" mystery is fixed — turn timeouts are generous, configurable, and say so |
| #220 | 2026-08-18 | Document library: uploads distilled into an OKF knowledge base the assistant searches and cites (distillation is removed; uploads go to file search) |
| #449 | 2026-10-12 | Chat-first web app: personal bots with neko avatars, routines in chat, skills found by meaning, priced Ax runs |
