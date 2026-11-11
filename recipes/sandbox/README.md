# Sandbox service

This recipe runs the agent's terminal commands, the script skills and
workflow script steps away from OpenNeko. OpenNeko sends it a command; it runs
the command and returns the exit code and the output. It holds no model key
and no OpenNeko secret.

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

The image carries Node, Python, Poppler, Tesseract, and the Python and npm
packages the bundled skills use. It leaves out the optional native canvas
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
- A command runs in the run's folder, which must sit under
  `OPENNEKO_SANDBOX_WORKSPACES`. POSIX ACLs give the run's group its own
  staged folder and nothing else. A runs folder without ACL support is
  refused.
- A command's environment holds only `PATH`, `HOME`, `TMPDIR`, `LANG`,
  `NODE_PATH`, `SKILL_DIR`, `RUN_DIR`, `ARTIFACT_DIR` and `PYTHONPATH`.
  `PATH` and `NODE_PATH` come from the service's own environment. The last
  four must be paths inside the runs folder.
- Ending a run kills its processes, removes its home and takes its group off
  the staged folder.
- In the overlay, the service sits on its own network. Commands reach the
  internet but no service of the stack.

## API

| Request | Body | Answer |
|---|---|---|
| `GET /v1/health` | | `{"status":"ok"}`, no key needed |
| `POST /v1/runs/{run}/exec` | `workspace`, `command`, `skill_dir`, `env`, `timeout_seconds` (1 to 3600), `max_output` (1,000 to 1,000,000) | `exit_code`, `output`, `timed_out`, `truncated` |
| `DELETE /v1/runs/{run}` | | 204 |

With `OPENNEKO_SANDBOX_KEY` set, every request except health needs
`Authorization: Bearer <key>`. `--listen` takes `unix:///path` (default
`unix:///run/openneko/sandbox.sock`) or `host:port`. A TCP listener needs a
key.

## Limits

- The service reads and writes the run's files in place, so it must share the
  runs folder with OpenNeko. It runs on the same host.
- Skill scripts cannot query GraphJin through `neko-query` from the sandbox.
  The data socket stays in the runner.

## Test

```sh
(cd recipes/sandbox && go test ./...)
(cd recipes/sandbox/skills && npm ci && npm test)
```

The tests that switch users and set ACLs run only as root, for example in a
container:

```sh
(cd recipes/sandbox && CGO_ENABLED=0 go test -c -o /tmp/sandbox.test .)
docker run --rm --tmpfs /tmp:exec,mode=1777 -v /tmp/sandbox.test:/sandbox.test:ro \
  debian:bookworm-slim /sandbox.test -test.v
```
