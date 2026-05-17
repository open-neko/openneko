import { z } from "zod";

/**
 * Serializable subset of OpenNeko's ActionRequestRecord that crosses the
 * worker ↔ plugin RPC boundary. The worker's plugin loader translates
 * its internal record into this shape before dispatch, and the plugin
 * returns a PluginActionOutcome that the loader translates back.
 *
 * Field choices:
 * - omit DB-only fields (workflow_run_id, observation refs, status,
 *   timestamps) — plugins should never depend on those
 * - keep payload as the agent-supplied object the plugin needs to act on
 * - keep target as the canonical resource identifier
 */
export const PluginActionRequest = z.object({
  id: z.string(),
  orgId: z.string(),
  scope: z.string(),
  kind: z.string(),
  target: z.string().nullable(),
  summary: z.string().nullable(),
  payload: z.record(z.string(), z.unknown()).nullable(),
  riskLevel: z.string().nullable(),
});

export type PluginActionRequest = z.infer<typeof PluginActionRequest>;

export const PluginActionOutcome = z.object({
  commandOrOperation: z.string().nullable().optional(),
  externalRef: z.string().nullable().optional(),
  result: z.record(z.string(), z.unknown()).nullable().optional(),
});

export type PluginActionOutcome = z.infer<typeof PluginActionOutcome>;

export const PluginActionDeclaration = z.object({
  kind: z
    .string()
    .min(1)
    .regex(
      /^[a-z][a-z0-9_]*$/,
      "action kind must be lowercase snake_case (matches OpenNeko convention)",
    ),
  description: z.string().min(1),
});

export type PluginActionDeclaration = z.infer<typeof PluginActionDeclaration>;
