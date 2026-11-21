# Sandbox service

This recipe runs the agent's terminal commands, the script skills and
workflow script steps away from OpenNeko. OpenNeko sends it a command with
the run's files; it runs the command and returns the exit code, the output
and the files the command changed. It holds no model key and no OpenNeko
secret.

## Run beside the stack

Each release publishes this image as `ghcr.io/open-neko/neko-sandbox`, for
amd64 and arm64, with the release's tag. Both compose files name it with
`${OPENNEKO_VERSION}` and have no `build:` line. Set `OPENNEKO_VERSION` to
the release tag, for example `v6.0.0`; without it, the image name is not
valid and the pull stops with a line that asks for it.
`openneko start` and `openneko upgrade` set it.

```sh
export OPENNEKO_VERSION=v6.0.0
docker compose -f compose.yml -f compose.db.yml -f recipes/sandbox/compose.yml --profile steps build
docker compose -f compose.yml -f compose.db.yml -f recipes/sandbox/compose.yml run --rm openneko-serve-init
docker compose -f compose.yml -f compose.db.yml -f recipes/sandbox/compose.yml up -d
```

To run a source build of the sandbox, build the image and name it in
`OPENNEKO_SANDBOX_IMAGE`. The compose files then use that image in place of
the published one, and `OPENNEKO_VERSION` is not needed:

```sh
docker build -t openneko-sandbox:local recipes/sandbox
export OPENNEKO_SANDBOX_IMAGE=openneko-sandbox:local
```

Build it again after each change to this folder.

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
on the same networks, and bind-mounts the socket folder of the install. The
native release archive holds the file as `share/openneko/sandbox/compose.native.yml`,
so you need no checkout. Run it from that folder with the release's tag:

```sh
OPENNEKO_SANDBOX_DIR=/run/openneko-sandbox \
OPENNEKO_SANDBOX_GID=$(getent group openneko-shared | cut -d: -f3) \
OPENNEKO_SANDBOX_KEY=<a long random string> \
OPENNEKO_VERSION=v$(openneko version --short) \
docker compose -f compose.native.yml up -d
```

A one-user native install uses
`OPENNEKO_SANDBOX_DIR=$HOME/.local/share/openneko-sandbox` and
`OPENNEKO_SANDBOX_GID=$(id -g)`. This file needs a key.

## Run with Docker Desktop on macOS or Windows

Docker Desktop runs the containers in a Linux VM. A Unix socket on a bind
mount does not reach the host, and an internal network cannot publish a
port. Use `compose.desktop.yml` there. It starts the sandbox and the egress
proxy as `compose.native.yml` does, with the sandbox on TCP port 8091 of its
internal network. A third service, `openneko-sandbox-publish`, joins that
network and a network of its own, and publishes port 8091 on `127.0.0.1`
only. It passes TCP to the sandbox and nothing else. It runs as user 65534
with no capabilities. The runs' firewall drops their packets to it.

```sh
OPENNEKO_SANDBOX_KEY=<a long random string> \
OPENNEKO_VERSION=v$(openneko version --short) \
docker compose -f compose.desktop.yml up -d
```

Then start OpenNeko with `OPENNEKO_SANDBOX_URL=http://127.0.0.1:8091` and
the same key. `OPENNEKO_SANDBOX_PORT` changes the host port.

`prove-desktop.sh` starts the recipe under its own project name and prints
one PASS or FAIL line per check. It checks that the port is on 127.0.0.1
only, that a request with no key or a wrong key gets 401, that the sandbox
reports its egress closed and runs a command, and that neither the sandbox
container nor a command in it reaches a public address, a public name, DNS
on 8.8.8.8, the metadata address or the Docker host. A control container on
an open network must reach each target first, so that a blocked result
counts. It also checks that a command cannot reach the publish container,
the egress proxy directly or the sandbox's own port. It needs bash, curl and
Docker. With no argument, it builds the image from this folder:

```sh
recipes/sandbox/prove-desktop.sh
OPENNEKO_BIN=$(command -v openneko) recipes/sandbox/prove-desktop.sh ghcr.io/open-neko/neko-sandbox:v6.0.0
```

`OPENNEKO_BIN` adds `openneko release-check --sandbox` against the
published port. The last line is `PASS all checks` or `FAIL one or more
checks`, and the exit code is 0 or 1.

The image carries Node, Python with PyYAML, Poppler, Tesseract, the npm
packages the bundled skills use, and the DejaVu Sans and Droid Sans Fallback
fonts. It has no Python document libraries (pypdf, python-docx, python-pptx,
openpyxl): no bundled skill uses them, and the agent prompt does not name
them. Keep the libraries section of the prompt in step with the image. The `pdf` skill uses the fonts for ₹, arrows, Greek, Cyrillic and CJK
text. It leaves out the optional native canvas
package of `pdfjs-dist`; the `pdf` skill draws pages with Poppler. Keep it in step with `KNOWN_SKILL_DEPS` in
`packages/llm/src/work/skill-deps.ts`.

The image holds only what the skills and the service use, plus `jq`,
`ripgrep` and `file` for the agent's shell. It has no compiler, no pip, no
npm and no git, so a command cannot install a package at run time. It is
about 522 MB unpacked. The published image is 131 MiB compressed on amd64
and 130 MiB on arm64; the release fails above 132 MiB
(`scripts/image-size-budgets.json`).

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
  needs to switch users, and `NET_ADMIN` and `SETPCAP` for its firewall at
  start (see [Egress](#egress)). Each run gets its own user and group, from
  `OPENNEKO_SANDBOX_BASE_UID` (default 20000, at least 1000) up to
  `OPENNEKO_SANDBOX_USERS` (default 256, 1 to 4096) runs at once. The
  firewall covers the same users and the probe user after them. The
  sandbox does not start with another value.
- Each run gets its own folder under `OPENNEKO_SANDBOX_WORKSPACES` (default
  `/var/lib/openneko/sandbox-runs`), owned by the run's user, mode 0700. The
  runs folder is mode 0711, so a run cannot list it. The sandbox shares no
  folder with OpenNeko.
- A command's environment holds only `PATH`, `HOME`, `TMPDIR`, `TEMP`, `TMP`, `LANG`,
  `NODE_PATH`, `SKILL_DIR`, `RUN_DIR`, `ARTIFACT_DIR` and `PYTHONPATH`.
  `PATH` and `NODE_PATH` come from the service's own environment.
  `SKILL_DIR` and the three after it are paths in the run's folder. With a
  proxy set, it also holds `HTTP_PROXY`, `HTTPS_PROXY`, `http_proxy`,
  `https_proxy` and `NODE_USE_ENV_PROXY=1` (see [Egress](#egress)).
- When a command ends, the sandbox kills every process of the run's user.
  Ending a run removes its folder and its home. At start, the sandbox
  removes the run folders of an earlier start.
- Runs share no place to write. At start, the sandbox takes write and
  list rights from other users on `/tmp`, `/var/tmp`, `/run/lock`,
  `/dev/shm`, `/dev/mqueue` and every other folder or file that any user
  could write, and the runs and homes folders are mode 0711. A run can
  write only its own folder and its home. `TMPDIR`, `TEMP` and `TMP` point
  to `tmp` in the run's home, and each command runs with `umask 077`. A
  tool that writes to `/tmp` by a fixed path gets "Permission denied", and
  Python `multiprocessing` fails, because it needs `/dev/shm`. A private
  `/tmp` for each command needs a mount namespace and so `CAP_SYS_ADMIN`,
  which the sandbox does not take. When a run ends, the sandbox removes
  the System V shared memory, queues and semaphores of its user before the
  user goes back to the pool. Three channels stay open, because each needs
  `CAP_SYS_ADMIN` to close. A run can read the process list and command
  lines of another run in `/proc`, so keep data out of command arguments.
  Two runs that both choose to can meet on an abstract Unix socket or on a
  System V object that one of them opens to all users.
- The service sits on its own network, `openneko-sandbox`, which no
  service of the stack joins. The network is internal: commands reach no
  host outside it except through the egress proxy.

## Egress

Commands reach no host by default. An admin lists the hosts that the
organization's commands may reach at Admin, Settings, Agent. The list is
empty by default.

- At start, the sandbox sets a firewall (nftables) in its own network
  namespace for the run users and the probe user
  (`OPENNEKO_SANDBOX_BASE_UID` up to `BASE_UID + OPENNEKO_SANDBOX_USERS`).
  Their packets may go only to the sandbox's relay at `127.0.0.1:3128`,
  which passes each connection to `openneko-egress:3128`. The firewall
  drops every other address: Docker's DNS at `127.0.0.11`, the bridge
  gateway, the Docker host, the stack's services and the internet. The
  sandbox then drops `NET_ADMIN` and `SETPCAP` from every set, so neither
  it nor a program it starts can change the rules. When it cannot set the
  rules or drop the capabilities, the sandbox exits and does not serve.
- A command keeps no loopback network, not even to its own listener. A
  loopback rule cannot tell one run's listener from another's. Child
  processes still talk through pipes and Unix sockets in the run's folder.
- The `openneko-sandbox` network also has `internal: true`: no route out,
  and its DNS answers no external name. The firewall does not depend on it.
  On Docker Engine 29.9.3 (rootless), a container on an internal network
  still reaches its own bridge gateway; the firewall closes that path.
- A probe runs at start and every minute as the probe user. It looks up
  `example.com` and `metadata.google.internal`, and connects to 1.1.1.1:443,
  8.8.8.8:53, 169.254.169.254:80 and ports 22, 53, 80, 443, 2375 and 2376
  of each bridge gateway. A name that resolves, or an address that answers
  or refuses, sets the egress state to `open`.
- `openneko-egress` is the only way out. It runs the same image as
  `openneko-sandbox egress`. It joins `openneko-sandbox` and the
  `openneko-egress` network, which has a route out. It has a read-only root
  and no capabilities, and it mounts only the control volume.
- OpenNeko sends the organization's list with each command, in `egress`.
  On the first command of a run, the sandbox makes a random 32-byte token
  for the run. It gives the command the proxy URL
  `http://<run>:<token>@127.0.0.1:3128` (the relay) in the proxy variables.
  Ending the run removes the token, so it fails at once.
- The proxy takes `CONNECT` and plain `http://` requests. For each request
  it asks the sandbox for the run's list over the control socket,
  `/run/openneko-egress/control.sock`. Only root can open that socket, so a
  command cannot ask for a list or a token. A run cannot read another run's
  environment, because each run has its own user.
- The proxy checks the host and the port against the list before it looks
  up the name. It then resolves the name once and refuses the request when
  an address is loopback, private, link-local, carrier-grade NAT, unique
  local, multicast or the metadata address 169.254.169.254, also in an IPv6
  form that carries an IPv4 address. It connects to the address that it
  checked. It answers 407 for a bad token and 403 for a host that the list
  does not allow.
- A run holds at most 32 connections through the proxy at once; the next
  one gets 429. The proxy closes a connection after 5 minutes with no
  traffic. Every 30 seconds it asks for the run's list again, and it closes
  the connection when its host left the list or its run ended.
- The proxy writes one JSON line per request to its log: the time, the
  run, the organization, the work run (`label`), the host, the port, the
  address, the result and the reason. Read it with
  `docker compose logs openneko-egress`.

List entries:

| Entry | Matches |
|---|---|
| `pypi.org` | `pypi.org`, port 443 |
| `files.example.com:8443` | that host, port 8443 |
| `*.example.com` | each subdomain of `example.com` that `example.com`'s owner holds, port 443; not `example.com` |
| `203.0.113.10:443` | that public address |

A list holds at most 100 entries. The admin form refuses `*`, an entry that
matches every host, and a private, loopback, link-local or metadata
address. The form and the proxy refuse a wildcard on a public suffix, where
unrelated owners register hosts: `*.co.uk`, `*.github.io`,
`*.s3.amazonaws.com`. A wildcard matches a host only when the host's
registered domain, by the public suffix list, is the wildcard's domain or
a parent of it. So `*.amazonaws.com` matches `sts.amazonaws.com`, and no
host under the public suffix `s3.amazonaws.com`. Tools that read the proxy variables (Python's `urllib`, Node with
`NODE_USE_ENV_PROXY`) work for a listed host. A tool
that ignores them reaches nothing.

`GET /v2/check` reports `egress`: `closed` when the firewall is in place,
the sandbox's network has no default route, and the last probe reached
nothing; else `open`. OpenNeko gives a run the terminal only on
`closed`. `openneko release-check --sandbox` prints it as `sandbox_egress`
and fails on `open`, and the Sandbox card on the status page shows it.

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
| `POST /v2/runs/{run}` | `org`, `label`, `hosts` | `{"token":"..."}`: the run's token. Only the control side (the key, or no `Authorization` on a sandbox without a key) opens a run. 409 when the run exists. |
| `POST /v2/runs/{run}/exec` | `fresh` (true on the run's first command), `root`, `command`, `dir`, `skill_dir`, `env`, `timeout_seconds` (1 to 3600), `max_output` (1,000 to 1,000,000), `dirs`, `put` (`path`, `mode`, `data` in base64), `delete`, `egress` (`org`, `label`, `hosts`) | `exit_code`, `output`, `timed_out`, `truncated`, `dirs`, `changed` (`path`, `mode`, `data`), `deleted`, `skipped` (`path`, `reason`) |
| `DELETE /v2/runs/{run}` | | 204 |

`{run}` is `run-` and 8 to 64 letters or digits. Every path in the body
and the answer is relative to the run's folder, with `/` between parts.
`dir`, `skill_dir` and the `env` values must stay inside the run;
`PYTHONPATH` takes a `:` list. With `OPENNEKO_SANDBOX_KEY` set, every
request except health needs `Authorization: Bearer <key>`. A run's token
allows only the exec and the end of that run. With the token, the run
takes the egress list, org and label that the control side opened it
with: an exec body with `egress` gets 403. When the sandbox no longer knows
the token (it restarted), it answers 401. `--listen` takes
`unix:///path` (default `unix:///run/openneko-sandbox/sandbox.sock`) or
`host:port`. A TCP listener needs a key.

`OPENNEKO_SANDBOX_EGRESS_PROXY` (`host:port`) is the proxy that the relay
passes commands to; without it, commands get no proxy variables. A sandbox
that runs as another user than root sets no firewall, runs commands as its
own user, and reports `egress` `open`.
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
the sandbox does not report its egress (a build from before 6.0.0). In both
cases release-check fails, the terminal is off, and the status page shows
the Sandbox card as failing. When a firewall on that host closes the
sandbox, set `OPENNEKO_SANDBOX_EGRESS_BY_OPERATOR=1` for web: the terminal
then runs, and the status page says that the operator enforces the egress.

## Upgrade the sandbox

On a Docker install, `openneko upgrade` pulls the sandbox image with the
stack when `compose.override.yml` names `ghcr.io/open-neko/neko-sandbox`,
and removes its older tags. A source build in `OPENNEKO_SANDBOX_IMAGE` keeps
its image; build it again from the new recipe.

On a native install, run the compose command of
[Run with a native install](#run-with-a-native-install) again with the new
`OPENNEKO_VERSION`. `openneko upgrade` prints a warning when the sandbox
still runs an older tag. Then run `docker image prune` to remove the old
image.

## Upgrade an install that runs an earlier recipe

Before 6.0.0, this recipe put the sandbox on a network with a route out,
so commands reached the internet. 6.0.0 closes that network and adds the
egress proxy. After the upgrade, commands reach no host until an admin
lists one.

1. Take the new recipe, set `OPENNEKO_VERSION` to the release tag, and pull
   the image of the release. For a source build, build it again and set
   `OPENNEKO_SANDBOX_IMAGE` as in [Run beside the stack](#run-beside-the-stack):

   ```sh
   docker compose -f compose.yml -f compose.db.yml -f recipes/sandbox/compose.yml pull openneko-sandbox openneko-egress
   docker compose -f compose.yml -f compose.db.yml -f recipes/sandbox/compose.yml up -d
   ```

   Compose makes the `openneko-sandbox` network again with
   `internal: true`, gives the sandbox `NET_ADMIN` and `SETPCAP` for its
   firewall, and starts `openneko-egress`. A `compose.override.yml` that sets
   its own `cap_add` for the sandbox must add both, or the sandbox does not
   start. If your Compose version
   stops and says that the network must be made again, run the same files
   with `down` (no `-v`), then `up -d`.
2. A native install that runs the sandbox with `docker run`: stop and
   remove that container, then start `compose.native.yml` as in
   [Run with a native install](#run-with-a-native-install).
3. Run `openneko release-check --sandbox`. It must print
   `sandbox_egress=closed`.
4. At Admin, Settings, Agent, list the hosts that your commands need, for
   example a package index. A list saved before 6.0.0 that holds a wildcard
   on a public suffix, such as `*.github.io`, no longer passes the check,
   and the organization's list reads as empty. Save the list again with the
   hosts named one by one.

A sandbox from an earlier recipe does not report `egress`. OpenNeko 6.0.0
turns the terminal off, and the status page shows the Sandbox card as
failing until you upgrade it.

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
- A run ends when OpenNeko ends it, or when no command comes for the run's
  longest command time plus 10 minutes, counted from the last command's
  start. The sandbox then kills the run's processes, removes its folder and
  home, voids its token, and gives its user back to the pool. A later
  command of that run gets 409, and OpenNeko sends the run folder again.
- The proxy filters by host and port. It does not read the traffic inside
  a `CONNECT` tunnel.

## Test

```sh
(cd recipes/sandbox && go test ./...)
(cd recipes/sandbox/skills && npm ci && npm test)
```

Without the Python document libraries, `npm test` skips the five checks
that open the output files. The `skill-test` stage of the Dockerfile adds
the libraries and runs every skill test in the image:

```sh
docker build --target skill-test -t openneko-sandbox:skill-test recipes/sandbox
docker run --rm openneko-sandbox:skill-test
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
closed network. It also puts the sandbox on a network with a route out and
checks that the firewall alone keeps every run in, checks that the sandbox
does not start without `NET_ADMIN` or `SETPCAP`, and listens on each bridge
gateway so that a blocked result there means something:

```sh
docker build -t openneko-sandbox:local recipes/sandbox
recipes/sandbox/egress-proof.sh openneko-sandbox:local
```

It needs Docker, Go, `nsenter` for rootless Docker, and the test database
of `apps/openneko` (`OPENNEKO_TEST_DATABASE_URL`). The PR checks and the
post-release smoke run `TestEgressProof` with
`OPENNEKO_EGRESS_PROOF_REQUIRED=1`, so a missing image fails the run. The
PR checks build the image from the Dockerfile. The post-release smoke
pulls the published image of the release and runs the stack with it.
No minimum Docker Engine version applies; the kernel needs nftables
(`nf_tables`), which every supported Linux has.
