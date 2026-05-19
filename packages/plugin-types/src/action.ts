import { z } from "zod";

export const ActionKindName = z
  .string()
  .min(1)
  .regex(
    /^[a-z][a-z0-9_]*$/,
    "action kind must be lowercase snake_case (matches OpenNeko convention)",
  );

/**
 * Serializable subset of OpenNeko's ActionRequestRecord that crosses the
 * worker ↔ plugin RPC boundary. The worker's plugin loader translates
 * its internal record into this shape before dispatch, and the plugin
 * returns a PluginActionOutcome that the loader translates back.
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

/**
 * Single declared action — a snake_case kind the agent can request,
 * plus the description the agent uses to pick it. Same shape lives in
 * the marketplace entry, the installed manifest, and the plugin's
 * runtime register() result; the worker checks all three agree before
 * dispatching.
 */
export const PluginActionDeclaration = z.object({
  kind: ActionKindName,
  description: z.string().min(1),
});

export type PluginActionDeclaration = z.infer<typeof PluginActionDeclaration>;
