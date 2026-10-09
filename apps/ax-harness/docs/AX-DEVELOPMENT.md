# Ax development references

Ax publishes [Go-specific agent skills](https://axllm.dev/go/skills/) alongside
the generated `github.com/ax-llm/ax/packages/go` package. These are guides for
**developing the Harness**. They are distinct from task skills that the
Harness discovers for a running agent. Neither kind is an independent model
client: all runtime model calls still cross Harness admission, Ax routing,
OpenShell and usage accounting.

Use a guide for the part being changed, then check its example against the
**pinned Go module source**, `API.md` and a no-key fixture. The published site
can describe a newer generated build than `go.mod`. AxIR generates the Go
package and its skills together; TypeScript examples are not proof of Go
behavior. In particular, the pinned and 2026-09-27 Go builds accept
`executorModelPolicy` but do not select a new route in the live executor loop.

| Harness work | Ax Go guide | Local acceptance evidence |
| --- | --- | --- |
| Provider profiles, route aliases, fallback and credentials | [`ax-go-ai`](https://axllm.dev/go/skills/ax-go-ai/) | Assert the actual provider endpoint, key alias and model for each stage; exercise pre-content failure and broker credential replacement. An approved fallback cannot change permissions or replay a tool effect. |
| AxAgent stages, child agents and tool envelopes | [`ax-go-agent`](https://axllm.dev/go/skills/ax-go-agent/) and [`ax-go-agent-rlm`](https://axllm.dev/go/skills/ax-go-agent-rlm/) | Check actor result pairing, child budget inheritance, cancellation and a successful receipt after crash/replay. |
| Long-turn context and skill discovery | [`ax-go-agent-context`](https://axllm.dev/go/skills/ax-go-agent-context/) and [`ax-go-agent-memory-skills`](https://axllm.dev/go/skills/ax-go-agent-memory-skills/) | Force actual compaction; retain user constraints, pending approvals, evidence references and catalog identity. Discovery must stay inside the admitted tool/skill scope. |
| Usage, callbacks and diagnostics | [`ax-go-agent-observability`](https://axllm.dev/go/skills/ax-go-agent-observability/) | Compare every dispatched call with durable start/finish and usage-coverage receipts, including failure and resume. Keep ordinary telemetry content-free. |
| Budget classification | [`ax-go-typesafe`](https://axllm.dev/go/skills/ax-go-typesafe/) | Verify native Choice probabilities, a bounded request, priced pre-call reserve, fixed-budget fallback and held-out false-low outcomes. |
| Typed capability input and deterministic batch graph | [`ax-go-signature`](https://axllm.dev/go/skills/ax-go-signature/) and [`ax-go-flow`](https://axllm.dev/go/skills/ax-go-flow/) | Validate schemas at admission and provider boundaries; qualify concurrency and cancellation before making a flow part of a durable workflow. |
| Offline quality improvements | [`ax-go-agent-optimize`](https://axllm.dev/go/skills/ax-go-agent-optimize/) and [`ax-go-playbook`](https://axllm.dev/go/skills/ax-go-playbook/) | Optimize against held-out, receipt-verified outcomes. Version and review any promoted artifact; no live self-modifying prompt or policy. |

The Ax usage observer is process-wide and best-effort: a cancelled or partially
consumed stream may never report usage. Its callback runs on the request path.
It can feed a bounded asynchronous telemetry queue, but the Harness's
invocation-scoped rate limiter and durable journal remain authoritative for
call admission, estimated charges and recovery. Likewise, Ax runtime callbacks
may observe or steer a run, while a state change affecting subsequent turns
must be committed by the Harness before the model sees it.

Do not install the whole catalog into a running agent or copy these guides into
customer task skills. Select the relevant guide during implementation. Audio,
multimodal refinement and optimizer skills become relevant only when a
corresponding admitted capability and outcome evaluation exist.
