import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import type { Transport } from "@modelcontextprotocol/sdk/shared/transport.js";
import {
  buildAuditViewerServer,
  buildChannelManagerServer,
  buildDataSourceManagerServer,
  buildPluginActionServer,
  buildPluginManagerServer,
  buildSkillBuilderServer,
  buildSourceConfigManagerServer,
  buildUserManagerServer,
  buildWorkMemoryServer,
  type PluginActionDescriptor,
} from "@neko/llm/work";
import { buildRuleBuilderServer, buildWorkflowBuilderServer } from "@neko/llm/workflows";
import { BrokerControlPlane } from "./broker-client";

/**
 * Stdio MCP server for ACP backends (hermes). The agent process can't hand
 * its in-process SDK server instances across a process boundary, so hermes'
 * `session/new` mcpServers entries launch THIS script — one child per named
 * server — and it rebuilds that server bound to the broker control plane
 * (the same path claude's in-box tools use). Spawned by hermes inside the
 * sandbox; never runs on the host.
 *
 * argv[2] = server name (agent-core's mcpServers map key). Per-run context
 * arrives via env (agent-core's mcpBridgeEnv); broker coords inherit through
 * the process env (entry.ts → hermes → here).
 */

function requireEnv(name: string): string {
  const value = process.env[name];
  if (!value) throw new Error(`mcp-bridge: missing required env ${name}`);
  return value;
}

export function buildBridgeServer(
  name: string,
  ctx: {
    orgId: string;
    threadId: string;
    runId: string;
    skillsRoot: string;
    pluginActions: PluginActionDescriptor[];
    controlPlane: BrokerControlPlane;
  },
): { instance: { connect: (t: Transport) => Promise<void> } } {
  const { orgId, threadId, runId, skillsRoot, pluginActions, controlPlane } = ctx;
  // Tool emits stream into the run on the in-process path; from a bridge
  // child they have no channel back, and the durable effects (action
  // requests, memory rows) persist via the control plane regardless.
  const emit = () => {};
  const common = { orgId, runId, emit, controlPlane };
  switch (name) {
    case "neko_skills":
      return buildSkillBuilderServer(skillsRoot);
    case "neko_memory":
      return buildWorkMemoryServer({ orgId, threadId, runId }, { controlPlane });
    case "neko_workflow_builder":
      return buildWorkflowBuilderServer({
        orgId,
        createdByThreadId: threadId,
        createdByRunId: runId,
        emit,
        controlPlane,
      });
    case "neko_rule_builder":
      return buildRuleBuilderServer({
        orgId,
        createdByThreadId: threadId,
        createdByRunId: runId,
        emit,
        controlPlane,
      });
    case "neko_plugin_manager":
      return buildPluginManagerServer(common);
    case "neko_user_manager":
      return buildUserManagerServer(common);
    case "neko_channel_manager":
      return buildChannelManagerServer(common);
    case "neko_data_source_manager":
      return buildDataSourceManagerServer(common);
    case "neko_source_config_manager":
      return buildSourceConfigManagerServer(common);
    case "neko_audit":
      return buildAuditViewerServer({ orgId, runId, controlPlane });
    case "neko_plugin_actions": {
      const server = buildPluginActionServer({
        orgId,
        threadId,
        runId,
        descriptors: pluginActions,
        emit,
        controlPlane,
      });
      if (!server) throw new Error("mcp-bridge: no plugin actions in this run");
      return server;
    }
    default:
      throw new Error(`mcp-bridge: unknown server ${name}`);
  }
}

/**
 * The egress proxy admits a freshly spawned process with a lag — its first
 * dials get ECONNREFUSED even with an allow rule in place. Poll until the
 * broker answers anything at all (any HTTP status counts) before serving, so
 * the first real tool call rides a warmed path.
 */
async function warmUpBroker(baseUrl: string, name: string): Promise<void> {
  const trail: string[] = [];
  const deadline = Date.now() + 8_000;
  let attempts = 0;
  for (;;) {
    attempts += 1;
    try {
      const res = await fetch(new URL("/v1/memory/search", baseUrl), {
        method: "POST",
        body: "{}",
      });
      trail.push(`attempt ${attempts}: HTTP ${res.status}`);
      break;
    } catch (err) {
      const cause = (err as { cause?: { code?: string } }).cause?.code;
      trail.push(`attempt ${attempts}: ${cause ?? (err as Error).message}`);
      if (Date.now() > deadline) break; // serve anyway; per-call retries remain
      await new Promise((r) => setTimeout(r, 250));
    }
  }
  // Startup diagnostics, readable mid-run via `sandbox exec` — bridge stderr
  // is swallowed by hermes, so a file is the only visible channel.
  try {
    const envPick = Object.fromEntries(
      Object.entries(process.env).filter(([k]) =>
        /OPENNEKO_|PROXY|proxy|NODE_USE/.test(k),
      ),
    );
    const { writeFileSync } = await import("node:fs");
    writeFileSync(
      `/tmp/bridge-${name}.log`,
      JSON.stringify({ baseUrl, trail, env: envPick }, null, 1),
    );
  } catch {
    /* diagnostics only */
  }
}

async function main(): Promise<void> {
  const name = process.argv[2];
  if (!name) throw new Error("mcp-bridge: missing server-name argument");
  const brokerUrl = requireEnv("OPENNEKO_BROKER_URL");
  await warmUpBroker(brokerUrl, name);
  const controlPlane = new BrokerControlPlane(
    brokerUrl,
    requireEnv("OPENNEKO_BROKER_TOKEN"),
  );
  const server = buildBridgeServer(name, {
    orgId: requireEnv("OPENNEKO_MCP_ORG_ID"),
    threadId: requireEnv("OPENNEKO_MCP_THREAD_ID"),
    runId: requireEnv("OPENNEKO_MCP_RUN_ID"),
    skillsRoot: requireEnv("OPENNEKO_MCP_SKILLS_ROOT"),
    pluginActions: JSON.parse(
      process.env.OPENNEKO_MCP_PLUGIN_ACTIONS ?? "[]",
    ) as PluginActionDescriptor[],
    controlPlane,
  });
  await server.instance.connect(new StdioServerTransport());
}

const invokedDirectly =
  process.argv[1]?.endsWith("mcp-bridge.ts") ||
  process.argv[1]?.endsWith("mcp-bridge.js");
if (invokedDirectly) {
  main().catch((err: unknown) => {
    console.error(
      "[mcp-bridge] fatal:",
      err instanceof Error ? err.message : String(err),
    );
    process.exit(1);
  });
}
