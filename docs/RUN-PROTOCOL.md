# Headless run protocol v1

Build `go build -o bin/harness ./cmd/harness`. The trusted host supplies
`HARNESS_MODEL_URL`, `HARNESS_MODEL` and `HARNESS_MODEL_API_KEY`; inside OpenShell
use its injected placeholder. Provider types never enter the run contract.

One bounded JSON object on stdin followed by EOF:

```json
{"version":1,"run_id":"run-1","input_id":"input-1","prompt":"Explain the supplied task"}
```

Unknown fields, blank/oversized values and trailing input are rejected. Stdout is
ordered NDJSON: `run.started`, Ax `span.started`/`span.finished`, optional
`tool.started`/`tool.finished`, and `run.finished`. Events carry version, run/input
IDs and sequence; spans have local parent IDs; host tool operations have stable local
IDs. Result status is `completed`, `failed` or `cancelled`; completed results have
an `answer`, `clarification`, `refusal`, `partial` or `approval` kind. Failed lookup transport
cannot produce an unqualified `answer` kind. Raw delegated envelopes retain source,
evidence, status, trace ID and usage. Tool data and final answers are **content**,
not content-free telemetry. Raw Ax attributes, reasoning and upstream errors are
excluded from lifecycle observations.

Limits: 64 KiB prompt/answer, 128 KiB stdin, two-minute run deadline, eight Ax actor
steps, four shared host operations, 8 KiB lookup instruction, 256 KiB lookup response,
zero configured validation/infrastructure retries. Goja has its default five-second
CPU execution bound. The adapter exposes `lookup(instruction)`. A trusted host may additionally install
`propose({action, arguments, summary})`; no actor shell, file, arbitrary HTTP or
direct mutation capability is installed.

`HARNESS_STATE_DIR` enables atomic, fsynced, bounded 8 MiB checkpoints in a trusted
consumer-scoped directory. The process locks the hashed run ID, persists accepted
input before model work and operation intent before lookup, then saves results
before publishing events. Completed duplicate input replays events without model
or tool calls. Conflicting input and concurrent execution are rejected. Interrupted
attempts retain completed read evidence but require reconciliation; they never
silently execute again. Directory retention and encryption belong to the host.
The trusted host can repair missing results with `harness-inspect --reconcile`
and matching durable receipts, without executing callbacks. This closes the lost
result window but does not resume the interrupted Ax program or synthesize an answer.
A trusted host may explicitly request `HARNESS_RESUME=1` with the exact accepted
specification and existing state directory. Unknown/unpaired operations prevent
execution. Resolved evidence seeds a new Ax attempt; exact matching lookups reuse
that evidence. `run.resumed` records the attempt before model work, and `tool.reused`
references the prior operation. Attempts are capped at three and dispatched
lookups at four across the run; event sequences and span IDs continue monotonically.
This per-run checkpoint is not an arbitrary-crash continuation engine.

SIGINT/SIGTERM cancel admission and ignore late results. Broken sinks cancel work;
a terminal event cannot be delivered to a broken sink. Sinks must return promptly.
Exit codes: 0 completed, 1 runtime/cancellation/delivery failure, 2 CLI input or
configuration failure. OpenShell local cancellation does **not** prove remote work
has stopped; the upstream idle-stream issue remains open in [OPENSHELL.md](OPENSHELL.md).

The optional `adapters/openneko/cmd/harness` binds a trusted broker URL/token/source
from environment and uses `/v1/harness/lookup`. The runtime attaches its numeric
operation ID; the model supplies only instructions. The broker saves a bounded
intent/result in PostgreSQL around the existing server-side GraphJin delegation.
Repeated IDs never dispatch again: completed records require host recovery, and
unfinished records report an unknown outcome. The legacy `/v1/graphjin/agent`
route remains available to existing clients.
The broker resolves actor and tenant from the authenticated run and checks that the
server agent is read-only. OpenNeko stages/retrieves checkpoints around cold sandbox
execution; a worker kill before retrieval is outside the M3 recovery claim.

OpenNeko translates tool results through its existing GraphJin usage normalizer:
aggregate remote usage is counted once, never summed again with nested actor usage.
Outer Ax token accounting and collector export remain unqualified and are explicitly
reported as incomplete. Ax stage timings and remote trace IDs are retained locally.
See [M3 acceptance](../integration/m3/README.md) for actual product evidence.

For non-executing recovery evidence, build `cmd/harness-inspect` and supply the
same trusted input and `HARNESS_STATE_DIR`, without model credentials. It rejects
active locks and inconsistent snapshots and reports terminal/interrupted/unknown
operation outcomes. The `can_resume` boolean and optional `next_attempt` use the
same operation-pairing and attempt-budget checks as `session.Resume`. Terminal,
unknown, and exhausted checkpoints have `can_resume: false`. This is checkpoint
eligibility, not host authorization or proof that a different checkpoint copy is
current. It never resumes an agent. The optional OpenNeko host uses terminal inspection
to adopt a durable receipt under a host launch lock; see [recovery](M4-RECOVERY.md).


With broker lookups configured, the Goja step deadline is 60 seconds because it includes time spent inside host
callbacks; the broker lookup deadline is 45 seconds and a complete attempt is
bounded to two minutes. The SDK's default five seconds is insufficient for real
GraphJin investigations. A slow-callback regression verifies that evidence from a
lookup taking more than five seconds reaches the responder. This is a wall-clock
step limit, not separate CPU accounting for JavaScript.

## Optional approval capability

`command.MainWithTools` installs typed callbacks without an OpenNeko dependency.
The OpenNeko launcher binds a `harness-governed` broker token only for a Work run
with held pack actions. It passes their exact kinds in
`OPENNEKO_HARNESS_ACTION_KINDS`; otherwise it uses `harness-read-only` and the Go
runtime does not install `propose`. A forged or unlisted kind is denied before
broker dispatch, and the broker independently rechecks current authorization.
Model input cannot select this profile, credentials, identity or an approval state.

A proposal contains an action name (128 bytes), object arguments and a summary
(1000 bytes), bounded to 64 KiB total. The broker resolves the installed ready pack
contract, checks its schema, current actor entitlement and policy, and uses the
existing worker preflight and approval store. Even an auto-allow policy creates a
human approval request. The shared operation journal binds the exact proposal
input and tool name before dispatch; the result is durable before delivery.

Receipts are either `{id,status:"pending_approval"}` or
`{status:"denied",reason}`. They never claim an effect executed. Pending receipts
produce result kind `approval`; `completed` still means the model turn ended.
Approval IDs are projected into product cards by the trusted launcher, including
terminal recovery. Saved proposal receipts are immutable and reused on bounded
continuation; human decisions and effect receipts are separate host records.

The existing action queue dispatches approved Harness actions through a dedicated
claim in `action_execution`. It rechecks actor, approver, policy, installed contract
and frozen arguments. A PostgreSQL owner lock fences live executions and a unique
index preserves the claim across death. A stable host idempotency key is supplied
to the adapter. Optional adapter `reconcile` reads provider status by that key;
missing/failed status never authorizes redispatch. Without a receipt the product
records an explicit unknown outcome. Successful receipts and terminal action
status commit together; repeated delivery returns the receipt without an effect.

Apply OpenNeko migrations 0084–0089 before deploying matching worker and Harness
images. Drain old workers first. Hermes keeps its existing execution path.
