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
IDs and sequence; spans have local parent IDs; lookup operations have stable local
IDs. Result status is `completed`, `failed` or `cancelled`; completed results have
an `answer`, `clarification`, `refusal` or `partial` kind. Failed lookup transport
cannot produce an unqualified `answer` kind. Raw delegated envelopes retain source,
evidence, status, trace ID and usage. Tool data and final answers are **content**,
not content-free telemetry. Raw Ax attributes, reasoning and upstream errors are
excluded from lifecycle observations.

Limits: 64 KiB prompt/answer, 128 KiB stdin, two-minute run deadline, eight Ax actor
steps, four lookup operations, 8 KiB lookup instruction, 256 KiB lookup response,
zero configured validation/infrastructure retries. Goja has its default five-second
CPU execution bound. The adapter exposes only `lookup(instruction)`; no actor shell,
file, arbitrary HTTP, MCP or mutation capability is installed.

`HARNESS_STATE_DIR` enables atomic, fsynced, bounded 8 MiB checkpoints in a trusted
consumer-scoped directory. The process locks the hashed run ID, persists accepted
input before model work and operation intent before lookup, then saves results
before publishing events. Completed duplicate input replays events without model
or tool calls. Conflicting input and concurrent execution are rejected. Interrupted
attempts retain completed read evidence but require reconciliation; they never
silently execute again. Directory retention and encryption belong to the host.
This per-run checkpoint is not an arbitrary-crash continuation engine.

SIGINT/SIGTERM cancel admission and ignore late results. Broken sinks cancel work;
a terminal event cannot be delivered to a broken sink. Sinks must return promptly.
Exit codes: 0 completed, 1 runtime/cancellation/delivery failure, 2 CLI input or
configuration failure. OpenShell local cancellation does **not** prove remote work
has stopped; the upstream idle-stream issue remains open in [OPENSHELL.md](OPENSHELL.md).

The optional `adapters/openneko/cmd/harness` binds a trusted broker URL/token/source
from environment and uses `/v1/graphjin/agent`. The model supplies only instructions.
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
operation outcomes. It never resumes an agent. The optional OpenNeko host uses terminal inspection
to adopt a durable receipt under a host launch lock; see [recovery](M4-RECOVERY.md).
