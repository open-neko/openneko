import type { SubscriptionSourceKind } from "./store";

export type WorkflowOutputFilter = {
  scope?: string;
  topic?: string;
  mood?: string | string[];
  kinds?: string[];
  org_id?: string;
};

export type SubscriptionQueryPayload = {
  query: string;
  variables: Record<string, unknown>;
};

/**
 * Build the GraphJin subscription query for a subscription row's filter.
 * The shape is shaped per source_kind:
 *   workflow_output → matches workflow_output rows (the canonical
 *     workflow-chaining case)
 *   source_change   → not implemented yet (will hit source_change_log)
 *   external_event  → not implemented yet
 *
 * Returns null for source kinds that aren't wired in this slice; the
 * subscription manager logs and skips those rows so future work can land
 * additively without breaking existing subscriptions.
 */
export function buildSubscriptionQuery(args: {
  sourceKind: SubscriptionSourceKind;
  filter: Record<string, unknown>;
  orgId: string;
}): SubscriptionQueryPayload | null {
  if (args.sourceKind === "workflow_output") {
    return buildWorkflowOutputSubscription(args.filter, args.orgId);
  }
  return null;
}

function buildWorkflowOutputSubscription(
  filter: Record<string, unknown>,
  orgId: string,
): SubscriptionQueryPayload {
  const f = filter as WorkflowOutputFilter;
  const where: Record<string, unknown> = { org_id: { eq: orgId } };
  if (f.scope) where.scope = { eq: f.scope };
  if (f.topic) where.topic = { eq: f.topic };
  if (f.mood) {
    where.mood = Array.isArray(f.mood) ? { in: f.mood } : { eq: f.mood };
  }
  if (f.kinds && f.kinds.length > 0) {
    where.kind = { in: f.kinds };
  }

  const query = `subscription WorkflowOutputMatch($where: workflow_outputWhereInput) {
  workflow_output(where: $where, order_by: { created_at: desc }, limit: 1) {
    id
    org_id
    workflow_run_id
    kind
    scope
    topic
    mood
    title
    created_at
  }
}`;

  return { query, variables: { where } };
}

export type WorkflowOutputMatch = {
  id: string;
  org_id: string;
  workflow_run_id: string;
  kind: string;
  scope: string | null;
  topic: string | null;
  mood: string | null;
  title: string;
  created_at: string;
};

export function parseWorkflowOutputMatch(
  payload: { data: unknown } | null | undefined,
): WorkflowOutputMatch | null {
  if (!payload || !payload.data || typeof payload.data !== "object") {
    return null;
  }
  const data = payload.data as { workflow_output?: unknown };
  const rows = data.workflow_output;
  if (!Array.isArray(rows) || rows.length === 0) return null;
  const row = rows[0] as Record<string, unknown>;
  if (!row.id || typeof row.id !== "string") return null;
  return {
    id: String(row.id),
    org_id: String(row.org_id),
    workflow_run_id: String(row.workflow_run_id),
    kind: String(row.kind),
    scope: row.scope == null ? null : String(row.scope),
    topic: row.topic == null ? null : String(row.topic),
    mood: row.mood == null ? null : String(row.mood),
    title: String(row.title ?? ""),
    created_at: String(row.created_at),
  };
}
