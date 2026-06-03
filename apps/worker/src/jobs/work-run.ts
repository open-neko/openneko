import type { AgentEvent } from "@neko/llm";
import {
  appendWorkRunEvent,
  getWorkRun,
  runChatTurn,
  scrubAgentEvent,
  type RunChatTurnDeps,
} from "@neko/llm/work";
import {
  getCurrentScrubber,
  getPluginRegistryInstance,
} from "../plugins/registry-instance.js";
import { makeSandboxRunCore } from "../agent-sandbox/launcher.js";

/**
 * Agent-in-sandbox wiring. OPENNEKO_AGENT_RUNTIME=openshell injects a
 * sandbox-running runCore (the agent loop runs in an OpenShell sandbox);
 * default `inprocess` is unchanged. The model provider + egress are env-wired
 * for now (per-org auto-sync is a follow-up).
 */
function agentRuntimeDeps(): Partial<RunChatTurnDeps> {
  if ((process.env.OPENNEKO_AGENT_RUNTIME ?? "inprocess").toLowerCase() !== "openshell") {
    return {};
  }
  const host = process.env.OPENNEKO_AGENT_MODEL_HOST;
  const binary = process.env.OPENNEKO_AGENT_MODEL_BINARY;
  return {
    runCore: makeSandboxRunCore({
      agentImage: process.env.OPENNEKO_AGENT_IMAGE ?? "ghcr.io/open-neko/agent:node20",
      gatewayName: process.env.OPENSHELL_GATEWAY || undefined,
      gatewayEndpoint: process.env.OPENSHELL_GATEWAY_ENDPOINT || undefined,
      modelProvider: process.env.OPENNEKO_AGENT_MODEL_PROVIDER || undefined,
      modelEgress: host && binary ? [{ host, binary }] : [],
      env: process.env.OPENNEKO_AGENT_HERMES_HOME
        ? { HERMES_HOME: process.env.OPENNEKO_AGENT_HERMES_HOME }
        : undefined,
    }),
  };
}

export async function runWorkRun(
  _jobId: string,
  orgId: string,
  payload: { runId: string; threadId: string; message: string },
): Promise<void> {
  const { runId, threadId, message } = payload;

  const run = await getWorkRun(orgId, runId);
  if (!run) {
    console.warn(
      `[work-run] run ${runId} not found for thread ${threadId}; skipping stale job`,
    );
    return;
  }

  // Snapshot the scrubber once per run. fs.watch on the secrets file
  // rebuilds the registry's scrubber, so a future run picks up rotated
  // values; mid-run rotation is documented as out-of-scope.
  const scrubber = getCurrentScrubber();

  const emit = async (event: AgentEvent): Promise<void> => {
    await appendWorkRunEvent({
      orgId,
      threadId,
      runId,
      event: scrubAgentEvent(scrubber, event),
    });
  };

  const pluginActions =
    getPluginRegistryInstance()?.getRegisteredActionDescriptors() ?? [];

  await runChatTurn(
    {
      orgId,
      threadId,
      runId,
      message,
      emit,
      pluginActions,
    },
    agentRuntimeDeps(),
  );
}
