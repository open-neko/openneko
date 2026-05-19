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
 * How an action should be approved by default when the plugin is
 * freshly installed. The host seeds an action_policy row using this
 * value; operators can override later in /settings/rules.
 *
 *  - "auto"  — adapter runs inline with no human gate (read-only or
 *              otherwise low-blast-radius kinds, e.g. web_search)
 *  - "ask"   — agent's turn suspends, user sees an approval card
 *              inline in /work, then the result completes the turn
 *              (externally observable kinds, e.g. send_slack_message)
 *  - "deny"  — the kind is never invokable; declared on plugins that
 *              want a "this exists but I never want the agent calling
 *              it without explicit opt-in" stance
 *
 * Optional: the host falls back to "ask" when omitted — safer to
 * surprise the user with an approval prompt than to fire a side
 * effect they didn't expect.
 */
export const ActionMode = z.enum(["auto", "ask", "deny"]);

export type ActionMode = z.infer<typeof ActionMode>;

/**
 * Per-scope default-mode override. Use when the same action kind
 * should have different blast-radius defaults depending on whether
 * it targets external or internal resources. e.g. a future
 * `send_message` kind might default `external: "ask"` (asking the
 * user before posting to a customer-visible channel) but
 * `internal: "auto"` (auto-firing in a bot-to-bot in-org context).
 *
 * Either key is optional; the host falls back to "ask" for a scope
 * that's omitted.
 */
export const ActionModePerScope = z.object({
  external: ActionMode.optional(),
  internal: ActionMode.optional(),
});

export type ActionModePerScope = z.infer<typeof ActionModePerScope>;

/**
 * The declaration's default_mode accepts either:
 *   - a scalar ActionMode applied to all scopes the kind is invoked
 *     under (the common case — most plugin kinds are external-only
 *     and the operator never sees a difference)
 *   - a per-scope object for kinds that legitimately want different
 *     defaults per scope
 */
export const ActionDefaultMode = z.union([ActionMode, ActionModePerScope]);

export type ActionDefaultMode = z.infer<typeof ActionDefaultMode>;

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
  default_mode: ActionDefaultMode.optional(),
});

export type PluginActionDeclaration = z.infer<typeof PluginActionDeclaration>;

/**
 * Convenience: collapse a declaration's default_mode into a per-scope
 * lookup so callers (the host's policy seeder, the agent tool builder)
 * always see the same shape. Scalar mode becomes both-scopes mode;
 * undefined keys stay undefined (operator falls through to the
 * scope's default policy).
 */
export function resolveDefaultModeForScope(
  declaration: PluginActionDeclaration,
  scope: "external" | "internal",
): ActionMode | undefined {
  const dm = declaration.default_mode;
  if (dm === undefined) return undefined;
  if (typeof dm === "string") return dm;
  return scope === "external" ? dm.external : dm.internal;
}
