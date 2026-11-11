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

The overlay adds the `openneko-sandbox` service and sets
`OPENNEKO_SANDBOX_URL=unix:///run/openneko/sandbox.sock` for web, worker and
`openneko-serve`. Set `OPENNEKO_SANDBOX_KEY` in the shell or in `.env` to
require a key on the socket as well.

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
  `PATH` and `NODE_PATH` come from the service's own environment. The last
  four are paths in the run's folder.
- When a command ends, the sandbox kills every process of the run's user.
  Ending a run removes its folder and its home. At start, the sandbox
  removes the run folders of an earlier start.
- In the overlay, the service sits on its own network. Commands reach the
  internet but no service of the stack.

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
| `GET /v2/check` | | `{"status":"ok"}` with the key, 401 without it. The status page uses it. |
| `POST /v2/runs/{run}/exec` | `fresh` (true on the run's first command), `root`, `command`, `dir`, `skill_dir`, `env`, `timeout_seconds` (1 to 3600), `max_output` (1,000 to 1,000,000), `dirs`, `put` (`path`, `mode`, `data` in base64), `delete` | `exit_code`, `output`, `timed_out`, `truncated`, `dirs`, `changed` (`path`, `mode`, `data`), `deleted`, `skipped` (`path`, `reason`) |
| `DELETE /v2/runs/{run}` | | 204 |

`{run}` is `run-` and 8 to 64 letters or digits. Every path in the body
and the answer is relative to the run's folder, with `/` between parts.
`dir`, `skill_dir` and the `env` values must stay inside the run;
`PYTHONPATH` takes a `:` list. With `OPENNEKO_SANDBOX_KEY` set, every
request except health needs `Authorization: Bearer <key>`. `--listen` takes
`unix:///path` (default `unix:///run/openneko/sandbox.sock`) or
`host:port`. A TCP listener needs a key.

## Another host

The sandbox can run on another host. Start it with a TCP listener and a key,
put TLS in front of it, and set `OPENNEKO_SANDBOX_URL` to
`https://host:port` and `OPENNEKO_SANDBOX_KEY` to the key in OpenNeko's
`.env`.

## Limits

- Skill scripts cannot query GraphJin through `neko-query` from the sandbox.
  The data socket stays in the runner.
- A run's folder lives in the sandbox until the run ends. Plan disk space
  for the largest run folder times the runs at once.

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
