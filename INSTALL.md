# Install OpenNeko

Sections below **Command reference** are reference — not needed to get started.
What you're installing, feature by feature, in plain language: **[FEATURES.md](FEATURES.md)**.

## Requirements

- Docker Desktop on macOS, or Docker Engine + Docker Compose on Linux
- An API key for at least one supported model provider, or the URL of a model server that speaks the OpenAI chat API (Ollama, vLLM, LM Studio)

## Install

```bash
curl -fsSL https://openneko.app/install.sh | sh
mkdir -p ~/openneko && cd ~/openneko
openneko setup --mode demo
```

The installer picks the right path for your platform — Homebrew on macOS, a checksum-verified release binary on Linux — and checks for Docker. `openneko setup` then runs preflight (Docker daemon up, host supported, ports free), brings up the stack, and configures it (see [Setup](#setup)).

`--mode demo` pulls pinned images and loads AdventureWorks sample data with three example watchers. For your own data, use `--mode prod` ([Use your own data](#use-your-own-data)).

### Manual install

Prefer to manage the binary yourself:

- **macOS (Homebrew):** `brew install open-neko/tap/openneko`
- **Linux:** download `openneko_<version>_linux_<arch>.tar.gz` from the [latest release](https://github.com/open-neko/openneko/releases/latest), verify it against `checksums.txt`, and put `openneko` on your `PATH`.

Then run `openneko setup` (guided), or `openneko start` to bring the stack up without the guided flow.

## Setup

`openneko setup` walks through the steps below **in the terminal**. Prefer a browser? Choose **browser** at the first prompt (or pass `--skip-onboarding`, or just run `openneko start`) and open [http://localhost:3000](http://localhost:3000) for the same wizard. Either way:

1. With the bundled database (`--mode demo` or `--mode dev`), choose an admin database password. In prod mode, setup asks for [OpenNeko's database](#opennekos-database) URL before it starts the stack.
2. Confirm the pre-filled GraphJin URL, `http://graphjin:8080`. It names the GraphJin that runs inside `openneko-serve`. Change it only when you run a GraphJin of your own.
3. Pick the model provider the agent should use (Anthropic / OpenAI / Google / Ollama / others). For any other server that speaks the OpenAI chat API, pick **OpenAI-compatible URL** and enter its Base URL, the model name and, when the server needs one, a key. Every model call, the agent's and the app's own, runs in `openneko serve`. The agent (Ax) runs there with the full tool set, including the chat-first management tools. It gets a terminal only when you connect a [sandbox](#sandbox-optional).
4. Add your provider API key. To use other models for some tasks, or a fallback model when a provider is down, add them later at Admin, Settings, Models.
5. Add an industry research provider, or skip.

Both paths drive the same web endpoints with the same validation (a live provider-key test, a data-source connectivity check), and each step persists independently — so you can start in the terminal and finish in the browser, or vice versa. For unattended installs (CI), pass `--admin-password`, `--provider`, `--provider-key`, etc. to run setup headless.

Integrations come from **solution packs**, installed after setup from **Admin → Settings → Solution packs** or with `openneko pack install`. Slack, Telegram and the HUD are built-in channels that turn on when an admin saves their settings under **Admin → Settings → Channels**; sign-in (Scalekit SSO or email link) is configured under **Admin → Settings**. (`--skip-plugins` and `--plugins` are still accepted by `setup` but do nothing.)

The AdventureWorks seed pre-fills business onboarding (`AdventureWorks Cycles`, fiscal year `July`, seats `CEO`/`CFO`/`COO`, priorities `Defend wholesale margins` / `Grow DTC in Europe`). Otherwise fill it in at `/onboarding`.

## Multiple customer instances on one host

Give every customer a stable lowercase instance name. A named installation gets
its own Compose project, databases and named volumes, encrypted config and
secrets, backup namespace, runtime markers, and Docker network.

```bash
# Bind customer apps to loopback when Caddy, nginx, or another TLS proxy is
# the public entry point. Setup persists the port.
openneko --instance acme setup \
  --mode prod \
  --port 3101 \
  --bind-address 127.0.0.1

openneko --instance globex setup \
  --mode prod \
  --port 3102 \
  --bind-address 127.0.0.1
```

Route a separate TLS hostname to each loopback web port. For example, with
Caddy:

```caddyfile
acme.example.com {
  reverse_proxy 127.0.0.1:3101
}

globex.example.com {
  reverse_proxy 127.0.0.1:3102
}
```

Only the reverse proxy needs public `80`/`443` firewall access. The customer web
ports above stay on host loopback. Databases, GraphJin, brokers, the file
service and the agent runtime stay inside their private Docker networks.

For named instances, omitting `--port` makes setup choose an available port
and persist it. Inspect
the resulting inventory with:

```bash
openneko instances
```

Target every later operation explicitly:

```bash
openneko --instance acme status
openneko --instance acme logs -f
openneko --instance acme upgrade
openneko --instance acme backup
openneko --instance acme stop
```

Named settings live under
`~/.config/openneko/instances/<name>/installation.json`; durable runtime state
lives under
`${XDG_STATE_HOME:-$HOME/.local/state}/openneko/instances/<name>/`. Do not reuse
an instance name for two customers on the same Docker daemon. Each customer is
still a complete OpenNeko stack, so size VM memory, disk, and backup retention
for the number of concurrent instances.

Named instances are an operational and data-placement boundary, not a
hypervisor security boundary. A host administrator or anyone with Docker daemon
access can reach every stack. Use a separate VM or equivalent tenant boundary
when a customer contract, threat model, or compliance regime requires host-level
isolation.

## Use your own data

```bash
mkdir -p ~/openneko && cd ~/openneko
openneko setup --mode prod --database-url 'postgres://neko:PASSWORD@db.example.com:5432/neko?sslmode=verify-full'
```

Prod mode runs no database of its own. Give it [OpenNeko's database](#opennekos-database)
with `--database-url`, or enter the URL when setup asks for it. Keep the pre-filled GraphJin URL.

Then connect your data from chat. As an admin, ask the assistant to connect a database, and it shows a card for a PostgreSQL, MySQL or MariaDB source: where the database runs, the read-only login and its password. **Check connection** tests each step and lists the tables the login can read. You approve the change in the card, and OpenNeko writes the GraphJin config. For a system with a pack, such as Magento or Shopify, the assistant shows the pack's setup card. You enter the address and the login only. See [Solution packs and skills](#solution-packs-and-skills).

If you run your own GraphJin on the host, give setup `http://host.docker.internal:8080`. OpenNeko adds the GraphQL and MCP paths.

## OpenNeko's database

OpenNeko keeps its own data in Postgres 16 or later, with pgvector 0.6.0 or
later for the `vector` extension. In
prod mode you run that Postgres server, and OpenNeko connects to it by one URL.
Dev and demo modes run a Postgres container (`neko-db`) for you.

```text
postgres://neko:PASSWORD@db.example.com:5432/neko?sslmode=verify-full&sslrootcert=/etc/openneko/db-ca.pem
```

`openneko setup --database-url` stores the URL in
`~/.config/openneko/config.json`, encrypted with the host secret key. You can
also put the URL in a file that only the OpenNeko account can read, and set
`OPENNEKO_DATABASE_URL_FILE` to its path. The URL replaces the `NEKO_PG_*`
settings. In prod mode, `openneko start` stops with a message when it finds no
URL.

`openneko start` gives the URL to the containers as a Compose secret, a file
at `/run/secrets/database-url`. It does not go into their environment.
`openneko-serve` does not get it. The URL host must be one that the containers
can reach: `localhost` names the container itself, so setup refuses it. For a
Postgres server on the same host, use `host.docker.internal` or the host's
name.

- `sslmode=verify-full` checks the server certificate and its host name.
  `verify-ca` checks only the certificate, and `require` encrypts without
  checking it. Use one of these three. With `prefer` or `allow`, web and the
  worker connect without TLS.
- `sslrootcert` names the CA file. Without it, OpenNeko uses its default CA
  list. `sslcert` and `sslkey` name a client certificate and its key, for a
  server that asks for one. Give paths on the host where you run `openneko`.
  `openneko start` copies each file into the containers that connect to the
  database, `openneko-serve` included, as a Compose secret readable only by
  user 1001, and points the URL at the copy. Serve's two
  [role logins](#the-serve-roles) use the same files.
- Give OpenNeko a database of its own and a login of its own. Restore drops
  every object that the login owns in that database (see
  [Backup and restore](#backup-and-restore)). The `neko` role needs `CREATE`
  on that database and on schema `public`, and `CREATEROLE`. It does not need to own
  the database, so a managed Postgres service works. The migrations create
  the `vector` extension, and each start creates the
  [serve roles](#the-serve-roles).
- Postgres 16 is the oldest version that OpenNeko supports. The storage check
  needs Postgres 15, CI and the bundled database run 16, and
  `openneko database move` restores a Postgres 16 dump. pgvector 0.6.0 is the
  oldest version tested with every migration.

Before the other start steps, `openneko start` and `setup` run
`openneko database check` in a container. It connects with the URL, reports
the Postgres and pgvector versions and the TLS mode. It checks that the login
can create schemas in the database and tables in schema `public`, can create
the `vector` extension (or finds it), and can create the two roles or set
their passwords. When a version is too old or a right is missing, start stops
and prints what to do, for example:

```text
  FAIL  neko cannot create schemas in neko. The start steps and the job queue create their own schemas
        Run this SQL as a superuser, in database neko:
          GRANT CREATE ON DATABASE "neko" TO "neko";
```

Run the SQL, then run `openneko start`. A URL without `sslmode=verify-full`
gives a warning, and start goes on. The check also warns when the login owns
objects that OpenNeko did not make: objects that no OpenNeko migration names,
outside the `pgboss`, `river` and `openneko_internal` schemas. A restore stops on them.
- With a URL, setup skips its database password step. Change the password
  where the database is managed, then update the URL.

The next start step, `storage-reconcile`, only reports on a database that you
run. It prints the collation provider and version. When the version changed
since the database recorded it, for example after an operating system upgrade
on the database server, it prints the `REINDEX DATABASE` and
`ALTER DATABASE ... REFRESH COLLATION VERSION` lines to run. It changes
nothing. With the bundled `neko-db`, it rebuilds the indexes itself.

### The serve roles

`openneko-serve` connects to the database with two narrow roles. Each role
has the database name in it, so two installs can use two databases on one
Postgres server:

- `neko_files_<database>` can read and write the file index tables.
- `neko_subscriptions_<database>` can read `workflow_output`.

Postgres keeps 63 bytes of a role name. When the name does not fit, or the
database name has a character other than `a` to `z`, `0` to `9` or `_`,
OpenNeko changes each other character to `_`, keeps the start of the name and
adds 8 hex characters of the database name's SHA-256. For example, database
`My-Shop` gets `neko_files_my_shop_` and 8 hex characters.
`openneko database check` prints the two names.

The start step `openneko-serve-init` runs at each start. It creates the two
roles when they are missing, grants their rights again and sets their
passwords, which it derives from the secret key. The `neko` login needs
`CREATEROLE` for this. Without it, a superuser can create the roles and
give the login `ADMIN` on them:

```sql
CREATE ROLE "neko_files_neko" NOLOGIN;
GRANT "neko_files_neko" TO "neko" WITH ADMIN OPTION;
```

Releases before this one used two roles for the whole server, `neko_files`
and `neko_subscriptions`. An upgraded install gets the new roles at its next
start, with the same rights. The old roles stay, because an install on the
same server that runs an older release may still use them.
`openneko database check` names each old role it finds and prints the SQL to
drop it. Run that SQL when no install on the server runs an older release.

### The bundled database of an older release

Releases before this one ran `neko-db` in prod mode too. When a prod install
has the `neko-db-data` volume and no database URL, `openneko start` and
`openneko upgrade` keep running `neko-db` with that volume, so the data stays
where it is. A new prod install never gets `neko-db`.

To move such an install to a database that you run, prepare the database as
in [OpenNeko's database](#opennekos-database), with the stack running. Then:

```bash
openneko database move --database-url 'postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=verify-full' --confirm MOVE
```

The command does these steps in order:

1. It checks the new database. It stops when the URL's login already owns
   tables there.
2. It stops web, worker and `openneko-serve`, and runs the start steps on the
   new database.
3. It takes a backup of `neko-db` into the usual backup folder.
4. It removes the `neko-db` container and restores the backup into the new
   database.
5. It starts the stack on the new database and stores the URL.

The new database gets serve roles with its own name, and the restore skips
the grants of the old ones ([the serve roles](#the-serve-roles)).

Until step 5, the install still uses `neko-db`. When a step fails, fix the
cause and run `openneko start -d` to go back, or run the move again. The
`neko-db-data` volume stays. Remove it when you no longer need it; the command
prints its name.

## Solution packs and skills

```bash
openneko pack list
openneko pack install magento
openneko install https://github.com/<owner>/<repo>#<path/to/skill>   # community skill (admin opt-in)
```

Packs add sources, actions, workflows and skills, and can turn on the built-in Slack, Telegram and HUD channels. See **[PACKS.md](PACKS.md)**. `openneko doctor` checks your host can run the stack.

---

*Reference below — not needed to get started.*

## Command reference

```bash
openneko [--instance name] setup [--mode prod|dev|demo] [--port N] \
  [--bind-address IPv4] [--database-url postgres://...]
openneko instances                 # configured host installations
openneko start [--mode prod|dev|demo] [--detach]
openneko upgrade [--version vX.Y.Z] [--mode auto|prod|dev|demo]
openneko status                    # docker compose ps proxy
openneko logs [service…] [-f]
openneko stop [--volumes]          # --volumes wipes data
openneko migrate                   # apply pending migrations to OpenNeko's database
openneko serve [--agent-socket path]  # user files and file search; agent turns on a Unix socket when set (runs as the openneko-serve container)
openneko files copy-to-bucket      # copy user files from the files folder to OPENNEKO_FILES_BUCKET
openneko seed adventureworks       # one-shot demo data load (already done by --mode demo)
openneko [--instance name] graphjin import <customer-config.yml|->
openneko graphjin check [--config file]  # load a GraphJin config with the pinned loader; exit 1 and the reason when it is not valid
openneko source check --type postgres|mysql|mariadb --host H [--port N] \
  --database D --user U (--secret NAME | --password-stdin) [-o json]
openneko reset [--all]             # tear down + clear local config (--all also wipes secrets)
openneko doctor                    # host check, docker daemon, stack health
openneko version
```

Modes:

- **`prod`** (default) — the production control, customer-data, file, and
  agent runtime services. `openneko start` runs five start steps in order,
  then starts three containers: `openneko-serve`, `web` and `worker`.
  OpenNeko's database is yours, by URL.
- **`dev`** — source-checkout workflow: use `pnpm dev:setup` for Docker-only dependencies plus AdventureWorks demo data, then `pnpm dev` for hot-reloaded web/worker from source.
- **`demo`** — core + `neko-db` + AdventureWorks: `adventureworks-db`, `adventureworks-init`, `neko-adventureworks-seed`. The full live-trial flow (continuous order trickle + scenario injector) lives in `compose.adventureworks.yml` and needs the [Build from source](#build-from-source-advanced) path.

`~/.config/openneko/compose.override.yml` is auto-applied if present (last `-f` to docker compose).

`graphjin import` accepts a YAML fragment with `sources`, `tables`, and/or
`relationships`. Use `-` to read it from standard input. It requires a running
production installation, validates the change through GraphJin, seals source
credentials, and preserves OpenNeko-managed authentication and keystore settings.

`source check` tests one customer database before you connect it, in this
order: the host resolves, the port answers, TLS, the login, the rights to read
the schema, and read-only rights. A failed step gives one sentence and, where
SQL fixes it, the SQL for the database administrator, for example how to create
a read-only login. A passed check lists the tables the login can read, with
row estimates. `--secret` names a password saved in Admin settings, GraphJin
config; `--password-stdin` reads it from standard input. The check runs in the
worker, so the stack must be running. It exits 1 when a step fails. It checks
PostgreSQL, MySQL and MariaDB, and says so for any other type.

`graphjin check` loads a GraphJin config file with the GraphJin version built
into `openneko` and runs the checks GraphJin runs at start. It prints the first
problem and exits 1, or prints "valid" and exits 0. The default file is
`OPENNEKO_GRAPHJIN_CONFIG`.

Admin settings, GraphJin config shows this file read-only, with secrets and
comments hidden, and says whether GraphJin is running. Edit the file on the
server, then run `openneko graphjin check`.

### Audit logging failure alerts

Every failed tamper-evident audit append emits a structured
`security.audit_logging_failure` event to stderr. The worker also exposes
process-local state at `GET /health/security`; monitor for
`status: "degraded"` or `auditLogging.healthy: false`.

For immediate delivery independent of PostgreSQL, configure an HTTPS webhook
on both processes that can write audit events:

```yaml
# ~/.config/openneko/compose.override.yml
services:
  web:
    environment:
      OPENNEKO_AUDIT_FAILURE_WEBHOOK_URL: https://alerts.example/audit-failure
      OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN: ${OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN}
  worker:
    environment:
      OPENNEKO_AUDIT_FAILURE_WEBHOOK_URL: https://alerts.example/audit-failure
      OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN: ${OPENNEKO_AUDIT_FAILURE_WEBHOOK_TOKEN}
```

The token is optional and is sent as a bearer token. Webhook delivery has a
two-second timeout, is fire-and-forget, and never blocks the governed operation.
Alert delivery failures are themselves emitted as structured critical stderr
events.

### Agent runtime

Web and worker start one agent runner per run. The runner stages the run's
files in a shared folder and sends each turn to `openneko serve` over a Unix
socket. The model key travels with the turn, and serve keeps every `*_API_KEY`
variable out of the agent's tools. The agent reads and writes files in its
run folder and reaches data only through the OpenNeko broker. The agent has
a terminal only when a sandbox is connected.

### External services

OpenNeko uses four services outside the core. Each one has a URL and a key.
Each one has a health check. Admin, Administration, External services shows
each service as On, Off or Failing, with what the check found.

| Service | URL | Key | Health check | When it is off |
|---|---|---|---|---|
| Inference | the default model at Admin, Settings, Models | the model's key | one short prompt to the default model | agent turns and summaries stop |
| Embedding | `NEKO_EMBEDDING_URL` and `NEKO_EMBEDDING_MODEL` | `NEKO_EMBEDDING_API_KEY` | one short text through the embedding gateway | search uses Postgres full-text search |
| Sandbox | `OPENNEKO_SANDBOX_URL` | `OPENNEKO_SANDBOX_KEY` | `GET /v2/check` with the key | no terminal and no script skills |
| File store | `OPENNEKO_FILES_BUCKET` on `openneko-serve` | `OPENNEKO_FILES_BUCKET_KEY_ID` and `_SECRET` | `openneko serve` checks the bucket | files stay in the local folder |

The inference and embedding checks each make one small paid call. The page
keeps the results for one minute. "Check again" runs every check at once. No
key appears in a result: a URL shows only its scheme and host.

### Sandbox (optional)

OpenNeko runs no command for the agent. To give the agent a terminal, and to
turn on workflow script steps and the bundled skills that run scripts
(`xlsx`, `docx`, `pptx`, `pdf` and `skill-creator`), connect
a sandbox service.
Give web, worker and `openneko-serve` its URL and key, then run
`openneko stop` and `openneko start`:

```yaml
# ~/.config/openneko/compose.override.yml
x-sandbox: &sandbox
  OPENNEKO_SANDBOX_URL: unix:///run/openneko/sandbox.sock   # or http(s)://host:port
  OPENNEKO_SANDBOX_KEY: <a long random string>
services:
  openneko-serve:
    environment: *sandbox
  web:
    environment: *sandbox
  worker:
    environment: *sandbox
```

Without a URL the agent has no terminal, the script skills are hidden from
every run, and a workflow script step fails with a message. Each command
carries the files of the run that changed since the last command, and the
sandbox returns the files that the command changed. The sandbox keeps its
own copy of each run and shares no folder with the stack, so it can run on
another host: use an `https://` URL and a key. It never gets a model key,
the broker token, the run's job file or a database password. The first
sandbox service is `recipes/sandbox`; see `recipes/sandbox/README.md`.

With a sandbox connected, each organization has a **Terminal** setting at
Admin, Settings, Agent. It is on by default. An admin who turns it off
takes the terminal, the script skills and workflow script steps away from
that organization's runs, from the next run. Without a sandbox URL the
setting shows as unavailable.

This release replaces the bundled `xlsx`, `docx`, `pptx` and `pdf` skills
with new ones. It removes the bundled `document-extraction`,
`internal-comms`, `excel-author`, `pptx-author` and five financial-model
skills; the new `pdf`, `docx`, `pptx` and `xlsx` skills read those files. On
upgrade, OpenNeko deletes each organization's unchanged copy of the old
skills and adds the new ones. An admin's changed copy stays as that
organization's own skill, and the new skill does not replace it. To get the
new skill, delete the changed copy in Skills.

### Semantic search (optional)

OpenNeko finds memories and skills with Postgres full-text search. To find
them by meaning as well, connect an OpenAI-compatible embedding service. Give
web and worker the same values, then run `openneko stop` and `openneko start`:

```yaml
# ~/.config/openneko/compose.override.yml
x-embedding: &embedding
  NEKO_EMBEDDING_URL: https://embeddings.example/v1   # OpenNeko calls <url>/embeddings
  NEKO_EMBEDDING_MODEL: text-embedding-3-small
  NEKO_EMBEDDING_API_KEY: ${NEKO_EMBEDDING_API_KEY}   # optional
  NEKO_EMBEDDING_DIMENSIONS: "768"                    # optional
services:
  web:
    environment: *embedding
  worker:
    environment: *embedding
```

Semantic search is off until both the URL and the model are set. When it is
on, openneko-serve sends memory and skill text, and each search query, to that
service for web and worker. The service must answer on the stack's network. A
background job embeds stored rows; until it reaches a row, and whenever the
service fails, search uses full-text search. A change of model re-embeds
every row. To run a model on your own machine, see
`recipes/embedding-gemma/README.md`.

### Traces (optional)

OpenNeko sends no traces until you give it an OTLP/HTTP collector. Give web,
worker and openneko-serve the endpoint, then run `openneko stop` and
`openneko start`:

```yaml
# ~/.config/openneko/compose.override.yml
x-traces: &traces
  OTEL_EXPORTER_OTLP_TRACES_ENDPOINT: http://collector:4318/v1/traces
services:
  web:
    environment: *traces
  worker:
    environment: *traces
  openneko-serve:
    environment: *traces
```

Each agent run is one trace. A GraphJin query or tool call that the run
makes appears inside that trace, under the run. The spans hold names, times,
roles and status codes. They hold no prompt, query text, query variables or
model output. GraphJin's spans also drop error text. `OTEL_SDK_DISABLED=true`
turns traces off in each service. For the full list of settings, see
`evals/README.md`.

### File search

The `openneko-serve` container keeps each user's files in a private folder
on the `openneko-files` volume. The assistant gets three tools over the files
of the user who owns the run:

- search: keyword search over PDF, Word, PowerPoint, Excel, CSV, Markdown and
  text files, with the matching passages
- read text: the text of one file, with page, slide and sheet markers
- query sheet tables: one read-only DuckDB query over the sheets of
  spreadsheet and CSV files

The keyword index is in OpenNeko's database. The sheet tables are Parquet files on the
same volume. OpenNeko rebuilds both from the files, so you can delete them.
DuckDB runs with external access off and reads only the tables of that user.
Web and worker reach the service at `OPENNEKO_SERVE_URL` with a token that
expires after one minute. When `OPENNEKO_SERVE_URL` is not set, the assistant
does not get the file tools.

`openneko-serve` connects to OpenNeko's database as `neko_files_<database>`
([the serve roles](#the-serve-roles)). That role can read and write only the
file index tables. The start step `openneko-serve-init` sets its password,
derived from the secret key, and gives the service a key that checks the
tokens. The service never gets the secret key or the `neko` password.

Files a user adds to the Library go into that folder. When `OPENNEKO_SERVE_URL`
is not set, the Library refuses uploads. After an upgrade, the worker moves
Library files from older releases into the folder once, at start. It marks a
document as failed when its original file is gone.

### Files in a bucket (optional)

By default `openneko-serve` keeps the files in a folder on the
`openneko-files` volume. To keep them in a bucket on Amazon S3 or another
S3-compatible service, give `openneko-serve` these settings, then run
`openneko stop` and `openneko start`:

```yaml
# ~/.config/openneko/compose.override.yml
services:
  openneko-serve:
    environment:
      OPENNEKO_FILES_BUCKET: s3://my-bucket/openneko      # the prefix is optional
      OPENNEKO_FILES_BUCKET_REGION: eu-west-1             # us-east-1 when not set
      OPENNEKO_FILES_BUCKET_ENDPOINT: https://s3.example.com  # only for a service other than Amazon S3
      OPENNEKO_FILES_BUCKET_KEY_ID: <access key id>
      OPENNEKO_FILES_BUCKET_SECRET: <secret access key>
```

Without a key ID and secret, serve uses the host's AWS credentials, for
example an instance role. The endpoint must use `https://`; serve accepts
`http://` only for `localhost`. The login needs list, read, write and delete
rights on the prefix. Two installs can share one bucket with two prefixes.

The keyword index stays in OpenNeko's database, and the sheet tables stay on
the `openneko-files` volume. Serve rebuilds both from the bucket.

Serve checks the bucket at start and logs the result. When it cannot reach
the bucket, serve still starts: agent turns and GraphJin work, and the file
tools fail with the bucket's error until it is back. `GET /healthz` on serve
reports `file_store` and `file_store_ok`.

Serve does not read the folder after the switch, and it logs how many files
the folder still holds. To copy them to the bucket, run the copy in the
running serve container:

```bash
docker exec openneko-prod-openneko-serve-1 /openneko files copy-to-bucket
```

A named instance has another container name; `openneko status` shows it.

The copy skips a file that the bucket already holds with the same size, so
you can run it again. It does not delete the files in the folder. Remove them
after you check the bucket. To go back to the folder, remove the settings and
copy the files back with your bucket tool.


## Backup and restore

`openneko backup` writes one encrypted file while the stack runs. The file
holds a `pg_dump` of OpenNeko's database and the config and file volumes
(`openneko-config`, `openneko-graphjin-cli` and `openneko-files`). With a
database URL, `pg_dump` runs in a worker container and connects by the URL and
its TLS files, so the host needs no Postgres tools. The dump holds the objects
that the URL's login owns. It leaves out the schemas and tables of other roles.
With `OPENNEKO_FILES_BUCKET` set, the backup does not hold the files in the
bucket; back up the bucket with your storage service, for example with
versioning. Files go to
`${XDG_DATA_HOME:-$HOME/.local/share}/openneko/backups/`. Use `--to` or
`OPENNEKO_BACKUP_DIR` to write them somewhere else, such as a NAS.

```bash
openneko backup
openneko restore <file> --confirm RESTORE
```

Restore decrypts and checks the whole file before it changes anything. Then it
names the database it will replace, stops the stack, replaces the volumes,
restores the database and starts the stack again. Migrations bring an older
backup forward. `openneko doctor` warns when the latest backup is missing or
more than a week old.

With the bundled `neko-db`, restore drops and recreates the database. With a
database URL, restore cannot do that. It drops every object that the URL's
login owns in the database the URL names, then restores the dump in one
transaction. It keeps the extensions, the objects that other roles own and the
grants that you gave the login. It restores the grants of the dump, except the
grants to a [serve role](#the-serve-roles) of any install or release. The
start step `openneko-serve-init` then grants this install's serve roles again.
A restore into the bundled `neko-db` restores no grants. Restore does not put
a backup of an external database into a bundled `neko-db`.

Before restore stops the stack, it lists the objects that the login owns and
that OpenNeko did not make. An object is OpenNeko's when the backup holds it,
an OpenNeko migration names it, or it is in the `pgboss`, `river` or
`openneko_internal` schema. When the list is not empty, restore prints it, changes nothing and
stops. Move those objects to another login, or give OpenNeko a database and a
login of its own. To drop them with the restore, add `--drop-foreign-objects`.
Restore then prints the list again and asks you to type the database name.
Nothing brings the dropped objects back.

Each installation has its own backup key. Keep a copy away from the backups:

```bash
openneko backup key export --to /secure/off-host/openneko-backup.key
openneko backup key adopt --from /secure/off-host/openneko-backup.key
```

On a new host, `adopt` installs the copied key. You can also pass
`--key-file` to a single restore. A file is AES-256-GCM in authenticated
chunks, so a wrong key, a cut file or any edit fails the check.

## Upgrade

`openneko upgrade` is the normal stack upgrade path. It:

1. Takes an `openneko backup` of the running stack and stops if the backup fails. Pass `--skip-backup` to upgrade without one.
2. Pulls the new OpenNeko images: `neko-cli`, `neko-serve`, `neko-web`, `neko-worker`, and `neko-db` when the install uses the bundled database.
3. Detects the mode recorded by your last `setup` or `start` (`prod`, `dev`, or `demo`) and recreates that stack detached.
4. Re-runs every idempotent initializer against the existing volumes, including runtime-artifact repair and pending migrations.
5. Waits for every declared service health check; a crash loop or unreachable data plane fails the upgrade instead of being reported as success.
6. Persists the new version and removes old OpenNeko image tags only after the stack is healthy (unless you pass `--no-prune`).

Run it from the same install directory you use for `openneko start`:

```bash
cd ~/openneko
openneko upgrade
```

Install a specific image version/tag:

```bash
openneko upgrade --version v1.18.0
# "1.18.0" is accepted too and is normalized to "v1.18.0".
```

Older installs without a saved mode marker are detected from their existing
Docker Compose project (`openneko`, `openneko-dev`, or `openneko-demo`). If more
than one OpenNeko stack exists on the machine, pass the intended mode explicitly:

```bash
openneko upgrade --mode demo
```

The command persists the selected image tag in `.openneko/runtime/.image-version`,
so later `openneko start`, `backup`, `restore` and `database move` run the
same tag. Upgrading the host binary is still
recommended when a release includes CLI changes.

### Upgrade from a release before November 2026

This release has fewer parts. The upgrade migrates your data in place:

- **Records** is gone. The migrations drop its tables, policies and workflows.
  Take a backup first if you need that data (`upgrade` takes one).
- **Plugins** are gone. Scalekit and email-link sign-in are built in. Slack,
  Telegram and the HUD are built-in channels. The upgrade moves their
  credentials, workspaces, identities and grants to the built-in names. A
  channel that had credentials is on after the upgrade, with no step from
  you ([PACKS.md](PACKS.md#channels)). Resend, Slack, Shopify and Parallel web
  search actions are packs now; install them from **Admin → Settings →
  Packs**.
- **The Library** keeps your files. At start, the worker moves each uploaded
  file into its owner's private folder for [file search](#file-search).
  Concepts and distillation are gone.
- **The OpenShell runtime, the embedding container and the separate GraphJin
  containers** are gone. `openneko-serve` runs agent turns and both GraphJins.
  Search by meaning is off until you connect an
  [embedding service](#semantic-search-optional), and the terminal is off
  until you connect a [sandbox](#sandbox-optional).
- **The serve roles** now have the database name in them. The first start
  creates `neko_files_<database>` and `neko_subscriptions_<database>` with the
  rights of the old roles. On a database that you run, the `neko` login needs
  `CREATEROLE` or `ADMIN` on the new roles ([the serve roles](#the-serve-roles)).
- **A prod install** keeps its bundled `neko-db`. Move it to a database that
  you run with [`openneko database move`](#the-bundled-database-of-an-older-release).

The upgrade removes the old containers and images. It leaves the
`openneko-plugins` and `openshell-config` volumes and the
`<project>_runtime` network, which nothing uses. Remove them with
`docker volume rm` and `docker network rm` when you no longer need them.

### macOS

```bash
brew update
brew upgrade openneko
cd ~/openneko
openneko upgrade
```

> **Installed before 1.17.2?** Those releases shipped a Homebrew *formula*; 1.17.2+ ships a *cask*, so `brew upgrade openneko` won't move you across. Switch once:
>
> ```bash
> brew uninstall --formula openneko
> brew install --cask open-neko/tap/openneko
> ```
>
> After that, `brew upgrade openneko` keeps the cask current as normal.

### Linux

```bash
TAG=$(curl -fsSL https://api.github.com/repos/open-neko/openneko/releases/latest | grep -oE '"tag_name": *"[^"]+"' | head -1 | cut -d'"' -f4)
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
curl -fsSL "https://github.com/open-neko/openneko/releases/download/$TAG/openneko_${TAG#v}_linux_$ARCH.tar.gz" | tar -xz openneko
sudo install -m 0755 openneko /usr/local/bin/ && rm -f openneko
cd ~/openneko
openneko upgrade
```

### What `openneko start` does

1. **Start steps.** Writes the embedded compose files to `.openneko/runtime/`, then runs the start steps in order, each in its own container: the database check (prod mode), the storage check, the migrations under a Postgres advisory lock, the config volume, and serve's credentials and GraphJin config. Demo mode also loads the sample data. A failed step stops the start. [AGENT_RUNTIME.md](AGENT_RUNTIME.md#start-steps) lists each step.
2. **Compose up.** Runs `docker compose up` for web, worker and `openneko-serve`. Docker pulls the pinned image tags.

`openneko stop` doesn't touch volumes. Use `--volumes` for a clean slate (wipes the metadata DB, demo state, agent workspaces).

## Reset

```bash
openneko stop --volumes   # wipes data; keeps secrets
openneko reset --all      # wipes everything including secrets
```

## Ports

The packaged stack publishes one host port: the web app, `3000` by default.
In the web container, `openneko web` answers that port. It serves the web app
and answers the version, sign-in, session, sign-out, device sign-in and SSO
setup routes, the business profile route, the route that connects a
personal pack account, the approval, action request and observation routes,
the route that starts the industry insights job, the memory routes, the skill routes, the config history and restore routes, the assets route, the audit export, the run file download, the file upload, the chat thread routes (list, open, create, delete, truncate), the run event list, the run timing report, the in-chat action decision, the workflow list and detail, the workflow changes (pause, schedule, delete, adopt for the team), the workflow API access settings, the workflow subscription routes (list, create, pause, delete, dry run), the workflow run list, detail and result download, the two webhook-compatibility routes under `/api/hooks` (start a run, fetch its result), the public workflow API that a workflow's bearer token opens (start a run, read its status, download its result), these admin settings: the channel identities (list, link, unlink, block), the data source registry, the named source secrets, the install policy, skill learning, the Ask-based config switch, the action rules (list, detail, approver group, approved targets) and the database source check, and each person's persona. It passes the rest of the API and the admin pages to the Next server
on `127.0.0.1:3001` inside the container. The bundled Postgres, GraphJin, the worker broker, the file service, the agent runtime and
the demo data services stay on the private Docker network.

Configure and persist the port during guided installation:

```bash
openneko setup --mode prod --port 3001
```

Setup still accepts `--openshell-port` and `--docker-subnet` from older
automation and ignores them. For the backward-compatible unnamed installation,
`OPENNEKO_PORT` remains available as a one-process environment override. Named
instances use their persisted assignments; re-run setup with explicit flags to
change them. `--bind-address 127.0.0.1` restricts the web listener to the host
for a reverse-proxy deployment.

## Build from source (advanced)

> Needed for developing OpenNeko itself or running the full live trial (continuous order trickle + scenario injector).

```bash
git clone https://github.com/open-neko/openneko.git
cd openneko
docker compose -f compose.yml -f compose.db.yml -f compose.adventureworks.yml --profile steps build
docker compose -f compose.yml -f compose.db.yml -f compose.adventureworks.yml run --rm openneko-serve-init
docker compose -f compose.yml -f compose.db.yml -f compose.adventureworks.yml up -d
docker compose -f compose.yml -f compose.db.yml -f compose.adventureworks.yml run --rm neko-adventureworks-seed
```

The `run --rm openneko-serve-init` line runs the start steps in order:
storage check, migrations, config volume, serve credentials. With your own
database in place of `compose.db.yml`, run `docker compose run --rm
database-check` first. Run it again
after each `git pull`, before `up`.

### Live trial data

The source compose trickles fresh sales orders into the sample DB every 10 minutes. Briefing numbers drift, cron workflows fire, runs accumulate on `/runs`. External actions use their configured adapters and approval policies.

```bash
AW_SIM_INTERVAL_SEC=300 AW_SIM_ORDERS_MIN=1 AW_SIM_ORDERS_MAX=5 \
  docker compose -f compose.yml -f compose.db.yml -f compose.adventureworks.yml up -d
```

Disable the trickle: `AW_SIM_ENABLED=0`.

### Watch the loop fire end-to-end

The seed pre-loads three watchers:

- **Daily Revenue Health Check** (9am cron) — yesterday's revenue vs trailing 7-day average.
- **Revenue Drop Alert** (hourly) — per-territory current hour vs same-hour-of-week baseline over 4 weeks; proposes a Slack alert if any territory falls below 50%.
- **Slow-Ship Operations** (8:30am cron) — orders stuck in *pending* > 5 days.

To see the loop without waiting for an organic dip, fire the Germany scenario (stops new orders for territory 8 for three hours):

```bash
docker compose -f compose.yml -f compose.db.yml -f compose.adventureworks.yml \
  exec adventureworks-scenario-injector \
  /scripts/scenario-injector.sh fire germany-revenue-drop
```

Wait ~15 minutes, then click **+ Run now** on **Revenue Drop Alert** in `/workflows`. A finding lands on the Briefing; a proposed Slack alert queues for approval. Click approve; the receipt lands under **Fired on your behalf**.

Then write your own watcher from `/work`, and swap AdventureWorks for your data — see [Use your own data](#use-your-own-data).

## Developer setup

Stack pieces in Docker, app processes from source:

```bash
corepack enable
pnpm bootstrap
pnpm dev:setup
pnpm dev
```

Web and worker run on the host with hot reload. Everything else runs in Docker.

- `pnpm dev:up` starts the backing services with `compose.db.yml` and `compose.dev.yml`: `neko-db`, the `openneko-serve` container with the customer GraphJin and the subscriptions GraphJin, and AdventureWorks with its simulator. It installs the pinned GraphJin CLI into `.openneko/dev/bin`, builds the serve image, runs migrations from source, and writes the GraphJin JWT secret.
- `pnpm dev:seed` seeds demo workflows. `pnpm dev:setup` runs both steps.
- `pnpm dev` runs `next dev` on port 3000 and `tsx watch` for the worker. Edits to `apps/web`, `apps/worker`, and workspace packages reload without an image build.
- `pnpm --filter @neko/web build:spa` builds the web app as static files in `apps/web/dist`, with a client router. `pnpm dev:web` runs `openneko web` on port 3000 in front of `next dev` on port 3001 (set `OPENNEKO_NEXT_PORT` for another port), as the release runs it in front of `next start`. It builds the static app first when `apps/web/src` changed since the last build. `dev:spa` serves the app with hot reload on port 5173 and passes `/api` and the admin pages to port 3000 (set `OPENNEKO_SPA_API` for another address). `openneko web` applies the sign-in and admin checks of `proxy.ts` to each request and sets the `Content-Security-Policy` header of `next.config.ts` on the app. It reads sessions, devices and groups, and writes users and their IdP groups at sign-in, with the main database login, and asks the worker at `--worker-admin-url` whether sign-in is on. After an admin changes the database password, `openneko web` opens each new database connection with the password in `config.json`. Go answers `/api/v1/version`, `/api/v1/auth/*`, `/api/v1/sso/setup/*`, `/api/v1/profile`, `/api/v1/my/pack-accounts/*`, `/api/v1/observations`, `/api/v1/approvals`, `/api/v1/action-requests/*`, `/api/v1/insights/ensure`, `/api/v1/work/memories/*`, `/api/v1/work/skills/*`, `/api/v1/work/config/*`, `/api/v1/work/assets`, `/api/v1/work/audit/export`, `/api/v1/work/files/*`, `/api/v1/work/upload`, the Library routes (`/api/v1/library/*`), the work thread and run routes, the bot routes (`/api/v1/bots/*`), the connector routes (`/api/v1/integrations/*`), the pack OAuth routes (`/api/v1/pack-accounts/*`), the workflow, subscription, workflow run and hooks routes, `/api/v1/policies/*`, `/api/v1/onboarding/status`, `/api/v1/onboarding/submit`, `/api/v1/settings/provider/test`, `/api/v1/admin/services`, the admin group, IdP rule, directory, item grant, data access, user, password, device, sign-in, public address, channel, spend, model, workflow API limit, pack and webhook-compatibility routes (`/api/v1/admin/groups/*`, `/api/v1/admin/idp-rules/*`, `/api/v1/admin/directory`, `/api/v1/admin/items`, `/api/v1/admin/item-grants`, `/api/v1/admin/data-access/*`, `/api/v1/admin/users/*`, `/api/v1/admin/change-password`, `/api/v1/admin/devices/*`, `/api/v1/admin/signin/*`, `/api/v1/admin/public-url`, `/api/v1/admin/channels/settings`, `/api/v1/admin/spend/*`, `/api/v1/admin/models/*`, `/api/v1/admin/model-routes`, `/api/v1/admin/workflow-api-limits`, `/api/v1/admin/packs/*`, `/api/v1/admin/reckon-webhooks/*`, `/api/v1/admin/context-remote/*`), and ten settings routes: `/api/v1/settings/channel-identities/*`, `data-sources`, `source-secrets`, `install-policy`, `skill-learn`, `graphjin-config`, `persona`, `source-check`, `file-sources` and `openapi-specs`. Next has no copy of these routes, so `next dev` alone does not sign anyone in. Go queues the action, insights and business profile jobs in the pg-boss schema that the worker creates at its first start, so start the worker once before you approve an action. Background jobs move from the worker's pg-boss queue to a River queue in the `river` schema, one queue at a time. `openneko migrate` creates and updates the `river` schema under the migration lock and moves the pending jobs of each moved queue from pg-boss. `openneko web` runs the moved jobs; today these are two hourly sweeps and one sweep each minute. The first hourly sweep deletes expired workflow outputs. The second puts the due metric refreshes on the worker's pg-boss queue, archives expired memories, declines memory proposals that nobody reviewed in 14 days, and commits the team and personal memory layers to the org config repository. The sweep each minute checks the behavior thresholds (`OPENNEKO_BEHAVIOR_MAX_CP_CALLS_PER_RUN_10M`, `OPENNEKO_BEHAVIOR_MAX_ACTIONS_PER_HOUR`, `OPENNEKO_BEHAVIOR_MAX_MEMORY_WRITES_PER_HOUR`, scaled by `OPENNEKO_PROFILE`) and sends the `security.behavior_threshold` event through the worker. It then runs each due condition watcher and puts its workflow on the worker's pg-boss `workflow_run_fire` queue. When the `river` schema is missing, `openneko web` still starts and says that background jobs are off. `openneko web --jobs=false` (or `OPENNEKO_WEB_JOBS=false`) runs no job; another `openneko web` with jobs on must run them.
- `scripts/dev-env.sh` holds the host settings. Each service a host process calls has a loopback port; `pnpm dev:web` and `pnpm dev:worker` source the file.
- The customer GraphJin runs in sources mode from `db/graphjin/dev.sources.example.yml`. Its config lives in `.openneko/dev/graphjin`, and the worker manages it like the core stack.
- OpenNeko state uses your XDG config home. Set `OPENNEKO_DEV_CONFIG_HOME=$PWD/.openneko/dev/config` to keep a worktree's stack separate, and a distinct `COMPOSE_PROJECT_NAME` with its ports when two stacks run at once.

Without the `openneko-serve` container, subscription-chained workflows stay silent (`subscription manager ready (0 active)` in worker logs).

Install host-only CLIs the worker shells out to (Docker images already include them):

```bash
./scripts/install-clis.sh
```

Installs the GraphJin CLI.

In the dev flow, use `http://localhost:8080` for customer-data GraphJin in the setup wizard.

### Working on the openneko binary

```bash
cd apps/openneko
go test ./... -count=1
go test -tags=integration -count=1 -timeout 10m ./internal/db/...   # pgvector via testcontainers
go build -o /tmp/openneko ./cmd/openneko
```

Sync embedded migrations after editing `db/migrations/`:

```bash
apps/openneko/scripts/sync-migrations.sh
```

CI runs this with `--check` and fails on drift.

## Troubleshooting

**Docker not running.** Start Docker Desktop or the daemon; re-run `openneko start`.

**Port in use.** Re-run setup with `--port`, or use the `OPENNEKO_PORT`
environment override.

**Image pull `unauthorized`.** Confirm packages are public at https://github.com/orgs/open-neko/packages.

**Worker crashes on boot.** Check `openneko logs worker`. `ERR_MODULE_NOT_FOUND` for a workspace dep means an old binary — `brew upgrade openneko` (≥ 1.7.3).

**GraphJin connection fails.** Prod and demo modes use `http://graphjin:8080` (pre-filled), the GraphJin inside `openneko-serve`. Dev mode (`pnpm dev`) on the host uses `http://localhost:8080`. Run `openneko status` and read the `graphjin` line: it says whether GraphJin runs or restarts, with the reason.

**Workflow subscriptions don't fire.** Run `openneko status` and check the `subscriptions` line. The worker logs `subscription manager ready (N active)` on boot. If `N=0` while the database holds subscriptions, the subscriptions GraphJin in `openneko-serve` is unreachable. `openneko stop && openneko start` writes its login again.

**Web stops with "the database lacks N migration(s)".** `openneko web` starts
only on a database that holds every migration of its release. Run
`openneko migrate`, then start web again. If you apply the migrations by
another route, set `OPENNEKO_SKIP_MIGRATE=1`; web then starts with a warning.

**Migrate waits for "the migration lock".** Another migrator holds the lock.
The message names its process ID and application name. It ends when that
migrator ends.

**Provider key fails.** Confirm it's active, has billing/quota, and matches the provider in `/settings`.

**Stale `pnpm dev` processes hammering port 5432.** `tsx watch` processes survive `openneko stop` and reconnect with a stale password (`password authentication failed for user "neko"` in logs):

```bash
pkill -f "tsx.*apps/worker/src/index"
pkill -f "tsx.*apps/web"
openneko stop --volumes && openneko start --mode demo --detach
```

**Clean slate.**

```bash
openneko reset --all
brew uninstall openneko && brew untap open-neko/tap   # if also reinstalling
pkill -f "tsx.*apps/(worker|web)"                     # if you ever ran pnpm dev
```
