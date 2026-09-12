# Agent startup

The agent image ships two JavaScript bundles and built-in skills. It does not
ship the worker's Node dependency tree, tsx, database libraries, or embedding
stack. Hermes and the document tools remain part of the agent image.

Each run creates a fresh OpenShell sandbox with its own provider and egress
policy. Creation runs `true`; execution starts `/app/entry.js` once. The entry
validates its bridge and skills before running the job. The same filesystem
checks run during the image build on both release architectures.

## Resource limits

`OPENNEKO_AGENT_CPUS` defaults to `2`; `OPENNEKO_AGENT_MEMORY` defaults to `1Gi`.
The launcher passes these as OpenShell `--cpu` and `--memory` creation flags.
The limits cover the sandbox's process tree, including Hermes and tool children.
The Compose overlays forward both variables to web and worker. Restart those
hosts after changing them; existing sandboxes retain their creation limits.

Ten sandboxes can consume at most 10 GiB at this default, leaving roughly 6 GiB
on a 16 GiB host for the OS and services. This is a capacity budget, not proof
that those services fit in the remainder.

A local Linux ARM64 startup probe loaded the bundled entry, initialized a
Hermes ACP session, and connected the real MCP bridge under a 1 GiB cap. Its
cgroup peak was about 395 MiB (including charged file cache). It made no model
calls and did not exercise document tools or delegation. Ten concurrently
launched copies also initialized successfully, with per-container cgroup peaks
of 233–266 MiB. Shared file-cache charging affects that comparison; it is not
a benchmark of ten active model/document workloads.

These are configurable starting limits, not measured workload peak requirements.
Size them for the document/delegation workloads and maximum concurrent turns
on the host. A memory cap is not a reservation or an aggregate admission limit.

## Timings

Launcher logs contain JSON records with `type: "sandbox_phase"`, `runId`,
`phase`, and `durationMs`. Individual operations also include `ok`:

- `stage`: copy the selected workspace and skill overrides.
- `create_upload`: OpenShell creation, image availability, policy setup,
  upload, and the initial command. Each retry has its own record.
- `exec`: execute the agent and drain streamed event handlers. This includes
  Hermes initialization, model time, tools, and the artifact scan.
- `download`: recover artifacts when present or their state is unknown.
- `delete`: destroy the sandbox and its remaining process tree.
- `total`: the full launcher call, including staging, retries, and cleanup.

Creation and upload share one CLI call, so the host does not claim separate
gateway/provision/upload measurements. Compare these logs with
`scripts/bench-hermes-acp.mjs` when investigating Hermes initialization.
Do not interpret `exec` or total lifecycle duration as time to first answer.

## Artifact recovery

After the agent finishes, including an ordinary agent error, the entry reports
whether the artifact tree contains files or links. Only an explicit empty-tree
report skips downloading. Old images, crashes, timeouts, or unreadable trees
keep the existing recovery attempt. Background jobs and aborted runs retain
their existing no-download behavior.

## Verification

```sh
pnpm --filter @neko/worker exec vitest run test/agent-sandbox
pnpm --filter @neko/llm exec vitest run test/sandbox-launcher.test.ts test/hermes-install-contract.test.ts test/workflow-api-contract.test.ts
docker build --target agent -t openneko-agent:startup .
```

The bundle test builds both executables into an isolated temporary directory,
rejects host DB/records/telemetry and embedding imports, and checks boot with
only the bundled assets and no inherited runtime overrides. Missing assets
must fail. Launcher tests cover resource limits, cancellation, partial output,
empty output, and cleanup. Real gateway/model acceptance remains the opt-in
`packages/llm/test/sandbox-launcher-e2e.test.ts` suite.

Embedding computation now runs in the shared embedding service. Both it and
Docling use the [lazy processing listener](../lazy-service/README.md), releasing
model memory after idle time. Sandbox pools and incremental upload caches remain
out of scope: fresh boxes have no upload baseline and unchanged built-in skills
are already omitted.
