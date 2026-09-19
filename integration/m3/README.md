# M3 acceptance — 2026-09-19

The Go runtime remains independent. Optional product integration was tested in
`OpenNeko-harness-m3` on `feat/harness-m3`: product commit **`c17c9ec`**, based on
OpenNeko `643b4a8`.
Hermes remains the default; `OPENNEKO_AGENT_BACKEND=harness` explicitly selects
this read-only prototype. No changes or upgrade were applied to the user's main
OpenNeko checkout, installed CLI, or active gateway.

## Reproduce

Requires Docker, Go, the pinned OpenShell **0.0.116** CLI, an OpenNeko checkout with
this adapter and installed worker/web dependencies, a built OpenNeko agent base
image, and a GraphJin image supporting the server agent. The checked local GraphJin
image was `sha256:ec6ca55bee6ced8e4c5f75ec0183a337ead74d0317cbc5b29d4afadeac5e3f64`.
Set `GRAPHJIN_TEST_IMAGE` to an available pinned build; the default local tag is
`ongroups-graphjin:latest`. This fixture does not build GraphJin itself.

```sh
OPENNEKO_TEST_SOURCE=/absolute/OpenNeko-integration-checkout \
OPENSHELL_TEST_CLI=/absolute/openshell-0.0.116 \
./integration/m3/run.sh
```

This builds the Go/Node integration image, starts isolated metadata Postgres,
seeded business Postgres, real GraphJin and a deterministic external model service,
then exercises the real product launcher/broker and pg-boss production work handler.
The outer M2 suite still exits nonzero at its **known upstream idle-stream
cancellation failure** after M3 checks pass. Do not treat that exit as a green
transport qualification. No real model keys or paid inference are used.

For manual browser acceptance add `HARNESS_M3_WEB=1`. The script prints its owned
state directory and serves the real web app at `http://localhost:18121/work`.
Browser waiting is capped at 30 minutes; create `<printed-state>/web-done` when
finished. Services use ports 18117–18119, 18121 and broker 18123. The queue driver
runs only the real `runWorkRun` handler, not unrelated worker channel/records jobs.
The fixture does not provision the Records subsystem; its navigation can show
“Temporarily unavailable”. That is outside the GraphJin assistant path.

The external model fixture is deliberately bounded to three calls per model and
requires actual reference/trace evidence in the responder request. Reset it between
scenarios with `POST http://localhost:18118/control`, body `{"delay":0}`. Set delay
30 for cancellation. It is a test-only service with synthetic data, not an LLM
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
actions, native subagents, outer token accounting and collector export are not
qualified. Interrupted attempts fail closed. Worker death before checkpoint
retrieval remains an M4 issue. M2 cancellation/revocation qualification remains
open. A live-model quality smoke test was **not run**; deterministic external-model
fixtures establish integration correctness, not model quality.
