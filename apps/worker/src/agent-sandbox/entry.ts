import type { AgentEvent } from "@neko/llm";
import { runChatTurn } from "@neko/llm/work";
import { BrokerControlPlane, postAgentEvents } from "./broker-client";

/**
 * Entrypoint that runs INSIDE the agent's OpenShell sandbox (Phase 2c). It
 * reads its job + broker coordinates from env (injected by the launcher in
 * work-run.ts) and wires runChatTurn so the agent reaches the control plane
 * and emits events ONLY through the broker — never the DB directly.
 *
 * NOTE: runChatTurn still makes a few store calls in its own body
 * (markWorkRunRunning, getWorkThreadBundle, saveAssistantWorkMessage,
 * finishWorkRun, setWorkThreadBackendState). Those must be brokered too — or
 * split into a host-side prologue/epilogue — before this runs in a real
 * sandbox with no DB creds. See docs/OPENSHELL_MIGRATION_PLAN.md (Phase 2c).
 */
interface SandboxJob {
  orgId: string;
  threadId: string;
  runId: string;
  message: string;
  pluginActions?: Parameters<typeof runChatTurn>[0]["pluginActions"];
}

function requireEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`agent-sandbox: missing required env ${name}`);
  return value;
}

export async function main(): Promise<void> {
  const brokerUrl = requireEnv("OPENNEKO_BROKER_URL");
  const token = requireEnv("OPENNEKO_BROKER_TOKEN");
  const job = JSON.parse(requireEnv("OPENNEKO_RUN_JOB")) as SandboxJob;

  const controlPlane = new BrokerControlPlane(brokerUrl, token);
  const emit = (event: AgentEvent): Promise<void> =>
    postAgentEvents(brokerUrl, token, [event]);

  const result = await runChatTurn({
    orgId: job.orgId,
    threadId: job.threadId,
    runId: job.runId,
    message: job.message,
    emit,
    pluginActions: job.pluginActions ?? [],
    controlPlane,
  });

  if (result.status === "failed") process.exitCode = 1;
}

main().catch((err: unknown) => {
  console.error(
    "[agent-sandbox] fatal:",
    err instanceof Error ? err.message : String(err),
  );
  process.exit(1);
});
