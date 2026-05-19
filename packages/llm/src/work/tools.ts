import { createSdkMcpServer, tool } from "@anthropic-ai/claude-agent-sdk";
import { z } from "zod";
import { writeWorkSkill } from "./skills";
import type { AgentEvent, AgentSurfaceMessage } from "../agent-backend";
import {
  WORK_MEMORY_KINDS,
  rememberWorkMemory,
  searchWorkMemoryByContext,
  type WorkMemoryContext,
} from "./memory";
import { enqueue, QUEUE } from "@neko/db/jobs";
import {
  createActionRequest,
  evaluateActionPolicy,
  listEnabledPolicies,
  type RiskLevel,
} from "../workflows";

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

const RENDER_CARDS_DESCRIPTION = [
  "Render structured Neko cards inline in the chat using the A2UI v0.9 protocol.",
  "Prefer this over markdown when you have numeric findings, KPIs, or trends.",
  "",
  "Pass an ARRAY of A2UI v0.9 protocol messages — each must have",
  '`version: "v0.9"` plus exactly one of: `createSurface`, `updateComponents`,',
  "`updateDataModel`, `deleteSurface`. Bare component objects (e.g.",
  '`{ "type": "kpi_group", ... }`) are rejected — they MUST be wrapped in',
  "an `updateComponents` envelope.",
  "",
  "Catalog (catalogId `urn:app:catalog:briefing:v1`):",
  "  - `Markdown` — prose block. Use for ANY narrative text — your response",
  "    prose lives here, never outside the tool call. Props: text (markdown",
  "    string; supports headings, lists, tables, code blocks).",
  "  - `Briefing` — root container. Props: greeting, subtitle, role, children[].",
  "  - `BriefingCard` — KPI card with optional chart. Required props:",
  "    metricId (any string for ad-hoc cards, e.g. 'chat-1'),",
  '    source ("chat" for ad-hoc rendering),',
  '    mood ("good" | "watch" | "act"),',
  "    text (1-sentence headline), metric (e.g. '$498,376'),",
  "    label (e.g. 'Total Profit'), detail (1-3 sentences),",
  '    chartType ("kpi" | "line" | "bar" | "area" | "donut"),',
  "    chartData (array of `{ d: string, v: number, t?: number }`,",
  '    or `[]` when chartType="kpi").',
  "",
  "Typical message sequence:",
  "  1. createSurface (once)",
  "  2. updateComponents with a `Briefing` root + 1-N `BriefingCard` children",
  "",
  "Example (one KPI card, no chart):",
  "[",
  '  { "version":"v0.9", "createSurface":{ "surfaceId":"s1",',
  '    "catalogId":"urn:app:catalog:briefing:v1" } },',
  '  { "version":"v0.9", "updateComponents":{ "surfaceId":"s1", "components":[',
  '    { "id":"root", "component":"Briefing", "greeting":"Top product",',
  '      "subtitle":"All stores", "role":"CEO", "children":["c1"] },',
  '    { "id":"c1", "component":"BriefingCard", "metricId":"chat-1",',
  '      "source":"chat", "mood":"good", "text":"Mountain-200 Silver leads",',
  '      "metric":"$498,376", "label":"Total Profit",',
  '      "detail":"$3.19M revenue − $2.70M cost on 2,130 units sold.",',
  '      "chartType":"kpi", "chartData":[] }',
  "  ]}}",
  "]",
].join("\n");

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
};

export function buildWorkMemoryServer(
  ctx: WorkMemoryContext,
  options: WorkMemoryServerOptions = {},
) {
  const exposeSave = options.exposeSave ?? true;
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
      const results = await searchWorkMemoryByContext({
        orgId: ctx.orgId,
        query: args.query,
        limit: args.limit ?? 5,
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
      const memory = await rememberWorkMemory({
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
        const policies = await listEnabledPolicies(opts.orgId);
        const decision = evaluateActionPolicy(
          {
            scope: "external",
            kind: d.kind,
            target:
              typeof args.target === "string" && args.target.length > 0
                ? args.target
                : null,
            riskLevel: (args.risk_level as RiskLevel | undefined) ?? null,
          },
          policies,
        );

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
          // (which holds the plugin registry + microsandbox runtime)
          // runs it.
          const request = await createActionRequest({
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
          await enqueue(QUEUE.ACTION_EXECUTE, {
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
        const request = await createActionRequest({
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
