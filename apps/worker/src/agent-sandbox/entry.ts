import { readFileSync } from "node:fs";
import {
  makeAgentBackend,
  type AgentBackendId,
  type AgentEvent,
  type AgentWorkspace,
} from "@neko/llm";
import { runAgentBackend, type RunAgentBackendInput } from "@neko/llm/work";
import { BrokerControlPlane } from "./broker-client";
import { EVENT_MARKER, RESULT_MARKER } from "./protocol";

/**
 * Runs INSIDE the agent's OpenShell sandbox (Phase 3). The launcher (work-run)
 * does the DB-bound prologue on the host, uploads the job + workspace, and
 * exec's this. We reconstruct the backend (with a PLACEHOLDER key — the real
 * key is injected by the OpenShell egress proxy, never here), run the agent
 * loop, and STREAM events back as tagged stdout lines that the launcher relays
 * to the host (which scrubs + persists). The model key never enters the box.
 *
 * Events go over stdout rather than a network broker because `openshell
 * sandbox exec` streams stdout to the host in real time — so hermes (which has
 * no MCP tools and emits its action/workflow fences for host-side parsing)
 * needs no broker at all. claude-agent's MCP tools DO need the control plane
 * mid-turn, so a BrokerControlPlane is wired when broker coords are present.
 */
interface SandboxJob {
  orgId: string;
  threadId: string;
  runId: string;
  message: string;
  prompt: string;
  backendId: AgentBackendId;
  model?: string;
  backendState?: Record<string, unknown>;
  pluginActions?: RunAgentBackendInput["pluginActions"];
  wantsCards?: boolean;
  workspace: AgentWorkspace;
}

function requireEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`agent-sandbox: missing required env ${name}`);
  return value;
}

function emitLine(marker: string, obj: unknown): void {
  // One JSON object per line, newline-delimited, with a leading newline so a
  // marker never glues onto unflushed backend output on the same line.
  process.stdout.write(`\n${marker}${JSON.stringify(obj)}\n`);
}

function loadJob(): SandboxJob {
  // Large prompts are uploaded as a file to dodge env/ARG_MAX limits; small
  // jobs may come inline via OPENNEKO_RUN_JOB.
  const file = process.env.OPENNEKO_RUN_JOB_FILE;
  const raw = file ? readFileSync(file, "utf8") : requireEnv("OPENNEKO_RUN_JOB");
  return JSON.parse(raw) as SandboxJob;
}

export async function main(): Promise<void> {
  const job = loadJob();
  const brokerUrl = process.env.OPENNEKO_BROKER_URL;
  const brokerToken = process.env.OPENNEKO_BROKER_TOKEN;

  const backend = makeAgentBackend({
    id: job.backendId,
    model: job.model,
    apiKey: process.env.ANTHROPIC_API_KEY ?? process.env.CLAUDE_API_KEY,
  });

  // Only claude-agent's MCP tools touch the control plane mid-turn; hermes
  // emits fences the host parses after the turn, so it needs no broker.
  const controlPlane =
    brokerUrl && brokerToken
      ? new BrokerControlPlane(brokerUrl, brokerToken)
      : undefined;

  const emit = (event: AgentEvent): Promise<void> => {
    emitLine(EVENT_MARKER, event);
    return Promise.resolve();
  };

  const result = await runAgentBackend({
    backend,
    prompt: job.prompt,
    userMessage: job.message,
    orgId: job.orgId,
    threadId: job.threadId,
    runId: job.runId,
    workspace: job.workspace,
    backendState: job.backendState,
    pluginActions: job.pluginActions ?? [],
    wantsCards: job.wantsCards ?? true,
    controlPlane,
    emit,
  });

  emitLine(RESULT_MARKER, result);
}

main().catch((err: unknown) => {
  console.error(
    "[agent-sandbox] fatal:",
    err instanceof Error ? err.message : String(err),
  );
  process.exit(1);
});
