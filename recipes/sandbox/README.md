# Sandbox service

This recipe runs the agent's terminal commands, the script skills and
workflow script steps away from OpenNeko. OpenNeko sends it a command with
the run's files; it runs the command and returns the exit code, the output
and the files the command changed. It holds no model key and no OpenNeko
secret.

## Build and run beside the stack

```sh
docker compose -f compose.yml -f compose.db.yml -f recipes/sandbox/compose.yml --profile steps build
docker compose -f compose.yml -f compose.db.yml -f recipes/sandbox/compose.yml run --rm openneko-serve-init
docker compose -f compose.yml -f compose.db.yml -f recipes/sandbox/compose.yml up -d
```

The `run` line runs the start steps (storage check, migrations, config
volume, serve credentials) before `up`.

The overlay adds the `openneko-sandbox` and `openneko-egress` services and sets
`OPENNEKO_SANDBOX_URL=unix:///run/openneko-sandbox/sandbox.sock` for web and
`openneko-serve`. The socket is in its own volume, `openneko-sandbox-socket`,
which the sandbox, web and serve mount at `/run/openneko-sandbox`. The
sandbox does not mount `openneko-run`, the folder of serve's sockets. The
socket belongs to group `OPENNEKO_SANDBOX_GID` (1001) with mode 0660, so the
overlay adds web to group 1001.

Earlier releases put `sandbox.sock` in `openneko-run`. On an upgrade,
`docker compose up -d` with this overlay creates the new volume and starts
the sandbox, web and serve again with it. The old socket file stays in
`openneko-run` with nothing listening on it.

Set `OPENNEKO_SANDBOX_KEY` in the shell or in `.env` to require a key on the
socket as well.

## Run with a native install

A native install (`openneko start --native` or `openneko server-install`)
runs the sandbox with `compose.native.yml`. It starts the same two services
on the same networks, and bind-mounts the socket folder of the install:

```sh
OPENNEKO_SANDBOX_DIR=/run/openneko-sandbox \
OPENNEKO_SANDBOX_GID=$(getent group openneko-shared | cut -d: -f3) \
OPENNEKO_SANDBOX_KEY=<a long random string> \
docker compose -f recipes/sandbox/compose.native.yml up -d --build
```

A one-user native install uses
`OPENNEKO_SANDBOX_DIR=$HOME/.local/share/openneko-sandbox` and
`OPENNEKO_SANDBOX_GID=$(id -g)`. This file needs a key.

The image carries Node, Python, Poppler, Tesseract, the Python and npm
packages the bundled skills use, and the DejaVu Sans and Droid Sans Fallback
fonts. The `pdf` skill uses the fonts for ₹, arrows, Greek, Cyrillic and CJK
text. It leaves out the optional native canvas
package of `pdfjs-dist`; the `pdf` skill draws pages with Poppler. Keep it in step with `KNOWN_SKILL_DEPS` in
`packages/llm/src/work/skill-deps.ts`.

## Skills

`skills/` holds the bundled skills that run only here, one folder each, and
one `package.json` for their npm packages. OpenNeko seeds each folder into
an organization's skills, and a run stages it into the run folder. The image
installs the packages under `/opt/openneko/skills/node_modules` and sets
`NODE_PATH`, so a staged script finds them with `require`. Skill scripts are
CommonJS (`.cjs`), because `import` does not read `NODE_PATH`.

| Skill | What it does |
|---|---|
| `xlsx` | Creates, reads and edits Excel workbooks and calculates their formulas |
| `docx` | Creates, reads and edits Word documents, with tracked changes and comments |
| `pptx` | Creates, reads and edits PowerPoint decks, from a blank deck or a company template |
| `pdf` | Reads PDFs and scanned pages, creates PDF reports, fills forms, merges, splits and changes pages |
| `skill-creator` | Writes, tests and packages a new skill |

## How it keeps runs apart

- The service runs as root with every capability dropped except the ones it
  needs to switch users. Each run gets its own user and group, from
  `OPENNEKO_SANDBOX_BASE_UID` (default 20000) up to
  `OPENNEKO_SANDBOX_USERS` (default 256) runs at once.
- Each run gets its own folder under `OPENNEKO_SANDBOX_WORKSPACES` (default
  `/var/lib/openneko/sandbox-runs`), owned by the run's user, mode 0700. The
  runs folder is mode 0711, so a run cannot list it. The sandbox shares no
  folder with OpenNeko.
- A command's environment holds only `PATH`, `HOME`, `TMPDIR`, `LANG`,
  `NODE_PATH`, `SKILL_DIR`, `RUN_DIR`, `ARTIFACT_DIR` and `PYTHONPATH`.
  `PATH` and `NODE_PATH` come from the service's own environment.
  `SKILL_DIR` and the three after it are paths in the run's folder. With a
  proxy set, it also holds `HTTP_PROXY`, `HTTPS_PROXY`, `http_proxy`,
  `https_proxy` and `NODE_USE_ENV_PROXY=1` (see [Egress](#egress)).
- When a command ends, the sandbox kills every process of the run's user.
  Ending a run removes its folder and its home. At start, the sandbox
  removes the run folders of an earlier start.
- The service sits on its own network, `openneko-sandbox`, which no
  service of the stack joins. The network is internal: commands reach no
  host outside it except through the egress proxy.

## Egress

Commands reach no host by default. An admin lists the hosts that the
organization's commands may reach at Admin, Settings, Agent. The list is
empty by default.

- The `openneko-sandbox` network has `internal: true`. It has no route out,
  and its DNS answers no external name. A command that does not use the
  proxy reaches nothing outside the network.
- `openneko-egress` is the only way out. It runs the same image as
  `openneko-sandbox egress`. It joins `openneko-sandbox` and the
  `openneko-egress` network, which has a route out. It has a read-only root
  and no capabilities, and it mounts only the control volume.
- OpenNeko sends the organization's list with each command, in `egress`.
  On the first command of a run, the sandbox makes a random 32-byte token
  for the run. It gives the command the proxy URL
  `http://<run>:<token>@openneko-egress:3128` in the proxy variables.
  Ending the run removes the token, so it fails at once.
- The proxy takes `CONNECT` and plain `http://` requests. For each request
  it asks the sandbox for the run's list over the control socket,
  `/run/openneko-egress/control.sock`. Only root can open that socket, so a
  command cannot ask for a list or a token. A run cannot read another run's
  environment, because each run has its own user.
- The proxy checks the host and the port against the list before it looks
  up the name. It then resolves the name once and refuses the request when
  an address is loopback, private, link-local, carrier-grade NAT, unique
  local, multicast or the metadata address 169.254.169.254. It connects to
  the address that it checked. It answers 407 for a bad token and 403 for a
  host that the list does not allow.
- The proxy writes one JSON line per request to its log: the time, the
  run, the organization, the work run (`label`), the host, the port, the
  address, the result and the reason. Read it with
  `docker compose logs openneko-egress`.

List entries:

| Entry | Matches |
|---|---|
| `pypi.org` | `pypi.org`, port 443 |
| `files.example.com:8443` | that host, port 8443 |
| `*.example.com` | each subdomain of `example.com`, port 443; not `example.com` |
| `203.0.113.10:443` | that public address |

A list holds at most 100 entries. The admin form refuses `*`, an entry that
matches every host, and a private, loopback, link-local or metadata
address. Tools that read the proxy variables (pip, npm, curl, Python's
`urllib`, Node with `NODE_USE_ENV_PROXY`) work for a listed host. A tool
that ignores them reaches nothing.

`GET /v2/check` reports `egress`: `closed` when the sandbox's network has
no default route, else `open`. `openneko release-check --sandbox` prints it
as `sandbox_egress`, and the Sandbox card on the status page shows it.

The bundled skills, `neko_data` and declared queries need no host on the
list.

## How files travel

OpenNeko sends each command with the files of the run that changed since
the last command. The sandbox writes them into the run's folder, runs the
command, and returns the files and folders that the command made, changed
or removed. OpenNeko writes them back into its own copy of the run.

- The first command of a run carries the whole run folder. Later commands
  carry only what changed.
- OpenNeko never sends the run's `.openneko` folder, which holds the job
  file, and refuses a returned file inside it. It sends regular files and
  folders only; it does not follow links.
- The sandbox returns regular files and folders only. It names each link,
  pipe or device it does not return, once, and OpenNeko adds those names to
  the command's output.
- One command carries at most 128 MiB of files each way. A changed file
  that does not fit stays in the sandbox and travels with a later command.
- OpenNeko sends its own path for the run as `root`. The sandbox swaps it
  for the run's folder in the command, and back in the output, so commands
  can use the paths that the agent sees. File contents keep the sandbox's
  paths.
- When the sandbox does not know the run (it restarted), it answers 409.
  OpenNeko then sends the whole run folder again, once.

## API

| Request | Body | Answer |
|---|---|---|
| `GET /v2/health` | | `{"status":"ok"}`, no key needed |
| `GET /v2/check` | | `{"status":"ok","egress":"closed"}` with the key, 401 without it. `egress` is `closed` or `open`. The status page uses it. |
| `POST /v2/runs/{run}/exec` | `fresh` (true on the run's first command), `root`, `command`, `dir`, `skill_dir`, `env`, `timeout_seconds` (1 to 3600), `max_output` (1,000 to 1,000,000), `dirs`, `put` (`path`, `mode`, `data` in base64), `delete`, `egress` (`org`, `label`, `hosts`) | `exit_code`, `output`, `timed_out`, `truncated`, `dirs`, `changed` (`path`, `mode`, `data`), `deleted`, `skipped` (`path`, `reason`) |
| `DELETE /v2/runs/{run}` | | 204 |

`{run}` is `run-` and 8 to 64 letters or digits. Every path in the body
and the answer is relative to the run's folder, with `/` between parts.
`dir`, `skill_dir` and the `env` values must stay inside the run;
`PYTHONPATH` takes a `:` list. With `OPENNEKO_SANDBOX_KEY` set, every
request except health needs `Authorization: Bearer <key>`. `--listen` takes
`unix:///path` (default `unix:///run/openneko-sandbox/sandbox.sock`) or
`host:port`. A TCP listener needs a key.

`OPENNEKO_SANDBOX_EGRESS_PROXY` (`host:port`) is the proxy that commands
use; without it, commands get no proxy variables.
`OPENNEKO_SANDBOX_EGRESS_CONTROL` (`unix:///path`) is the control socket
that the proxy asks. The proxy takes `--listen` (default `:3128`) and
`--control`.

## Another host

The sandbox can run on another host. Start it with a TCP listener and a key,
put TLS in front of it, and set `OPENNEKO_SANDBOX_URL` to
`https://host:port` and `OPENNEKO_SANDBOX_KEY` to the key in OpenNeko's
`.env`.

OpenNeko cannot see the network of another host. Run the sandbox there with
`compose.native.yml`, or give it a network with no route out and run the
egress proxy beside it yourself. `openneko release-check --sandbox` prints
`sandbox_egress=open` when the sandbox has a route out, and `unknown` when
the sandbox does not report its egress (a build from before 6.0.0). The
status page then shows the Sandbox card as failing.

## Upgrade an install that runs an earlier recipe

Before 6.0.0, this recipe put the sandbox on a network with a route out,
so commands reached the internet. 6.0.0 closes that network and adds the
egress proxy. After the upgrade, commands reach no host until an admin
lists one.

1. Take the new recipe and build the image again. Compose does not build a
   new image for `up` on its own:

   ```sh
   docker compose -f compose.yml -f compose.db.yml -f recipes/sandbox/compose.yml up -d --build
   ```

   Compose makes the `openneko-sandbox` network again with
   `internal: true`, and starts `openneko-egress`. If your Compose version
   stops and says that the network must be made again, run the same files
   with `down` (no `-v`), then `up -d --build`.
2. A native install that runs the sandbox with `docker run`: stop and
   remove that container, then start `compose.native.yml` as in
   [Run with a native install](#run-with-a-native-install).
3. Run `openneko release-check --sandbox`. It must print
   `sandbox_egress=closed`.
4. At Admin, Settings, Agent, list the hosts that your commands need, for
   example a package index.

A sandbox from an earlier recipe keeps working with OpenNeko 6.0.0: it
ignores `egress`, so its commands still reach the internet, and the status
page shows the Sandbox card as failing until you upgrade it.

## Script data

A skill script reads data with `from neko_data import query` or
`$RUN_DIR/bin/neko-query`. The sandbox holds no key, no token and no
socket, and it needs no network for this. The host runs each query:

1. The script asks for rows that the run does not hold yet. `neko_data`
   writes the request to `$RUN_DIR/.neko-data/requests/` and stops the
   script with exit code 75.
2. OpenNeko runs the query with the run's GraphJin tools, under the run's
   identity, tool policy, audit and trace. It keeps the query and its rows
   in the run's record, `.openneko/script-data.jsonl`, which never goes to
   the sandbox.
3. OpenNeko writes the rows to `$RUN_DIR/.neko-data/answers/` and runs the
   same command again. The script now reads the rows from the file. A query
   that fails gives the script the error, and `query()` raises
   `NekoDataError`.

The model sees only the output of the last run. Each run has these limits:
10,000 rows and 4 MiB for one query, 50 queries, and 15 minutes of query
time. A query over a limit gives the script an error. When the run reaches
the query or time limit, OpenNeko stops and adds a line to the command's
output. A script that runs again in the same run gets the same rows from
the record.

### Declared queries

A skill script can name its queries in a file beside it. The file name is
the script's name with `.queries.json` added: `report.py` reads its queries
from `report.py.queries.json`.

```json
{
  "queries": {
    "orders": {
      "query": "query ($since: String!, $limit: Int) { orders(where: {created_at: {gte: $since}}, limit: $limit) { id total } }",
      "params": {
        "since": {},
        "limit": { "type": "integer", "default": 100 }
      }
    },
    "stock": { "query": "query { stock { sku on_hand } }", "namespace": "shop" }
  }
}
```

- `queries` holds 1 to 20 queries. A query name uses lower-case letters,
  digits and `_`.
- `params` become the GraphQL variables. The type is `string` (the default),
  `integer`, `number` or `boolean`. The script's command line gives a value
  with `--since 2026-11-01` or `--since=2026-11-01`. A parameter with no
  value takes its `default`; with no default, the script does not start.
- `namespace` is optional. It picks the GraphJin namespace.

When a command starts a script with this file, OpenNeko runs each query
before the script starts. It uses the same tools, policy, audit, record and
limits as `neko_data`. It writes each data object to
`$RUN_DIR/.neko-data/queries/<name>.json`. If a query fails, for example
because the run's policy refuses it, the script does not start, and the
command's output names the query and the reason. One command may start one
script with declared queries. A script with no such file runs as before.

Outside OpenNeko, set `NEKO_DATA_GRAPHJIN_URL` to a GraphJin GraphQL URL
(for example `http://localhost:8080/api/v1/graphql`), and `neko_data` sends
each query there.

## Limits

- A run's folder lives in the sandbox until the run ends. Plan disk space
  for the largest run folder times the runs at once.
- A token is a bearer secret for the life of its run. A command that holds
  it can pass it to a child process of the same run. No other run can read
  it.
- The proxy filters by host and port. It does not read the traffic inside
  a `CONNECT` tunnel.

## Test

```sh
(cd recipes/sandbox && go test ./...)
(cd recipes/sandbox/skills && npm ci && npm test)
```

The tests that switch users and set ACLs run only as root, for example in a
container:

```sh
(cd recipes/sandbox && CGO_ENABLED=0 go test -c -o /tmp/sandbox.test .)
docker run --rm --init --tmpfs /tmp:exec,mode=1777 -v /tmp/sandbox.test:/sandbox.test:ro \
  debian:bookworm-slim /sandbox.test -test.v
```

`egress-proof.sh` proves the egress controls against real containers. It
starts the recipe's compose files with a built image, checks from inside a
run what a command can reach with an empty list and with a short list, and
runs the `neko_data` and declared-query tests against a sandbox on the
closed network:

```sh
docker build -t openneko-sandbox:local recipes/sandbox
recipes/sandbox/egress-proof.sh openneko-sandbox:local
```

It needs Docker, Go and the test database of `apps/openneko`
(`OPENNEKO_TEST_DATABASE_URL`).
