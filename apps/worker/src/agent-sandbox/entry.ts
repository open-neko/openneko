import { existsSync, readFileSync } from "node:fs";
import { rm, symlink } from "node:fs/promises";
import { join } from "node:path";
import {
  type AgentBackendId,
  type AgentEvent,
  type AgentModelIdentity,
  type AgentRunOptions,
  type AgentRunResult,
  type AgentWorkspace,
} from "@neko/llm";
import { makeAgentBackend } from "@neko/llm/agent-runtime";
import {
  buildGraphjinAgentServer,
  buildGraphjinMcpServer,
  buildWorkMemoryServer,
  getBuiltinSkillsRoot,
  materializeBuiltinSkills,
  runAgentBackend,
  runWorkflowAgentBackend,
  type RunAgentBackendInput,
} from "@neko/llm/sandbox-runtime";
import { BrokerControlPlane } from "./broker-client";
import { EVENT_MARKER, RESULT_MARKER } from "./protocol";
import { configureAgentRuntime } from "./runtime-contract";

/**
 * Runs INSIDE the agent's OpenShell sandbox (Phase 3). The launcher (work-run)
 * does the DB-bound prologue on the host, uploads the job + workspace, and
 * exec's this. We reconstruct the backend (with a PLACEHOLDER key — the real
 * key is injected by the OpenShell egress proxy, never here), run the agent
 * loop, and STREAM events back as tagged stdout lines that the launcher relays
 * to the host (which scrubs + persists). The model key never enters the box.
 *
 * Events go over stdout because `openshell sandbox exec` streams stdout to the
 * host in real time. Hermes MCP tools use BrokerControlPlane only when the run
 * grants brokered control-plane capabilities.
 */
interface SandboxJob {
  kind?: "work" | "workflow" | "agent-job";
  orgId: string;
  threadId: string;
  runId: string;
  message: string;
  prompt: string;
  backendId: AgentBackendId;
  configuredIdentity?: AgentModelIdentity;
  model?: string;
  backendState?: Record<string, unknown>;
  pluginActions?: RunAgentBackendInput["pluginActions"];
  sourceConfigEnabled?: boolean;
  dataSurface?: RunAgentBackendInput["dataSurface"];
  graphjinToolPolicy?: RunAgentBackendInput["graphjinToolPolicy"];
  nativeDelegation?: RunAgentBackendInput["nativeDelegation"];
  wantsCards?: boolean;
  workflowRunId?: string;
  mode?: "live" | "headless";
  triggeredByObservationId?: string | null;
  workspace: AgentWorkspace;
  /** Explicit least-privilege envelope for non-interactive agent jobs. */
  agentAccess?: {
    graphjinRead?: boolean;
    graphjinAgent?: boolean;
    memorySearch?: boolean;
  };
  agentRun?: Pick<
    AgentRunOptions,
    | "userMessage"
    | "timeoutMs"
    | "retries"
    | "debug"
    | "tag"
    | "skills"
    | "backendState"
    | "nativeDelegation"
    | "wantsCards"
  >;
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
  // The launcher transfers only custom or modified skill directories. Fill in
  // unchanged built-ins from the agent image. Skill bodies remain filesystem
  // resources and are read only when the agent chooses one.
  await materializeBuiltinSkills(job.workspace.skillsRoot);
  // Hermes scans HERMES_HOME/skills. Point it at the per-run catalog so a
  // required skill staged by the host is visible to Hermes' native skill tool.
  if (process.env.HERMES_HOME) {
    const hermesSkillsRoot = join(process.env.HERMES_HOME, "skills");
    await rm(hermesSkillsRoot, { recursive: true, force: true });
    await symlink(job.workspace.skillsRoot, hermesSkillsRoot, "dir");
  }
  const brokerUrl = process.env.OPENNEKO_BROKER_URL;
  const brokerToken = process.env.OPENNEKO_BROKER_TOKEN;

  const backend = makeAgentBackend({
    id: job.backendId,
    configuredIdentity: job.configuredIdentity,
  });

  // MCP tools reach the control plane through stdio bridge children that
  // Hermes ACP launches from this path.
  const controlPlane =
    brokerUrl && brokerToken
      ? new BrokerControlPlane(brokerUrl, brokerToken)
      : undefined;
  const emit = (event: AgentEvent): Promise<void> => {
    emitLine(EVENT_MARKER, event);
    return Promise.resolve();
  };

  const kind = job.kind ?? "work";
  if (kind !== "agent-job" && !controlPlane) {
    throw new Error("agent-sandbox: work run missing broker control plane");
  }
  let result: AgentRunResult;
  if (kind === "agent-job") {
    const graphjinRead = job.agentAccess?.graphjinRead === true;
    const graphjinAgent = job.agentAccess?.graphjinAgent === true;
    const memorySearch = job.agentAccess?.memorySearch === true;
    if ((graphjinRead || graphjinAgent || memorySearch) && !controlPlane) {
      throw new Error("agent-sandbox: brokered job capability missing broker");
    }
    const mcpServers =
      graphjinRead || graphjinAgent || memorySearch
        ? {
            ...(graphjinRead
              ? {
                  neko_graphjin: buildGraphjinMcpServer({
                    orgId: job.orgId,
                    controlPlane,
                  }),
                }
              : {}),
            ...(graphjinAgent
              ? {
                  neko_graphjin_agent: buildGraphjinAgentServer({
                    orgId: job.orgId,
                    runId: job.runId,
                    controlPlane,
                  }),
                }
              : {}),
            ...(memorySearch
              ? {
                  neko_memory: buildWorkMemoryServer(
                    { orgId: job.orgId, runId: job.runId },
                    { exposeSave: false, controlPlane },
                  ),
                }
              : {}),
          }
        : undefined;
    result = await backend.run({
      ...(job.agentRun ?? {}),
      prompt: job.prompt,
      userMessage: job.agentRun?.userMessage ?? (job.message || undefined),
      orgId: job.orgId,
      workspace: job.workspace,
      onEvent: emit,
      mcpServers,
      wantsCards: false,
      mcpBridgeEnv:
        graphjinRead || graphjinAgent || memorySearch
          ? {
              OPENNEKO_MCP_ORG_ID: job.orgId,
              OPENNEKO_MCP_MODE: "agent-job",
              OPENNEKO_MCP_THREAD_ID: job.threadId,
              OPENNEKO_MCP_RUN_ID: job.runId,
              OPENNEKO_MCP_SKILLS_ROOT: job.workspace.skillsRoot,
              OPENNEKO_MCP_PLUGIN_ACTIONS: "[]",
              OPENNEKO_MCP_MEMORY_READ_ONLY: "1",
            }
          : undefined,
    });
  } else if (kind === "workflow") {
    if (!controlPlane) {
      throw new Error("agent-sandbox: workflow run missing broker control plane");
    }
    const workflowRunId = job.workflowRunId;
    if (!workflowRunId) {
      throw new Error("agent-sandbox: workflow job missing workflowRunId");
    }
    result = await runWorkflowAgentBackend({
      backend,
      prompt: job.prompt,
      userMessage: job.message,
      orgId: job.orgId,
      threadId: job.threadId,
      runId: job.runId,
      workflowRunId,
      mode: job.mode ?? "headless",
      triggeredByObservationId: job.triggeredByObservationId ?? null,
      workspace: job.workspace,
      controlPlane,
      emit,
    });
  } else {
    if (!controlPlane) {
      throw new Error("agent-sandbox: work run missing broker control plane");
    }
    result = await runAgentBackend({
      backend,
      prompt: job.prompt,
      userMessage: job.message,
      orgId: job.orgId,
      threadId: job.threadId,
      runId: job.runId,
      workspace: job.workspace,
      backendState: job.backendState,
      pluginActions: job.pluginActions ?? [],
      sourceConfigEnabled: job.sourceConfigEnabled ?? false,
      dataSurface: job.dataSurface ?? "customer",
      ...(job.graphjinToolPolicy
        ? { graphjinToolPolicy: job.graphjinToolPolicy }
        : {}),
      ...(job.nativeDelegation
        ? { nativeDelegation: job.nativeDelegation }
        : {}),
      wantsCards: job.wantsCards ?? true,
      controlPlane,
      emit,
    });
  }

  emitLine(RESULT_MARKER, result);
}

async function run(): Promise<void> {
  const runtime = configureAgentRuntime({ entryUrl: import.meta.url });
  const skillsRoot = getBuiltinSkillsRoot();
  if (!existsSync(skillsRoot)) {
    throw new Error(
      `agent runtime contract invalid: built-in skills not found at ${skillsRoot}`,
    );
  }

  if (process.argv.includes("--preflight")) {
    process.stdout.write(
      `${JSON.stringify({
        status: "ok",
        skillsRoot,
        mcpBridgePath: runtime.mcpBridgePath,
        lazyInstallsDisabled: process.env.HERMES_DISABLE_LAZY_INSTALLS === "1",
      })}\n`,
    );
    return;
  }
  await main();
}

run().catch((err: unknown) => {
  console.error(
    "[agent-sandbox] fatal:",
    err instanceof Error ? err.message : String(err),
  );
  process.exit(1);
});
