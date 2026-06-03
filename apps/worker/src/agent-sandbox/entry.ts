import {
  makeAgentBackend,
  type AgentBackendId,
  type AgentEvent,
  type AgentWorkspace,
} from "@neko/llm";
import { runAgentBackend, type RunAgentBackendInput } from "@neko/llm/work";
import { BrokerControlPlane, postAgentEvents } from "./broker-client";

/**
 * Runs INSIDE the agent's OpenShell sandbox (Phase 3). The launcher (work-run)
 * does the DB-bound prologue on the host, then exec's this with the prompt +
 * workspace + backend config (NO real key) and the broker coordinates. Here we
 * reconstruct the backend, run the agent loop (runAgentBackend), and reach the
 * control plane + stream events ONLY through the broker. The model key is a
 * proxy-injected placeholder — never the real value. The host does the epilogue
 * (fence handling, persistence) after this returns its result on stdout.
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
  workspace: AgentWorkspace;
}

/** Marker the launcher greps for on the last stdout line to recover the result. */
export const AGENT_RESULT_MARKER = "__openneko_agent_result";

function requireEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`agent-sandbox: missing required env ${name}`);
  return value;
}

export async function main(): Promise<void> {
  const brokerUrl = requireEnv("OPENNEKO_BROKER_URL");
  const token = requireEnv("OPENNEKO_BROKER_TOKEN");
  const job = JSON.parse(requireEnv("OPENNEKO_RUN_JOB")) as SandboxJob;

  // claude reads its key from env (the OpenShell provider sets a placeholder;
  // the egress proxy swaps the real key on the wire). hermes ignores apiKey
  // and reads HERMES_HOME. Either way the real key never enters the sandbox.
  const backend = makeAgentBackend({
    id: job.backendId,
    model: job.model,
    apiKey: process.env.ANTHROPIC_API_KEY ?? process.env.CLAUDE_API_KEY,
  });

  const controlPlane = new BrokerControlPlane(brokerUrl, token);
  const emit = (event: AgentEvent): Promise<void> =>
    postAgentEvents(brokerUrl, token, [event]);

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
    controlPlane,
    emit,
  });

  // Hand the AgentRunResult back to the host launcher as the last stdout line.
  process.stdout.write(`\n${JSON.stringify({ [AGENT_RESULT_MARKER]: result })}\n`);
}

main().catch((err: unknown) => {
  console.error(
    "[agent-sandbox] fatal:",
    err instanceof Error ? err.message : String(err),
  );
  process.exit(1);
});
