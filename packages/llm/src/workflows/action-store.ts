import {
  action_execution,
  action_policy,
  action_request,
  and,
  asc,
  db,
  desc,
  eq,
  work_run,
} from "@neko/db";

export type ActionScope = "internal" | "external";

export type ActionRequestStatus =
  | "draft"
  | "pending_approval"
  | "approved"
  | "rejected"
  | "expired"
  | "executed"
  | "failed"
  | "cancelled";

export type ActionExecutionStatus =
  | "pending"
  | "running"
  | "succeeded"
  | "failed";

export type RiskLevel = "low" | "medium" | "high" | "critical";

export type ActionPolicyMode =
  | "observe_only"
  | "draft_only"
  | "auto_approve"
  | "approval_required"
  | "never";

export type ActionPolicyRecord = {
  id: string;
  orgId: string;
  name: string;
  description: string;
  appliesToKinds: string[];
  appliesToScopes: ActionScope[];
  mode: ActionPolicyMode;
  riskThresholdAutoApprove: RiskLevel | null;
  allowedTargets: Record<string, unknown> | null;
  deniedTargets: Record<string, unknown> | null;
  limits: Record<string, unknown>;
  approverRole: string | null;
  priority: number;
  enabled: boolean;
  createdByThreadId: string | null;
  createdByRunId: string | null;
  createdAt: Date;
  updatedAt: Date;
};

function toPolicyRecord(
  row: typeof action_policy.$inferSelect,
): ActionPolicyRecord {
  return {
    id: row.id,
    orgId: row.org_id,
    name: row.name,
    description: row.description,
    appliesToKinds: row.applies_to_kinds,
    appliesToScopes: row.applies_to_scopes as ActionScope[],
    mode: row.mode as ActionPolicyMode,
    riskThresholdAutoApprove:
      (row.risk_threshold_auto_approve as RiskLevel | null) ?? null,
    allowedTargets:
      (row.allowed_targets as Record<string, unknown> | null) ?? null,
    deniedTargets:
      (row.denied_targets as Record<string, unknown> | null) ?? null,
    limits: (row.limits as Record<string, unknown>) ?? {},
    approverRole: row.approver_role,
    priority: row.priority,
    enabled: row.enabled,
    createdByThreadId: row.created_by_thread_id,
    createdByRunId: row.created_by_run_id,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

export async function listEnabledPolicies(
  orgId: string,
): Promise<ActionPolicyRecord[]> {
  const rows = await db()
    .select()
    .from(action_policy)
    .where(
      and(eq(action_policy.org_id, orgId), eq(action_policy.enabled, true)),
    )
    .orderBy(asc(action_policy.priority));
  return rows.map(toPolicyRecord);
}

export async function listAllPolicies(
  orgId: string,
): Promise<ActionPolicyRecord[]> {
  const rows = await db()
    .select()
    .from(action_policy)
    .where(eq(action_policy.org_id, orgId))
    .orderBy(asc(action_policy.priority), asc(action_policy.name));
  return rows.map(toPolicyRecord);
}

export type CreateActionPolicyInput = Omit<
  ActionPolicyRecord,
  "id" | "createdAt" | "updatedAt" | "createdByThreadId" | "createdByRunId"
> & {
  createdByThreadId?: string | null;
  createdByRunId?: string | null;
};

export async function createActionPolicy(
  input: CreateActionPolicyInput,
): Promise<ActionPolicyRecord> {
  const [row] = await db()
    .insert(action_policy)
    .values({
      org_id: input.orgId,
      name: input.name,
      description: input.description,
      applies_to_kinds: input.appliesToKinds,
      applies_to_scopes: input.appliesToScopes,
      mode: input.mode,
      risk_threshold_auto_approve: input.riskThresholdAutoApprove,
      allowed_targets: input.allowedTargets,
      denied_targets: input.deniedTargets,
      limits: input.limits,
      approver_role: input.approverRole,
      priority: input.priority,
      enabled: input.enabled,
      created_by_thread_id: input.createdByThreadId ?? null,
      created_by_run_id: input.createdByRunId ?? null,
    })
    .returning();
  return toPolicyRecord(row);
}

export async function getActionPolicy(
  orgId: string,
  policyId: string,
): Promise<ActionPolicyRecord | null> {
  const rows = await db()
    .select()
    .from(action_policy)
    .where(and(eq(action_policy.org_id, orgId), eq(action_policy.id, policyId)))
    .limit(1);
  return rows[0] ? toPolicyRecord(rows[0]) : null;
}

export async function getActionPolicyByName(
  orgId: string,
  name: string,
): Promise<ActionPolicyRecord | null> {
  const rows = await db()
    .select()
    .from(action_policy)
    .where(and(eq(action_policy.org_id, orgId), eq(action_policy.name, name)))
    .limit(1);
  return rows[0] ? toPolicyRecord(rows[0]) : null;
}

export type UpsertActionPolicyResult = {
  action: "created" | "updated";
  policy: ActionPolicyRecord;
};

export async function upsertActionPolicyByName(
  input: CreateActionPolicyInput,
): Promise<UpsertActionPolicyResult> {
  const existing = await getActionPolicyByName(input.orgId, input.name);
  if (!existing) {
    const created = await createActionPolicy(input);
    return { action: "created", policy: created };
  }
  const updated = await updateActionPolicy(input.orgId, existing.id, {
    description: input.description,
    appliesToKinds: input.appliesToKinds,
    appliesToScopes: input.appliesToScopes,
    mode: input.mode,
    riskThresholdAutoApprove: input.riskThresholdAutoApprove,
    allowedTargets: input.allowedTargets,
    deniedTargets: input.deniedTargets,
    limits: input.limits,
    approverRole: input.approverRole,
    priority: input.priority,
    enabled: input.enabled,
  });
  if (!updated) throw new Error(`action_policy ${existing.id} disappeared`);
  // Provenance fields (createdByThreadId/createdByRunId) are creation-only:
  // an edit doesn't overwrite the originating thread, so the rule's audit
  // trail still points at the conversation that produced it the first time.
  return { action: "updated", policy: updated };
}

export type UpdateActionPolicyInput = Partial<
  Omit<CreateActionPolicyInput, "orgId">
>;

export async function updateActionPolicy(
  orgId: string,
  policyId: string,
  patch: UpdateActionPolicyInput,
): Promise<ActionPolicyRecord | null> {
  const set: Record<string, unknown> = { updated_at: new Date() };
  if (patch.name !== undefined) set.name = patch.name;
  if (patch.description !== undefined) set.description = patch.description;
  if (patch.appliesToKinds !== undefined)
    set.applies_to_kinds = patch.appliesToKinds;
  if (patch.appliesToScopes !== undefined)
    set.applies_to_scopes = patch.appliesToScopes;
  if (patch.mode !== undefined) set.mode = patch.mode;
  if (patch.riskThresholdAutoApprove !== undefined)
    set.risk_threshold_auto_approve = patch.riskThresholdAutoApprove;
  if (patch.allowedTargets !== undefined)
    set.allowed_targets = patch.allowedTargets;
  if (patch.deniedTargets !== undefined)
    set.denied_targets = patch.deniedTargets;
  if (patch.limits !== undefined) set.limits = patch.limits;
  if (patch.approverRole !== undefined) set.approver_role = patch.approverRole;
  if (patch.priority !== undefined) set.priority = patch.priority;
  if (patch.enabled !== undefined) set.enabled = patch.enabled;

  if (Object.keys(set).length === 1) {
    // only updated_at — nothing meaningful to write
    return getActionPolicy(orgId, policyId);
  }

  const [row] = await db()
    .update(action_policy)
    .set(set)
    .where(and(eq(action_policy.org_id, orgId), eq(action_policy.id, policyId)))
    .returning();
  return row ? toPolicyRecord(row) : null;
}

export type ActionRequestRecord = {
  id: string;
  orgId: string;
  /** SEC5: the human principal + agent backend, snapshotted at creation. */
  actorUserId: string | null;
  actorRole: string | null;
  actorBackend: string | null;
  workflowRunId: string | null;
  triggeredByObservationId: string | null;
  policyId: string | null;
  scope: ActionScope;
  kind: string;
  target: string | null;
  payload: Record<string, unknown>;
  riskLevel: RiskLevel | null;
  status: ActionRequestStatus;
  summary: string | null;
  /**
   * Agent's natural-language framing of *why* this action was
   * requested — populated for ask-mode requests so the inline
   * approval card in /work has a human-authored headline. Null
   * for auto-mode and pre-intent legacy rows.
   */
  intent: string | null;
  /** Agent-estimated, server-clamped minutes of human effort saved. */
  minutesSaved: number | null;
  minutesSavedBasis: string | null;
  /**
   * /work run id this request was emitted from. Lets the worker's
   * runActionExecute emit an action_request_result event into the
   * same run so the chat UI can render the outcome inline. Null
   * for workflow-runner-emitted requests.
   */
  workRunId: string | null;
  requestedByRunId: string | null;
  approvedByUserId: string | null;
  approvedAt: Date | null;
  rejectionReason: string | null;
  createdAt: Date;
  updatedAt: Date;
};

function toRequestRecord(
  row: typeof action_request.$inferSelect,
): ActionRequestRecord {
  return {
    id: row.id,
    orgId: row.org_id,
    actorUserId: row.actor_user_id,
    actorRole: row.actor_role,
    actorBackend: row.actor_backend,
    workflowRunId: row.workflow_run_id,
    triggeredByObservationId: row.triggered_by_observation_id,
    policyId: row.policy_id,
    scope: row.scope as ActionScope,
    kind: row.kind,
    target: row.target,
    payload: (row.payload as Record<string, unknown>) ?? {},
    riskLevel: (row.risk_level as RiskLevel | null) ?? null,
    status: row.status as ActionRequestStatus,
    summary: row.summary,
    intent: row.intent ?? null,
    minutesSaved: row.minutes_saved ?? null,
    minutesSavedBasis: row.minutes_saved_basis ?? null,
    workRunId: row.work_run_id ?? null,
    requestedByRunId: row.requested_by_run_id,
    approvedByUserId: row.approved_by_user_id,
    approvedAt: row.approved_at,
    rejectionReason: row.rejection_reason,
    createdAt: row.created_at,
    updatedAt: row.updated_at,
  };
}

export type CreateActionRequestInput = {
  orgId: string;
  workflowRunId?: string | null;
  triggeredByObservationId?: string | null;
  policyId?: string | null;
  scope: ActionScope;
  kind: string;
  target?: string | null;
  payload?: Record<string, unknown>;
  riskLevel?: RiskLevel | null;
  status: ActionRequestStatus;
  summary?: string | null;
  /** Agent's NL framing — required at the app layer for ask-mode rows. */
  intent?: string | null;
  /** /work run id this request was emitted from (omit for workflow paths). */
  workRunId?: string | null;
  requestedByRunId?: string | null;
  /** Agent-estimated, server-clamped minutes of human effort this saves. */
  minutesSaved?: number | null;
  minutesSavedBasis?: string | null;
  /** SEC5: dual identity. Omit to snapshot from workRunId's K1 actor + backend. */
  actorUserId?: string | null;
  actorRole?: string | null;
  actorBackend?: string | null;
};

export async function createActionRequest(
  input: CreateActionRequestInput,
): Promise<ActionRequestRecord> {
  // SEC5: snapshot the dual identity at creation. When the caller did
  // not resolve it, derive it from the originating work run (K1 actor
  // + agent backend) so every request carries who-via-which-agent.
  let actor = {
    userId: input.actorUserId ?? null,
    role: input.actorRole ?? null,
    backend: input.actorBackend ?? null,
  };
  if (input.workRunId && (!actor.role || !actor.backend)) {
    const [run] = await db()
      .select({
        userId: work_run.actor_user_id,
        role: work_run.actor_role,
        backend: work_run.backend,
      })
      .from(work_run)
      .where(eq(work_run.id, input.workRunId))
      .limit(1);
    if (run) {
      actor = {
        userId: actor.userId ?? run.userId,
        role: actor.role ?? run.role,
        backend: actor.backend ?? run.backend,
      };
    }
  }
  const [row] = await db()
    .insert(action_request)
    .values({
      org_id: input.orgId,
      actor_user_id: actor.userId,
      actor_role: actor.role,
      actor_backend: actor.backend,
      workflow_run_id: input.workflowRunId ?? null,
      triggered_by_observation_id: input.triggeredByObservationId ?? null,
      policy_id: input.policyId ?? null,
      scope: input.scope,
      kind: input.kind,
      target: input.target ?? null,
      payload: input.payload ?? {},
      risk_level: input.riskLevel ?? null,
      status: input.status,
      summary: input.summary ?? null,
      intent: input.intent ?? null,
      minutes_saved: input.minutesSaved ?? null,
      minutes_saved_basis: input.minutesSavedBasis ?? null,
      work_run_id: input.workRunId ?? null,
      requested_by_run_id: input.requestedByRunId ?? null,
    })
    .returning();
  const record = toRequestRecord(row);
  // SEC10: governance events ride the tamper-evident chain.
  const { recordAuditEvent } = await import("./audit-chain");
  await recordAuditEvent({
    orgId: input.orgId,
    entityKind: "action_request",
    entityId: record.id,
    event: `created:${record.status}`,
    payload: {
      kind: record.kind,
      scope: record.scope,
      target: record.target,
      status: record.status,
      actorUserId: record.actorUserId,
      actorRole: record.actorRole,
      actorBackend: record.actorBackend,
    },
  });
  return record;
}

export async function getActionRequest(
  orgId: string,
  id: string,
): Promise<ActionRequestRecord | null> {
  const rows = await db()
    .select()
    .from(action_request)
    .where(and(eq(action_request.org_id, orgId), eq(action_request.id, id)))
    .limit(1);
  return rows[0] ? toRequestRecord(rows[0]) : null;
}

export type ListActionRequestsOptions = {
  orgId: string;
  status?: ActionRequestStatus;
  workflowRunId?: string;
  limit?: number;
};

export async function listActionRequests(
  opts: ListActionRequestsOptions,
): Promise<ActionRequestRecord[]> {
  const filters = [eq(action_request.org_id, opts.orgId)];
  if (opts.status) filters.push(eq(action_request.status, opts.status));
  if (opts.workflowRunId)
    filters.push(eq(action_request.workflow_run_id, opts.workflowRunId));
  const rows = await db()
    .select()
    .from(action_request)
    .where(and(...filters))
    .orderBy(desc(action_request.created_at))
    .limit(opts.limit ?? 100);
  return rows.map(toRequestRecord);
}

export class InvalidActionStatusTransitionError extends Error {
  constructor(from: ActionRequestStatus, to: ActionRequestStatus) {
    super(`invalid action_request transition: ${from} → ${to}`);
    this.name = "InvalidActionStatusTransitionError";
  }
}

function assertTransition(
  current: ActionRequestStatus,
  next: ActionRequestStatus,
  allowedFrom: ActionRequestStatus[],
): void {
  if (!allowedFrom.includes(current)) {
    throw new InvalidActionStatusTransitionError(current, next);
  }
}

export async function approveActionRequest(args: {
  id: string;
  orgId: string;
  approverUserId: string | null;
  /** K2: when supplied, the matched policy's approver_role is enforced.
   *  Legacy callers (no actor context yet) skip the check. */
  approver?: { userId: string | null; role: "admin" | "member" | "service" };
}): Promise<ActionRequestRecord> {
  const existing = await getActionRequest(args.orgId, args.id);
  if (!existing) throw new Error(`action_request ${args.id} not found`);
  if (args.approver) await assertMayDecide(args.orgId, existing, args.approver);
  assertTransition(existing.status, "approved", ["draft", "pending_approval"]);
  const [row] = await db()
    .update(action_request)
    .set({
      status: "approved",
      approved_by_user_id: args.approverUserId,
      approved_at: new Date(),
      updated_at: new Date(),
    })
    .where(eq(action_request.id, args.id))
    .returning();
  const record = toRequestRecord(row);
  const { recordAuditEvent } = await import("./audit-chain");
  await recordAuditEvent({
    orgId: args.orgId,
    entityKind: "action_request",
    entityId: record.id,
    event: "approved",
    payload: { approvedBy: args.approverUserId, kind: record.kind },
  });
  return record;
}

/**
 * K2: enforce the matched policy's approver_role for a decision. Admin
 * always may; member may unless the policy demands a different role;
 * service principals never decide.
 */
async function assertMayDecide(
  orgId: string,
  request: ActionRequestRecord,
  approver: { userId: string | null; role: "admin" | "member" | "service" },
): Promise<void> {
  const { assertCan } = await import("../work/authz");
  const policy = request.policyId
    ? await getActionPolicy(orgId, request.policyId)
    : null;
  assertCan(
    { userId: approver.userId, role: approver.role },
    "approve",
    { kind: "action_approval", approverRole: policy?.approverRole ?? null },
    `action_request ${request.id}`,
  );
}

export async function rejectActionRequest(args: {
  id: string;
  orgId: string;
  approverUserId: string | null;
  reason?: string;
  /** K2: same gate as approve — rejecting is a decision too. */
  approver?: { userId: string | null; role: "admin" | "member" | "service" };
}): Promise<ActionRequestRecord> {
  const existing = await getActionRequest(args.orgId, args.id);
  if (!existing) throw new Error(`action_request ${args.id} not found`);
  if (args.approver) await assertMayDecide(args.orgId, existing, args.approver);
  assertTransition(existing.status, "rejected", ["draft", "pending_approval"]);
  const [row] = await db()
    .update(action_request)
    .set({
      status: "rejected",
      approved_by_user_id: args.approverUserId,
      approved_at: new Date(),
      rejection_reason: args.reason ?? null,
      updated_at: new Date(),
    })
    .where(eq(action_request.id, args.id))
    .returning();
  const record = toRequestRecord(row);
  const { recordAuditEvent } = await import("./audit-chain");
  await recordAuditEvent({
    orgId: args.orgId,
    entityKind: "action_request",
    entityId: record.id,
    event: "rejected",
    payload: {
      rejectedBy: args.approverUserId,
      reason: args.reason ?? null,
      kind: record.kind,
    },
  });
  return record;
}

export async function markActionRequestExecuted(
  id: string,
): Promise<void> {
  await db()
    .update(action_request)
    .set({ status: "executed", updated_at: new Date() })
    .where(eq(action_request.id, id));
}

export async function markActionRequestFailed(
  id: string,
  error: string,
): Promise<void> {
  await db()
    .update(action_request)
    .set({
      status: "failed",
      rejection_reason: error,
      updated_at: new Date(),
    })
    .where(eq(action_request.id, id));
}

export type ActionExecutionRecord = {
  id: string;
  orgId: string;
  actionRequestId: string;
  executor: string;
  commandOrOperation: string | null;
  payload: Record<string, unknown> | null;
  result: Record<string, unknown> | null;
  externalRef: string | null;
  status: ActionExecutionStatus;
  error: string | null;
  startedAt: Date | null;
  finishedAt: Date | null;
  createdAt: Date;
};

function toExecutionRecord(
  row: typeof action_execution.$inferSelect,
): ActionExecutionRecord {
  return {
    id: row.id,
    orgId: row.org_id,
    actionRequestId: row.action_request_id,
    executor: row.executor,
    commandOrOperation: row.command_or_operation,
    payload: (row.payload as Record<string, unknown> | null) ?? null,
    result: (row.result as Record<string, unknown> | null) ?? null,
    externalRef: row.external_ref,
    status: row.status as ActionExecutionStatus,
    error: row.error,
    startedAt: row.started_at,
    finishedAt: row.finished_at,
    createdAt: row.created_at,
  };
}

export async function recordActionExecution(args: {
  orgId: string;
  actionRequestId: string;
  executor: string;
  commandOrOperation?: string | null;
  payload?: Record<string, unknown> | null;
}): Promise<ActionExecutionRecord> {
  const [row] = await db()
    .insert(action_execution)
    .values({
      org_id: args.orgId,
      action_request_id: args.actionRequestId,
      executor: args.executor,
      command_or_operation: args.commandOrOperation ?? null,
      payload: args.payload ?? null,
      status: "running",
      started_at: new Date(),
    })
    .returning();
  return toExecutionRecord(row);
}

export async function finishActionExecution(args: {
  id: string;
  status: "succeeded" | "failed";
  result?: Record<string, unknown> | null;
  externalRef?: string | null;
  error?: string | null;
}): Promise<ActionExecutionRecord> {
  const [row] = await db()
    .update(action_execution)
    .set({
      status: args.status,
      result: args.result ?? null,
      external_ref: args.externalRef ?? null,
      error: args.error ?? null,
      finished_at: new Date(),
    })
    .where(eq(action_execution.id, args.id))
    .returning();
  const record = toExecutionRecord(row);
  const { recordAuditEvent } = await import("./audit-chain");
  await recordAuditEvent({
    orgId: record.orgId,
    entityKind: "action_execution",
    entityId: record.id,
    event: `execution:${record.status}`,
    payload: {
      actionRequestId: record.actionRequestId,
      executor: record.executor,
      status: record.status,
      externalRef: record.externalRef,
      error: record.error,
    },
  });
  return record;
}

export async function listActionExecutions(
  actionRequestId: string,
): Promise<ActionExecutionRecord[]> {
  const rows = await db()
    .select()
    .from(action_execution)
    .where(eq(action_execution.action_request_id, actionRequestId))
    .orderBy(desc(action_execution.created_at));
  return rows.map(toExecutionRecord);
}
