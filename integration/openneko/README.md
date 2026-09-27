# OpenNeko consumer acceptance

The Go runtime remains independent. Optional product integration was tested in
`OpenNeko-harness-m3` on `feat/openneko-harness`: initial product commit **`c17c9ec`**, based on
OpenNeko `643b4a8`.
Hermes remains the default; `OPENNEKO_AGENT_BACKEND=harness` explicitly selects
this read-only prototype. No changes or upgrade were applied to the user's main
OpenNeko checkout, installed CLI, or active gateway.

## Reproduce

Requires Docker, Go, the pinned OpenShell **0.0.116** CLI, an OpenNeko checkout with
this adapter and installed worker/web dependencies, a built OpenNeko agent base
image, and a GraphJin image supporting the server agent. The checked local GraphJin
image was `sha256:ec6ca55bee6ced8e4c5f75ec0183a337ead74d0317cbc5b29d4afadeac5e3f64`.
The default is `ghcr.io/open-neko/neko-graphjin:v3.5.6`; set
`GRAPHJIN_TEST_IMAGE` to another available build if needed. This fixture does
not build GraphJin itself.

```sh
OPENNEKO_TEST_SOURCE=/absolute/OpenNeko-integration-checkout \
OPENSHELL_TEST_CLI=/absolute/openshell-0.0.116 \
./integration/openneko/run.sh
```

This builds the Go/Node integration image, starts isolated metadata Postgres,
seeded business Postgres, real GraphJin and a deterministic external model service,
then exercises the real product launcher/broker and pg-boss production work handler.
The outer M2 suite reports the accepted idle-stream cancellation defect as a
warning, and still requires sandbox deletion to close upstream work. No real
model keys or paid inference are used.

Set `HARNESS_M3_API_HTTP=1` on the command above to add the public HTTP
submission, status-poll and download check through the Go/OpenShell/GraphJin
batch worker. It starts one temporary Next dev process and cleans it up.

For manual browser acceptance add `HARNESS_M3_WEB=1`. The script prints its owned
state directory and serves the real web app at `http://localhost:18121/work`.
Before printing `M3_WEB_READY`, it requires `M5_WEB_ARTIFACT_PASS`: the live
Work route must return this queue run's exact CSV bytes with attachment headers
and reject an unissued filename. Chromium also opens the queued card's Work
thread and verifies its text appears exactly once before and after page reload.
The browser artifact link remains a separate visual check.
Browser waiting is capped at 30 minutes; create `<printed-state>/web-done` when
finished. Services use ports 18117–18119, 18121 and broker 18123. The queue driver
runs only the real `runWorkRun` handler, not unrelated worker channel/records jobs.
The fixture does not provision the Records subsystem; its navigation can show
“Temporarily unavailable”. That is outside the GraphJin assistant path.

The external model fixture defaults to three calls per model and
requires actual reference/trace evidence in the responder request. Reset it between
scenarios with `POST http://localhost:18118/control`, body `{"delay":0}`. Set delay
30 for cancellation. `{ "continue": true }` preserves counters and permits one
additional three-stage Harness attempt, requiring recovered evidence on its first
request. `{ "pause_responder": true }` pauses the first Harness responder for the
process-kill recovery test. It is a test-only service with synthetic data, not an LLM
quality test or a production endpoint.

## Observed results

- **Real launcher/broker/GraphJin/database:** returned seeded `REF-42`; a new sandbox
  replayed the same completed checkpoint without another model/lookup request.
- **Authorization:** no token returned 401; a valid run token with forged org/actor
  fields and an unavailable source did not gain data access.
- **Queue worker:** pg-boss dispatched the production `runWorkRun` handler; run
  `fac85ae0-0166-4b25-be12-d4feffb70a9e` completed through the real sandbox/broker.
- **Browser:** submitted a known-answer prompt, saw “OpenNeko is working”, reloaded
  during execution, and recovered “The reference is REF-42.” from persisted state.
- **Failure:** exhausted model fixture produced a durable `model_http_400` failure
  and the existing technical-details error presentation.
- **Cancellation:** Stop persisted run `9f5cbfa5-29af-4e7b-b8bc-9376011c6b71` as
  `cancelled`; later model work did not reopen it. Reload preserved the terminal
  state. Existing UI uses the generic incomplete-run presentation for cancellation.
  This verifies local cancellation, **not** upstream remote-work termination.
- **Refusal:** temporarily setting the test GraphJin agent to non-read-only caused
  the real broker to refuse delegation. Browser showed “The lookup was refused
  because the data agent is not configured read-only.” Configuration was restored.
- **Final telemetry:** browser run `d8d74935-6c6e-4e95-9266-a3cb6189a7d6` persisted
  18 Go events and one lookup; remote trace `agent-1789823794903637482`. Product
  observations counted one tool and one delegation. GraphJin aggregate usage was
  **30 input + 30 output = 60 tokens**, counted once despite nested usage entries.
  Coverage correctly remained `partial` because outer Ax token usage is not yet
  projected. Product inference counts represent outer/inner operations, not the
  exact number of HTTP calls made by each agent.

A clean-stack rerun also passed launcher/authorization/replay and queued run
`7f190d89-5c79-403e-af78-07d2da7bae15`, then failed only the documented M2
upstream-cancellation check. Owned test containers were removed.

On 2026-09-26, the cumulative suite exited 0 with `M5_QUEUE_UPLOAD_PASS`.
The production queue handler staged a synthetic `lead.csv` in the current
thread's upload root. Go/Ax called `upload_search` and `upload_read` inside the
OpenShell sandbox, recovered `LEAD-42`, found no path for a sibling thread's
`OTHER-SECRET` file, and made no GraphJin operation for that run. Existing Hermes,
approval, worker-death and sandbox-teardown checks passed. The idle-proxy
cancellation warning remained visible. The test projects, networks and volumes
were removed; no demo stack or installed OpenShell CLI was changed.

A second 2026-09-26 rerun exited 0 with `M5_QUEUE_ARTIFACT_PASS`. The Go file
tools used only the run's artifact directory, wrote `result.csv` inside OpenShell,
and the production queue handler recovered its exact bytes and emitted one Work
artifact event. A sibling run's marker was absent from file search. The existing
web download-route tests passed separately (20 assertions across authorization
and file handling). A later isolated rerun opened the artifact thread at
`http://localhost:18121/work/6f3dd1d7-5e79-43ce-a8ac-279ec3b57145`,
rendered and clicked its `result.csv` link, and observed HTTP 200. Fetching that
same link returned the exact `lead_id\nLEAD-42\n` bytes as a CSV attachment.
Use `localhost` for the Next dev origin; `127.0.0.1` blocks its dev resources.
The large validated Daily Lead Union batch artifact remains an M5b gate.

On 2026-09-27, the connected suite with `HARNESS_M3_API_HTTP=1` also passed
`M5_QUEUE_PROCESS_PASS`, `M5_WEB_PROCESS_PASS`, and
`M5_BROWSER_PROCESS_ARTIFACT_PASS`. A production Work queue run admitted the
opt-in `process_run` tool through the run-bound broker, staged its synthetic
thread upload, executed a bounded script in a separate OpenShell sandbox with
no broker/provider credentials or network access, and published one validated
CSV and one durable operation receipt. The Work download route returned its
exact bytes with attachment headers and rejected an unissued filename.
Chromium found the artifact link exactly once before and after reload and
downloaded the same bytes. The isolated suite removed its containers and web
process afterward. This is synthetic acceptance, not a real GraphJin database
or Daily Lead Union execution.

With `HARNESS_M3_API_HTTP=1`, the isolated suite starts the real Next API
alongside the worker. On 2026-09-27 it passed
`M5_HTTP_API_BATCH_GRAPHJIN_PASS`: bearer-authenticated `POST` returned `202`,
the pg-boss worker ran the Go query-to-file executor through OpenShell and a
seeded GraphJin broker, and authenticated status and artifact `GET` returned a
completed run and the exact 19-byte CSV. The API download filename is
`workflow-<runId>.csv`; the internal artifact remains `references.csv`.
The web process and worker read the same versioned executor registry, and a
separate live worker check retains a queued v1 revision after v2 becomes active.
This fixture uses synthetic GraphJin data and no live model key.
The isolated browser also rendered the completed API workflow run and its
Download CSV link. Clicking it returned HTTP 200, and the same route returned
the exact `reference\r\nREF-42\r\n` bytes with attachment headers. The live
worker regression checks that one claim records one queue attempt and that the
no-model batch path reports zero tokens and complete usage coverage.

An additional isolated rerun passed `mcp_library_search` through the real
OpenNeko bridge, scoped broker, OpenShell worker and Go/Ax checkpoint. A later
rerun resolved `TERMS-42` from a seeded pgvector row through the real library
search and run-entitlement code, using a deterministic embedding response. Its
temporary organization is removed before the queued Work probe. Uploaded-document
and browser search workflows still need acceptance.

Automated verification includes Go race tests/vet, interrupted-read evidence
retention, completed-input replay, typed remote statuses, protocol bounds,
real HTTP cancellation, and 96 product backend/telemetry/launcher regressions
including unchanged Hermes spawn and ACP suites, plus worker typecheck.

## Product changes and limits

The adapter adds a backend selector, fixed Go executable, trusted run identity,
read-only broker capability, an accurate restricted prompt, cold sandbox egress
rules and checkpoint retrieval. It retains Hermes's binary rules, default selection
and warm-pool path. It reuses the existing broker, actor policy and telemetry usage
normalizer. No UI rewrite or alternative queue implementation was added.

M3 qualifies the read-only internal demo only. Native provider protocols beyond
OpenAI-compatible routes, arbitrary crash continuation, mutations, workflow/Records
actions, native subagents and outer token accounting are not qualified.
Interrupted attempts fail closed. [M4 terminal recovery](../../docs/M4-RECOVERY.md)
now covers lost checkpoint transfers and cross-host recovery using PostgreSQL
ownership and receipts. The cumulative gate also redelivers a completed run through pg-boss with changed
business context, verifies no extra model/lookup calls or duplicate assistant
messages, and rejects valid mutation fences without changing effect tables.
A sequential cancellation gate traverses the real GraphJin server and observes
its model connection closing after broker-client abort. Operation records stay
unknown and prevent repeat dispatch. This does not qualify cancellation through
the separate OpenShell inference proxy.
Full operation recovery and approvals remain open.
The [expanded M2 suite](../README.md) qualifies credential rotation/detach,
OAuth refresh and gateway/Docker OTLP delivery; upstream idle-stream cancellation
still fails. A live-model quality smoke test was **not run**; deterministic external-model
fixtures establish integration correctness, not model quality.


The M4 extension now includes repaired-checkpoint continuation and a real Go
process `SIGKILL` while the responder is pending. It requires unchanged broker
receipts and no repeated GraphJin call after recovery. The 2026-09-19 browser rerun
also verified active and terminal reload with a delayed model fixture; completed
run `bbece083-d945-4a5a-a4f4-db626648210b` had exactly one user message, assistant
message and lookup. See [M4 evidence](../../docs/M4-RECOVERY.md) for the JSON receipt
ordering and Goja deadline fixes exposed by these checks, and the remaining gates.


The automated queue driver also kills its production-handler worker and broker
process after a saved lookup, lets pg-boss expire the abandoned job's 60-second
lease, and starts a replacement worker. `M4_QUEUE_WORKER_DEATH_PASS` requires the
same queue job to complete on retry 1 with one user message, one assistant message,
unchanged accepted context and lookup receipt, and no new model/GraphJin calls.
The broker runs in the killed process; the remote sandbox is left to complete its
30-second delayed responder before queue redelivery. This adds about one minute
to the suite. It covers terminal adoption after worker death, not all crash windows.

The governed-action gate includes real broker/DB proposal validation, exclusive
execution claims, process death and provider-status reconciliation. With
`HARNESS_M3_WEB=1`, the queue driver leaves a pending approval and prints
`M4_QUEUE_APPROVAL_PASS <thread> <run> <request>`. Open
`http://localhost:18121/work/<thread>` (localhost is the Next development origin).
In the same isolated database environment, run the worker script with
`--approval-worker-only` to consume the production action queue and invoke a
controlled local HTTP effect. Add `--effect-unknown` to lose the receipt after
that call. Verify approval and unknown/completed states across reload, stop the
fixture worker, then touch the printed state's `web-done` marker. These flags are
acceptance fixtures only; they cannot start against the normal database port.
