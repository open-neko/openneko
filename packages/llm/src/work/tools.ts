import { createSdkMcpServer, tool } from "@anthropic-ai/claude-agent-sdk";
import { z } from "zod";
import { writeWorkSkill } from "./skills";
import { RENDER_CARDS_DESCRIPTION } from "./render-catalog";
import type { AgentEvent, AgentSurfaceMessage } from "../agent-backend";
import { WORK_MEMORY_KINDS, type WorkMemoryContext } from "./memory";
import type { RiskLevel } from "../workflows";
import {
  inProcessControlPlane,
  type AgentControlPlane,
} from "./control-plane";

const a2uiMessageSchema = z.object({ version: z.literal("v0.9") }).passthrough();

function isValidA2UIMessage(m: unknown): m is AgentSurfaceMessage {
  if (!m || typeof m !== "object") return false;
  const o = m as Record<string, unknown>;
  if (o.version !== "v0.9") return false;
  return (
    "createSurface" in o ||
    "updateComponents" in o ||
    "updateDataModel" in o ||
    "deleteSurface" in o
  );
}

export function buildRenderCardsServer(
  emit: (event: AgentEvent) => Promise<void> | void,
) {
  const renderCards = tool(
    "render_cards",
    RENDER_CARDS_DESCRIPTION,
    {
      messages: z.array(a2uiMessageSchema).min(1),
    },
    async (args) => {
      const valid = (args.messages as unknown[]).filter(isValidA2UIMessage);
      const rejected = args.messages.length - valid.length;
      if (valid.length === 0) {
        return {
          content: [
            {
              type: "text" as const,
              text: JSON.stringify({
                ok: false,
                error:
                  "All messages rejected — each must have version: 'v0.9' and one of createSurface/updateComponents/updateDataModel/deleteSurface. Bare component objects are not accepted; wrap them in updateComponents.",
                rejected,
              }),
            },
          ],
        };
      }
      await emit({ type: "surface", messages: valid });
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({
              ok: true,
              accepted: valid.length,
              rejected,
            }),
          },
        ],
      };
    },
  );

  return createSdkMcpServer({
    name: "neko_ui",
    version: "1.0.0",
    tools: [renderCards],
  });
}

/**
 * ADM3 — chat-first plugin management. The agent can list installed +
 * marketplace plugins and PROPOSE installs/uninstalls; the commit step is
 * always an action_request through the control plane (policy-gated;
 * plugin_management_default seeds approval_required), never a direct
 * mutation. Credentials never transit this path — installs that need env
 * keys fail with a pointer to the secrets flow.
 */
export function buildPluginManagerServer(opts: {
  orgId: string;
  runId?: string;
  emit: (event: AgentEvent) => Promise<void> | void;
  controlPlane?: AgentControlPlane;
}) {
  const controlPlane = opts.controlPlane ?? inProcessControlPlane;

  const listPlugins = tool(
    "list_plugins",
    [
      "List installed plugins (with version, source, declared network",
      "egress) and the official marketplace catalog. Use this before",
      "proposing an install or uninstall, and to answer 'what plugins do",
      "we have?'.",
    ].join(" "),
    {},
    async () => {
      const catalog = await controlPlane.listPlugins({ orgId: opts.orgId });
      return {
        content: [
          { type: "text" as const, text: JSON.stringify(catalog) },
        ],
      };
    },
  );

  const requestChange = (
    kind: "plugin_install" | "plugin_uninstall",
    target: string,
    intent: string,
    payload: Record<string, unknown>,
  ) =>
    proposeAdminAction({
      controlPlane,
      orgId: opts.orgId,
      runId: opts.runId,
      emit: opts.emit,
      kind,
      target,
      intent,
      payload,
    });

  const installTool = tool(
    "request_plugin_install",
    [
      "Propose installing a plugin from the marketplace. This NEVER",
      "installs directly — it files an approval-gated action request; the",
      "operator approves it on the approvals surface. Tell the operator",
      "what you proposed and end your turn. If the plugin requires env",
      "keys, the install will report them — the operator sets those via",
      "`openneko secrets set` or the integrations page, never through",
      "chat.",
    ].join(" "),
    {
      spec: z.string().trim().min(1).describe("npm package name, e.g. @open-neko/plugin-slack"),
      intent: z.string().trim().min(1).max(500),
    },
    async (args) =>
      requestChange("plugin_install", args.spec, args.intent, {
        spec: args.spec,
      }),
  );

  const uninstallTool = tool(
    "request_plugin_uninstall",
    [
      "Propose removing an installed plugin. Approval-gated like installs;",
      "the operator confirms on the approvals surface.",
    ].join(" "),
    {
      name: z.string().trim().min(1),
      intent: z.string().trim().min(1).max(500),
    },
    async (args) =>
      requestChange("plugin_uninstall", args.name, args.intent, {
        name: args.name,
      }),
  );

  return createSdkMcpServer({
    name: "neko_plugin_manager",
    version: "1.0.0",
    tools: [listPlugins, installTool, uninstallTool],
  });
}

async function proposeAdminAction(opts: {
  controlPlane: AgentControlPlane;
  orgId: string;
  runId?: string;
  emit: (event: AgentEvent) => Promise<void> | void;
  kind:
    | "plugin_install"
    | "plugin_uninstall"
    | "user_admin"
    | "channel_admin"
    | "data_source_admin";
  target: string;
  intent: string;
  payload: Record<string, unknown>;
}) {
  const decision = await opts.controlPlane.evaluateActionPolicy({
    orgId: opts.orgId,
    scope: "internal",
    kind: opts.kind,
    target: opts.target,
    riskLevel: "high",
  } as Parameters<AgentControlPlane["evaluateActionPolicy"]>[0]);

  if (decision.decision !== "allow" && decision.decision !== "needs_approval") {
    return {
      content: [
        {
          type: "text" as const,
          text: JSON.stringify({
            ok: false,
            decision: "denied",
            reason:
              "reason" in decision && decision.reason
                ? String(decision.reason)
                : "policy denies plugin management from chat",
          }),
        },
      ],
    };
  }

  const status =
    decision.decision === "allow" ? "approved" : "pending_approval";
  const request = await opts.controlPlane.createActionRequest({
    orgId: opts.orgId,
    scope: "internal",
    kind: opts.kind,
    target: opts.target,
    payload: opts.payload,
    riskLevel: "high",
    status,
    policyId: "policy" in decision && decision.policy ? decision.policy.id : null,
    summary: opts.intent,
    intent: opts.intent,
    workRunId: opts.runId ?? null,
    requestedByRunId: null,
  } as Parameters<AgentControlPlane["createActionRequest"]>[0]);

  if (status === "approved") {
    await opts.controlPlane.enqueueActionExecute({
      orgId: opts.orgId,
      actionRequestId: request.id,
    });
  }

  await opts.emit({
    type: "action_request_emit",
    action_request_id: request.id,
    kind: opts.kind,
    scope: "internal",
    decision: status === "approved" ? "auto_approved" : "pending_approval",
    summary: opts.intent,
    risk_level: "high",
  } as AgentEvent);

  return {
    content: [
      {
        type: "text" as const,
        text: JSON.stringify({
          ok: true,
          decision: status,
          action_request_id: request.id,
          note:
            status === "approved"
              ? "Queued for execution; the result lands in this run."
              : "Approval-gated: tell the operator what you proposed and end your turn — the approval card is rendered inline.",
        }),
      },
    ],
  };
}

/**
 * ADM1 — chat-first user management. list_users reads through the
 * control plane; every change is an approval-gated action request whose
 * policy demands an ADMIN approver (user_management_default, K2-enforced).
 */
export function buildUserManagerServer(opts: {
  orgId: string;
  runId?: string;
  emit: (event: AgentEvent) => Promise<void> | void;
  controlPlane?: AgentControlPlane;
}) {
  const controlPlane = opts.controlPlane ?? inProcessControlPlane;

  const listUsers = tool(
    "list_users",
    [
      "List the org's users (email, role, disabled state, last login).",
      "Use before proposing any change, and to answer 'who has access?'.",
    ].join(" "),
    {},
    async () => ({
      content: [
        {
          type: "text" as const,
          text: JSON.stringify(await controlPlane.listUsers({ orgId: opts.orgId })),
        },
      ],
    }),
  );

  const requestChange = tool(
    "request_user_change",
    [
      "Propose a user-management change: invite (email + role),",
      "set_role (userId + role admin|member), deactivate or reactivate",
      "(userId). NEVER applies directly — files an action request that an",
      "ADMIN must approve on the approvals surface. Tell the operator what",
      "you proposed and end your turn.",
    ].join(" "),
    {
      action: z.enum(["invite", "set_role", "deactivate", "reactivate"]),
      email: z.string().trim().email().optional(),
      userId: z.string().trim().min(1).optional(),
      role: z.enum(["admin", "member"]).optional(),
      intent: z.string().trim().min(1).max(500),
    },
    async (args) => {
      if (args.action === "invite" && (!args.email || !args.role)) {
        return {
          content: [
            {
              type: "text" as const,
              text: JSON.stringify({ ok: false, error: "invite needs email + role" }),
            },
          ],
        };
      }
      if (args.action !== "invite" && !args.userId) {
        return {
          content: [
            {
              type: "text" as const,
              text: JSON.stringify({ ok: false, error: `${args.action} needs userId` }),
            },
          ],
        };
      }
      return proposeAdminAction({
        controlPlane,
        orgId: opts.orgId,
        runId: opts.runId,
        emit: opts.emit,
        kind: "user_admin",
        target: args.email ?? args.userId ?? args.action,
        intent: args.intent,
        payload: {
          action: args.action,
          ...(args.email ? { email: args.email } : {}),
          ...(args.userId ? { userId: args.userId } : {}),
          ...(args.role ? { role: args.role } : {}),
        },
      });
    },
  );

  return createSdkMcpServer({
    name: "neko_user_manager",
    version: "1.0.0",
    tools: [listUsers, requestChange],
  });
}

/**
 * ADM5 — chat-first channel management. Reads (workspaces + identities)
 * answer "who's talking to the bot and as whom?"; changes (link/unlink/
 * block/unblock an identity) file an action request an ADMIN must
 * approve (channel_management_default, K2-enforced).
 */
export function buildChannelManagerServer(opts: {
  orgId: string;
  runId?: string;
  emit: (event: AgentEvent) => Promise<void> | void;
  controlPlane?: AgentControlPlane;
}) {
  const controlPlane = opts.controlPlane ?? inProcessControlPlane;

  const listChannels = tool(
    "list_channels",
    [
      "List the org's channel workspaces (Slack team / WhatsApp account /",
      "Telegram bot bindings) and every channel identity seen, with link",
      "status (linked|unverified|blocked) and the app_user it acts as.",
      "Use before proposing any change, and to answer 'who can reach the",
      "agent from chat?'.",
    ].join(" "),
    {},
    async () => ({
      content: [
        {
          type: "text" as const,
          text: JSON.stringify(
            await controlPlane.listChannels({ orgId: opts.orgId }),
          ),
        },
      ],
    }),
  );

  const requestChange = tool(
    "request_channel_change",
    [
      "Propose a channel-identity change: link (identityId + appUserId),",
      "unlink, block or unblock (identityId). NEVER applies directly —",
      "files an action request that an ADMIN must approve on the approvals",
      "surface. Tell the operator what you proposed and end your turn.",
    ].join(" "),
    {
      action: z.enum(["link", "unlink", "block", "unblock"]),
      identityId: z.string().trim().min(1),
      appUserId: z.string().trim().min(1).optional(),
      intent: z.string().trim().min(1).max(500),
    },
    async (args) => {
      if (args.action === "link" && !args.appUserId) {
        return {
          content: [
            {
              type: "text" as const,
              text: JSON.stringify({ ok: false, error: "link needs appUserId" }),
            },
          ],
        };
      }
      return proposeAdminAction({
        controlPlane,
        orgId: opts.orgId,
        runId: opts.runId,
        emit: opts.emit,
        kind: "channel_admin",
        target: args.identityId,
        intent: args.intent,
        payload: {
          action: args.action,
          identityId: args.identityId,
          ...(args.appUserId ? { appUserId: args.appUserId } : {}),
        },
      });
    },
  );

  return createSdkMcpServer({
    name: "neko_channel_manager",
    version: "1.0.0",
    tools: [listChannels, requestChange],
  });
}

/**
 * ADM2 — chat-first data-source registry. Reads list the org's sources
 * (hostnames only — connection strings and credentials never enter
 * model context); changes file a data_source_admin action request an
 * ADMIN must approve. Registration creates a disabled placeholder; the
 * admin enters connection details + credentials in the settings form
 * (forms are for credential entry only, never the model path). The
 * GraphJin discovery itself (discover_databases / plan_database_setup /
 * test_database_connection) runs through `graphjin cli` like every
 * other read.
 */
export function buildDataSourceManagerServer(opts: {
  orgId: string;
  runId?: string;
  emit: (event: AgentEvent) => Promise<void> | void;
  controlPlane?: AgentControlPlane;
}) {
  const controlPlane = opts.controlPlane ?? inProcessControlPlane;

  const listSources = tool(
    "list_data_sources",
    [
      "List the org's registered data sources: name, label, auth mode,",
      "default flag, enabled state and server hostname. No connection",
      "strings or credentials are ever returned. Use before proposing",
      "any change.",
    ].join(" "),
    {},
    async () => ({
      content: [
        {
          type: "text" as const,
          text: JSON.stringify(
            await controlPlane.listDataSources({ orgId: opts.orgId }),
          ),
        },
      ],
    }),
  );

  const requestChange = tool(
    "request_data_source_change",
    [
      "Propose a data-source registry change: register (name + optional",
      "label — creates a DISABLED placeholder; the admin completes the",
      "connection in Settings, credentials never pass through chat),",
      "enable, disable, set_default, or remove (name). NEVER applies",
      "directly — files an action request that an ADMIN must approve.",
      "Tell the operator what you proposed and end your turn.",
    ].join(" "),
    {
      action: z.enum(["register", "enable", "disable", "set_default", "remove"]),
      name: z
        .string()
        .trim()
        .min(1)
        .max(64)
        .regex(/^[a-z0-9][a-z0-9-]*$/, "lowercase letters, digits, dashes"),
      label: z.string().trim().max(120).optional(),
      // OL5 source graph: what kind of system this source fronts.
      sourceKind: z.enum(["graphjin", "database", "api", "files", "code"]).optional(),
      intent: z.string().trim().min(1).max(500),
    },
    async (args) =>
      proposeAdminAction({
        controlPlane,
        orgId: opts.orgId,
        runId: opts.runId,
        emit: opts.emit,
        kind: "data_source_admin",
        target: args.name,
        intent: args.intent,
        payload: {
          action: args.action,
          name: args.name,
          ...(args.label ? { label: args.label } : {}),
          ...(args.sourceKind ? { sourceKind: args.sourceKind } : {}),
        },
      }),
  );

  return createSdkMcpServer({
    name: "neko_data_source_manager",
    version: "1.0.0",
    tools: [listSources, requestChange],
  });
}

/**
 * ADM4 — chat-first audit viewer. One read-only tool returning the
 * action-request trail (with SEC5 dual identity), SEC7 behavior alerts,
 * and a 24h gateway-call summary. The control plane enforces the admin
 * gate on the requesting run's actor — a member or service run gets a
 * denial, never data.
 */
export function buildAuditViewerServer(opts: {
  orgId: string;
  runId?: string;
  controlPlane?: AgentControlPlane;
}) {
  const controlPlane = opts.controlPlane ?? inProcessControlPlane;

  const auditTrail = tool(
    "audit_trail",
    [
      "ADMIN ONLY. The org's audit trail: recent action requests (who",
      "proposed what via which agent backend, and what happened),",
      "behavioral alerts (SEC7 thresholds), and a 24h summary of",
      "control-plane gateway calls per run. Use to answer 'what has the",
      "agent been doing?' and 'who approved that?'.",
    ].join(" "),
    {
      limit: z.number().int().min(1).max(200).optional(),
    },
    async (args) => {
      const result = await controlPlane.listAuditTrail({
        orgId: opts.orgId,
        runId: opts.runId ?? null,
        limit: args.limit,
      });
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify(
              result.denied
                ? { ok: false, error: "audit trail is admin-only" }
                : { ok: true, ...result },
            ),
          },
        ],
      };
    },
  );

  return createSdkMcpServer({
    name: "neko_audit",
    version: "1.0.0",
    tools: [auditTrail],
  });
}

export function buildSkillBuilderServer(skillsRoot: string) {
  const createSkill = tool(
    "create_skill",
    "Create or update an agentskills.io-style skill in Neko's shared skills directory.",
    {
      name: z.string().trim().min(1).max(64),
      description: z.string().trim().min(1).max(1024),
      body: z.string().min(1),
      license: z.string().trim().min(1).max(200).optional(),
      compatibility: z.string().trim().min(1).max(500).optional(),
      metadata: z.record(z.string().trim().min(1).max(128), z.string().max(2048)).optional(),
      allowedTools: z.string().trim().min(1).max(1000).optional(),
      files: z.array(
        z.object({
          path: z.string().trim().min(1).max(240),
          content: z.string().max(128 * 1024),
        }),
      ).max(30).optional(),
    },
    async (args) => {
      const result = await writeWorkSkill(skillsRoot, args);
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({
              ok: true,
              name: result.name,
              skillFile: `${result.skillPath}/SKILL.md`,
            }),
          },
        ],
      };
    },
  );

  return createSdkMcpServer({
    name: "neko_skills",
    version: "1.0.0",
    tools: [createSkill],
  });
}

// Two-tool memory surface: `save` and `search`. Reads use pgvector
// context-search (matches the auto-context retrieval path), writes go
// through the same rememberWorkMemory used by the `save:` chat command,
// so embeddings get computed in lockstep. We deliberately don't expose
// `forget` from the agent — archival is operator-driven, not agent-driven.
export type WorkMemoryServerOptions = {
  /**
   * Expose the `save` tool. Default true. One-shot agents (e.g. the
   * metric agent) pass `false` so they get search-only — the operator
   * remains the authority on what gets persisted.
   */
  exposeSave?: boolean;
  /** Defaults to the in-process control plane; the agent sandbox injects an HTTP impl. */
  controlPlane?: AgentControlPlane;
};

export function buildWorkMemoryServer(
  ctx: WorkMemoryContext,
  options: WorkMemoryServerOptions = {},
) {
  const exposeSave = options.exposeSave ?? true;
  const controlPlane = options.controlPlane ?? inProcessControlPlane;
  const search = tool(
    "search",
    [
      "Semantic search over OpenNeko's saved memories. Returns top-N",
      "memories ranked by cosine similarity to the query. Use when the",
      "loaded memories above don't seem to cover what the operator is",
      "asking about.",
    ].join(" "),
    {
      query: z.string().min(2).max(800),
      limit: z.number().int().min(1).max(20).optional(),
    },
    async (args) => {
      const results = await controlPlane.searchWorkMemoryByContext({
        orgId: ctx.orgId,
        query: args.query,
        limit: args.limit ?? 5,
        runId: ctx.runId ?? null,
      });
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({ ok: true, results }),
          },
        ],
      };
    },
  );

  const save = tool(
    "save",
    [
      "Save a durable memory the operator stated explicitly. Use only when",
      "the operator says to remember/save something, corrects a recurring",
      "assumption, defines a metric/business rule, or states a stable",
      "preference. Never speculatively. Default scope=global, kind=business_rule,",
      "pinned=true.",
    ].join(" "),
    {
      text: z.string().min(5).max(2000),
      kind: z.enum(WORK_MEMORY_KINDS).optional(),
      scope: z.enum(["global", "thread"]).optional(),
      pinned: z.boolean().optional(),
    },
    async (args) => {
      const memory = await controlPlane.rememberWorkMemory({
        orgId: ctx.orgId,
        threadId: ctx.threadId ?? null,
        runId: ctx.runId ?? null,
        text: args.text,
        kind: args.kind ?? "business_rule",
        scope: args.scope ?? "global",
        pinned: args.pinned ?? true,
      });
      return {
        content: [
          {
            type: "text" as const,
            text: JSON.stringify({ ok: true, memoryId: memory.id }),
          },
        ],
      };
    },
  );

  return createSdkMcpServer({
    name: "neko_memory",
    version: "1.0.0",
    tools: exposeSave ? [search, save] : [search],
  });
}

/**
 * Plugin-installed action kinds, surfaced to the agent as MCP tools.
 * The worker collects these from PluginRegistry at turn start and
 * passes them in — the registry has the manifest entries; this module
 * only needs the {kind, description, default_mode} triples to build
 * tools.
 */
export interface PluginActionDescriptor {
  kind: string;
  description: string;
  /**
   * Seeded approval mode from the manifest; runtime policy may
   * override. Accepts either a scalar (applies to all scopes) or a
   * per-scope object for kinds whose default depends on the scope
   * they're invoked under.
   */
  default_mode?:
    | "auto"
    | "ask"
    | "deny"
    | {
        external?: "auto" | "ask" | "deny";
        internal?: "auto" | "ask" | "deny";
      };
  /** Example payload from the manifest, surfaced to the agent so it shapes the call correctly. */
  example?: Record<string, unknown>;
}

function modeForScope(
  default_mode: PluginActionDescriptor["default_mode"],
  scope: "external" | "internal",
): "auto" | "ask" | "deny" | undefined {
  if (default_mode === undefined) return undefined;
  if (typeof default_mode === "string") return default_mode;
  return scope === "external" ? default_mode.external : default_mode.internal;
}

function isDeniedEverywhere(
  default_mode: PluginActionDescriptor["default_mode"],
): boolean {
  if (default_mode === "deny") return true;
  if (default_mode && typeof default_mode === "object") {
    const keys = Object.keys(default_mode) as Array<"external" | "internal">;
    if (keys.length > 0 && keys.every((k) => default_mode[k] === "deny")) {
      return true;
    }
  }
  return false;
}

function needsIntentForKind(
  default_mode: PluginActionDescriptor["default_mode"],
): boolean {
  // Intent is required at schema-time whenever the kind COULD land in
  // ask-mode under any scope — so the agent has to author one
  // regardless of which scope it picks. Auto-only kinds skip it.
  if (default_mode === "ask") return true;
  if (default_mode && typeof default_mode === "object") {
    return Object.values(default_mode).some((m) => m === "ask");
  }
  return false;
}

export interface BuildPluginActionServerOptions {
  orgId: string;
  threadId: string;
  runId: string;
  descriptors: readonly PluginActionDescriptor[];
  emit: (event: AgentEvent) => Promise<void> | void;
  /** Defaults to the in-process control plane; the agent sandbox injects an HTTP impl. */
  controlPlane?: AgentControlPlane;
}

/**
 * One MCP tool per registered plugin action kind. Each tool's handler
 * routes through the action_policy engine:
 *
 *   - allow (auto_approve)  → create approved action_request, run the
 *                             adapter synchronously, return the outcome
 *                             inline. The agent sees the result and
 *                             keeps talking.
 *   - needs_approval (ask)  → create pending_approval action_request,
 *                             emit an action_request_emit event so the
 *                             /work UI can render an inline approval
 *                             card, return {pending_approval, id}.
 *                             The agent's run ends gracefully. Slice 5
 *                             will kick off a follow-up run with the
 *                             approval outcome.
 *   - deny / no_policy      → return {ok:false, denied} to the agent.
 *
 * Tools whose declared default_mode is "deny" are excluded from the
 * MCP surface entirely — plugin authors using `deny` mean "this
 * kind exists in the plugin's vocabulary but the agent never gets to
 * call it without explicit operator opt-in."
 *
 * `intent` (the agent's single-sentence natural-language framing of
 * what it's about to do) is required only on kinds whose default_mode
 * is `ask` — it's what the approval card shows to the user. Auto kinds
 * skip it; if a runtime policy escalates auto → needs_approval and the
 * agent didn't supply intent, we synthesize a fallback from the tool
 * call.
 *
 * Returns null when no kinds are registered (or all are deny) so the
 * caller can omit the server from the MCP map.
 */
export function buildPluginActionServer(
  opts: BuildPluginActionServerOptions,
): ReturnType<typeof createSdkMcpServer> | null {
  const active = opts.descriptors.filter((d) => !isDeniedEverywhere(d.default_mode));
  if (active.length === 0) return null;

  const controlPlane = opts.controlPlane ?? inProcessControlPlane;

  const tools = active.map((d) => {
    const needsIntent = needsIntentForKind(d.default_mode);
    const baseSchema = {
      target: z
        .string()
        .optional()
        .describe(
          "Canonical resource identifier this action targets (e.g. a channel id, a record id). Optional; some kinds don't need one.",
        ),
      risk_level: z
        .enum(["low", "medium", "high", "critical"])
        .optional()
        .describe(
          "Best-effort blast-radius estimate for this specific invocation. Influences risk-threshold-based policy rules.",
        ),
      payload: z
        .record(z.string(), z.unknown())
        .default({})
        .describe(
          "Action-specific payload. See the tool description for the expected shape.",
        ),
    };
    // `intent` lives in both schemas so handler-side narrowing is
    // trivial (one optional property). Ask-mode kinds require it
    // (min length 3); auto-mode kinds make it optional and ignore
    // it unless the runtime policy escalates the call to ask.
    const intentField = needsIntent
      ? z
          .string()
          .min(3)
          .max(500)
          .describe(
            "Single-sentence natural-language description of what you're about to do and why, written for the user reviewing the approval request. e.g. 'Send Sarah the Q2 revenue summary so she has it before tomorrow's board prep.'",
          )
      : z
          .string()
          .min(3)
          .max(500)
          .optional()
          .describe(
            "Optional one-sentence intent. Only surfaced if a runtime policy escalates this call to ask-mode.",
          );
    const schema = {
      intent: intentField,
      ...baseSchema,
    };

    const externalMode = modeForScope(d.default_mode, "external");
    const internalMode = modeForScope(d.default_mode, "internal");
    const modeHint =
      d.default_mode === undefined
        ? "Approval policy decided per-call."
        : typeof d.default_mode === "string"
          ? d.default_mode === "auto"
            ? "Auto-approved by default — runs without user confirmation. Operators can override via /settings/rules."
            : "Asks the user for approval before running. You MUST set `intent` to a clear one-sentence explanation."
          : [
              externalMode ? `external: ${externalMode}` : null,
              internalMode ? `internal: ${internalMode}` : null,
            ]
              .filter(Boolean)
              .join(", ") +
            (needsIntent
              ? " — set `intent` whenever the call might land in ask-mode."
              : "");

    return tool(
      d.kind,
      `${d.description}\n\n${modeHint}`,
      schema,
      async (args) => {
        const decision = await controlPlane.evaluateActionPolicy({
          orgId: opts.orgId,
          scope: "external",
          kind: d.kind,
          target:
            typeof args.target === "string" && args.target.length > 0
              ? args.target
              : null,
          riskLevel: (args.risk_level as RiskLevel | undefined) ?? null,
        });

        if (decision.decision === "deny") {
          return {
            content: [
              {
                type: "text" as const,
                text: JSON.stringify({
                  ok: false,
                  decision: "denied",
                  reason: decision.reason,
                  policy: decision.policy.name,
                }),
              },
            ],
          };
        }
        if (decision.decision === "no_policy") {
          return {
            content: [
              {
                type: "text" as const,
                text: JSON.stringify({
                  ok: false,
                  decision: "denied",
                  reason:
                    "no policy matches this scope/kind — refuse for safety. Operator must define a policy in /settings/rules first.",
                }),
              },
            ],
          };
        }

        const intent =
          typeof args.intent === "string" && args.intent.length > 0
            ? args.intent
            : `Auto-fired ${d.kind} (no explicit intent supplied).`;
        const payload =
          (args.payload as Record<string, unknown> | undefined) ?? {};
        const target =
          typeof args.target === "string" && args.target.length > 0
            ? args.target
            : null;
        const riskLevel = (args.risk_level as RiskLevel | undefined) ?? null;

        if (decision.decision === "allow") {
          // Auto-approve path: persist as approved and enqueue the
          // action_execute pg-boss job. We deliberately do NOT call
          // executeApprovedActionRequest inline here — the plugin VM
          // adapters are registered in the worker process, not the
          // web process that hosts /work runs. Enqueuing decouples
          // the two: any process can submit the job, only the worker
          // (which holds the plugin registry + sandbox runtime)
          // runs it.
          const request = await controlPlane.createActionRequest({
            orgId: opts.orgId,
            scope: "external",
            kind: d.kind,
            target,
            payload,
            riskLevel,
            status: "approved",
            policyId: decision.policy.id,
            summary: intent,
            intent,
            workRunId: opts.runId,
            requestedByRunId: null,
          });
          await controlPlane.enqueueActionExecute({
            orgId: opts.orgId,
            actionRequestId: request.id,
          });
          await opts.emit({
            type: "action_request_emit",
            action_request_id: request.id,
            kind: d.kind,
            scope: "external",
            decision: "auto_approved",
            summary: intent,
            ...(riskLevel ? { risk_level: riskLevel } : {}),
          });
          return {
            content: [
              {
                type: "text" as const,
                text: JSON.stringify({
                  ok: true,
                  decision: "auto_approved",
                  action_request_id: request.id,
                  policy: decision.policy.name,
                  status: "queued_for_execution",
                  note: "Action queued. The result will arrive as an action_request_result event in this run — you can stop here; the user will see the outcome inline.",
                }),
              },
            ],
          };
        }

        // needs_approval — emit a card event, end the agent's turn with
        // a "pending" status. Slice 5 wires the approval click + the
        // follow-up run that threads the outcome back into the
        // conversation.
        const request = await controlPlane.createActionRequest({
          orgId: opts.orgId,
          scope: "external",
          kind: d.kind,
          target,
          payload,
          riskLevel,
          status: "pending_approval",
          policyId: decision.policy.id,
          summary: intent,
          intent,
          workRunId: opts.runId,
          requestedByRunId: null,
        });
        await opts.emit({
          type: "action_request_emit",
          action_request_id: request.id,
          kind: d.kind,
          scope: "external",
          decision: "pending_approval",
          intent,
          summary: intent,
          ...(riskLevel ? { risk_level: riskLevel } : {}),
        });
        return {
          content: [
            {
              type: "text" as const,
              text: JSON.stringify({
                ok: true,
                decision: "pending_approval",
                action_request_id: request.id,
                policy: decision.policy.name,
                intent,
                note: "User-approval gate. Tell the user briefly what you've requested and end your turn — the approval card is rendered inline; their decision will land in a follow-up turn.",
              }),
            },
          ],
        };
      },
    );
  });

  return createSdkMcpServer({
    name: "neko_plugin_actions",
    version: "1.0.0",
    tools,
  });
}
